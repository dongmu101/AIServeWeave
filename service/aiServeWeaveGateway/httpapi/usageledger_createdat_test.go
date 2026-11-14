package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// flakyUsagePushClient fails its first push, then succeeds.
//
// flakyUsagePushClient 第一次推送失败，之后成功。
type flakyUsagePushClient struct {
	mu    sync.Mutex
	calls [][]UsageRecord
}

func (c *flakyUsagePushClient) PushUsageRecords(_ context.Context, records []UsageRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, append([]UsageRecord(nil), records...))
	if len(c.calls) == 1 {
		return errors.New("control plane unavailable")
	}
	return nil
}

func (c *flakyUsagePushClient) snapshot() [][]UsageRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]UsageRecord(nil), c.calls...)
}

// TestUsageLedgerRetryKeepsTheOriginalCreatedAt pins the contract the control
// plane's partitioned usage_records relies on: its primary key is
// (id, created_at), so a retried batch only deduplicates if every record keeps
// the CreatedAt it was stamped with, through the wire encoding too.
//
// TestUsageLedgerRetryKeepsTheOriginalCreatedAt 固定控制面分区后的 usage_records 所依赖
// 的契约：其主键是 (id, created_at)，因此重试的批次只有在每条记录都保持最初的 CreatedAt
// （经过线上编码后也不变）时才能去重。
func TestUsageLedgerRetryKeepsTheOriginalCreatedAt(t *testing.T) {
	client := &flakyUsagePushClient{}
	clock := gatewaytest.NewClock()
	p := newUsageLedgerPusher(client, clock, nil, nil, usageLedgerPushConfig{BufferSize: 100, BatchSize: 2, FlushInterval: time.Second})
	go p.run()
	defer p.Stop()

	stamped := time.Date(2026, 11, 14, 23, 59, 59, 999999999, time.UTC)
	p.enqueue(UsageRecord{RequestID: "req_1", TenantID: "tnt", Model: "m", CreatedAt: stamped})
	p.enqueue(UsageRecord{RequestID: "req_2", TenantID: "tnt", Model: "m", CreatedAt: stamped.Add(-time.Hour)})
	gatewaytest.WaitFor(t, "the first push to fail", func() bool { return len(client.snapshot()) == 1 })

	// The clock moving on must not restamp anything held for retry.
	// 时钟前进不得给等待重试的记录重新打时间戳。
	gatewaytest.WaitFor(t, "the pusher to arm its flush timer", func() bool { return clock.PendingTimers() >= 1 })
	clock.Advance(time.Second)
	gatewaytest.WaitFor(t, "the retry", func() bool { return len(client.snapshot()) >= 2 })

	calls := client.snapshot()
	first, retried := calls[0], calls[1]
	if len(first) != 2 || len(retried) != 2 {
		t.Fatalf("pushes = %d then %d records, want the same batch twice", len(first), len(retried))
	}
	for i := range first {
		if first[i].RequestID != retried[i].RequestID || !first[i].CreatedAt.Equal(retried[i].CreatedAt) {
			t.Errorf("record %d: first %s@%v, retried %s@%v", i, first[i].RequestID, first[i].CreatedAt, retried[i].RequestID, retried[i].CreatedAt)
		}
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(retried)
	if string(a) != string(b) {
		t.Errorf("retried batch encodes differently:\nfirst   %s\nretried %s", a, b)
	}
}

package httpapi

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

func isolatedUsageOutbox(t *testing.T, consumer string, sink UsageLedgerClient) *usageLedgerOutbox {
	t.Helper()
	addr := os.Getenv("AISW_USAGE_REDIS_ADDR")
	if addr == "" {
		t.Skip("set AISW_USAGE_REDIS_ADDR for the usage outbox integration test")
	}
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 14, ContextTimeoutEnabled: true})
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis unavailable: %v", err)
	}
	p := newUsageLedgerPusher(sink, nil, nil, nil, usageLedgerPushConfig{BatchSize: 10})
	outbox := newUsageLedgerOutbox(rdb, consumer, p)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	outbox.stream += ":" + suffix
	outbox.rejected += ":" + suffix
	outbox.pollEvery = 5 * time.Millisecond
	outbox.claimAfter = 20 * time.Millisecond
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), outbox.stream, outbox.rejected).Err()
		_ = rdb.Close()
	})
	return outbox
}

func TestUsageOutboxReplaysPendingAfterGatewayRestart(t *testing.T) {
	failing := &fakeUsagePushClient{err: fmt.Errorf("control plane offline")}
	first := isolatedUsageOutbox(t, "gateway-before", failing)
	first.start()
	t.Cleanup(first.stop)
	original := UsageRecord{
		RequestID: "req_1", TenantID: "tenant_1", Model: "test-model", Endpoint: UsageEndpointAnthropicMessages,
		CreatedAt: time.Unix(1234567890, 123000).UTC(), PromptTokens: 42, CompletionTokens: 3,
		TotalTokens: 45, Details: map[string]int64{"input.cache_read": 7},
	}
	if !first.enqueue(original) {
		t.Fatal("usage could not be staged")
	}
	gatewaytest.WaitFor(t, "the original gateway attempts delivery", func() bool { return failing.callCount() > 0 })
	first.stop()
	ctx := context.Background()
	if count := first.redis.XLen(ctx, first.stream).Val(); count != 1 {
		t.Fatalf("durable rows after shutdown = %d, want 1", count)
	}
	var recovered fakeUsagePushClient
	second := newUsageLedgerOutbox(first.redis, "gateway-after", newUsageLedgerPusher(&recovered, nil, nil, nil, usageLedgerPushConfig{}))
	second.stream, second.rejected = first.stream, first.rejected
	second.pollEvery, second.claimAfter = first.pollEvery, first.claimAfter
	second.start()
	defer second.stop()
	gatewaytest.WaitFor(t, "the restarted gateway acknowledges the durable row", func() bool {
		count, err := first.redis.XLen(ctx, first.stream).Result()
		return err == nil && count == 0 && recovered.callCount() > 0
	})
	recovered.mu.Lock()
	defer recovered.mu.Unlock()
	if !reflect.DeepEqual(recovered.calls, [][]UsageRecord{{original}}) {
		t.Fatalf("replayed usage = %#v, want the same ID, timestamp and billable details", recovered.calls)
	}
}

type rejectOneUsageRecord struct {
	accepted []string
}

func (p *rejectOneUsageRecord) PushUsageRecords(_ context.Context, records []UsageRecord) error {
	for _, record := range records {
		if record.RequestID == "bad" {
			return ErrUsageBatchRejected
		}
	}
	for _, record := range records {
		p.accepted = append(p.accepted, record.RequestID)
	}
	return nil
}

func TestUsageOutboxIsolatesRejectedRecord(t *testing.T) {
	sink := &rejectOneUsageRecord{}
	outbox := isolatedUsageOutbox(t, "gateway", sink)
	ctx := context.Background()
	for _, id := range []string{"a", "bad", "b"} {
		if !outbox.enqueue(UsageRecord{RequestID: id}) {
			t.Fatalf("enqueue %q failed", id)
		}
	}
	if err := outbox.redis.XGroupCreateMkStream(ctx, outbox.stream, usageOutboxGroup, "0").Err(); err != nil {
		t.Fatal(err)
	}
	batch, _, err := outbox.read(ctx, "0-0")
	if err != nil || len(batch) != 3 {
		t.Fatalf("read batch: %d records, %v", len(batch), err)
	}
	if err := outbox.deliver(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sink.accepted, []string{"a", "b"}) {
		t.Fatalf("accepted records = %v", sink.accepted)
	}
	if count := outbox.redis.XLen(ctx, outbox.stream).Val(); count != 0 {
		t.Fatalf("pending records = %d, want 0", count)
	}
	if rejected := outbox.redis.XRange(ctx, outbox.rejected, "-", "+").Val(); len(rejected) != 1 || rejected[0].Values["reason"] != "controlplane_rejected" {
		t.Fatalf("expected one quarantined record with the rejection reason")
	}
}

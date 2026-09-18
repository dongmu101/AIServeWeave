package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

type fakeUsagePushClient struct {
	mu    sync.Mutex
	calls [][]UsageRecord
	err   error
}

func (c *fakeUsagePushClient) PushUsageRecords(_ context.Context, records []UsageRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, records)
	return c.err
}

func (c *fakeUsagePushClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func TestUsageLedgerPusherFlushesOnBatchSize(t *testing.T) {
	client := &fakeUsagePushClient{}
	p := newUsageLedgerPusher(client, gatewaytest.NewClock(), nil, nil, usageLedgerPushConfig{BufferSize: 100, BatchSize: 2, FlushInterval: time.Hour})
	go p.run()
	defer p.Stop()

	p.enqueue(UsageRecord{RequestID: "req_1"})
	p.enqueue(UsageRecord{RequestID: "req_2"})

	gatewaytest.WaitFor(t, "PushUsageRecords to be called after the batch size was reached", func() bool {
		return client.callCount() > 0
	})
}

func TestUsageLedgerPusherDropsWhenBufferIsFull(t *testing.T) {
	client := &fakeUsagePushClient{}
	// No run() goroutine draining it, so the buffer fills immediately.
	p := newUsageLedgerPusher(client, gatewaytest.NewClock(), nil, nil, usageLedgerPushConfig{BufferSize: 1, BatchSize: 10, FlushInterval: time.Hour})

	if ok := p.enqueue(UsageRecord{RequestID: "req_1"}); !ok {
		t.Fatal("enqueue() = false on the first record, want true (buffer has room)")
	}
	if ok := p.enqueue(UsageRecord{RequestID: "req_2"}); ok {
		t.Fatal("enqueue() = true on the second record, want false (buffer is full and must drop, not block)")
	}
}

func TestUsageLedgerPusherFlushesOnTimerTick(t *testing.T) {
	client := &fakeUsagePushClient{}
	clock := gatewaytest.NewClock()
	p := newUsageLedgerPusher(client, clock, nil, nil, usageLedgerPushConfig{BufferSize: 100, BatchSize: 100, FlushInterval: time.Second})
	go p.run()
	defer p.Stop()

	p.enqueue(UsageRecord{RequestID: "req_1"})

	gatewaytest.WaitFor(t, "the pusher to arm its flush timer", func() bool { return clock.PendingTimers() >= 1 })
	if got := client.callCount(); got != 0 {
		t.Fatalf("PushUsageRecords call count before the flush timer fired = %d, want 0 (only %d record enqueued, below BatchSize)", got, 1)
	}

	clock.Advance(time.Second)
	gatewaytest.WaitFor(t, "PushUsageRecords to be called after the flush timer fired", func() bool {
		return client.callCount() > 0
	})
}

func TestUsageLedgerPusherStopReturnsPromptly(t *testing.T) {
	client := &fakeUsagePushClient{}
	p := newUsageLedgerPusher(client, gatewaytest.NewClock(), nil, nil, usageLedgerPushConfig{BufferSize: 100, BatchSize: 100, FlushInterval: time.Hour})
	go p.run()
	p.enqueue(UsageRecord{RequestID: "req_1"})

	done := make(chan struct{})
	go func() { p.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(gatewaytest.Timeout):
		t.Fatal("Stop() did not return; the run loop likely did not exit")
	}
}

package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakePushClient struct {
	mu    sync.Mutex
	calls [][]RequestLogRecord
	err   error
}

func (c *fakePushClient) PushRequestLogs(_ context.Context, records []RequestLogRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, records)
	return c.err
}

func (c *fakePushClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func TestRequestLogPusherFlushesOnBatchSize(t *testing.T) {
	client := &fakePushClient{}
	p := newRequestLogPusher(client, nil, nil, requestLogPushConfig{BufferSize: 100, BatchSize: 2, FlushInterval: time.Hour})
	go p.run()
	defer p.Stop()

	p.enqueue(RequestLogRecord{RequestID: "req_1"})
	p.enqueue(RequestLogRecord{RequestID: "req_2"})

	deadline := time.After(2 * time.Second)
	for client.callCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("PushRequestLogs was never called after the batch size was reached")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestRequestLogPusherDropsWhenBufferIsFull(t *testing.T) {
	client := &fakePushClient{}
	// No run() goroutine draining it, so the buffer fills immediately.
	p := newRequestLogPusher(client, nil, nil, requestLogPushConfig{BufferSize: 1, BatchSize: 10, FlushInterval: time.Hour})

	if ok := p.enqueue(RequestLogRecord{RequestID: "req_1"}); !ok {
		t.Fatal("enqueue() = false on the first record, want true (buffer has room)")
	}
	if ok := p.enqueue(RequestLogRecord{RequestID: "req_2"}); ok {
		t.Fatal("enqueue() = true on the second record, want false (buffer is full and must drop, not block)")
	}
}

func TestRequestLogPusherStopFlushesWhatItCanAndReturns(t *testing.T) {
	client := &fakePushClient{}
	p := newRequestLogPusher(client, nil, nil, requestLogPushConfig{BufferSize: 100, BatchSize: 100, FlushInterval: time.Hour})
	go p.run()
	p.enqueue(RequestLogRecord{RequestID: "req_1"})

	done := make(chan struct{})
	go func() { p.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return; the run loop likely did not exit")
	}
}

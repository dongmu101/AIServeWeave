package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
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
	p := newRequestLogPusher(client, gatewaytest.NewClock(), nil, nil, requestLogPushConfig{BufferSize: 100, BatchSize: 2, FlushInterval: time.Hour})
	go p.run()
	defer p.Stop()

	p.enqueue(RequestLogRecord{RequestID: "req_1"})
	p.enqueue(RequestLogRecord{RequestID: "req_2"})

	gatewaytest.WaitFor(t, "PushRequestLogs to be called after the batch size was reached", func() bool {
		return client.callCount() > 0
	})
}

func TestRequestLogPusherDropsWhenBufferIsFull(t *testing.T) {
	client := &fakePushClient{}
	// No run() goroutine draining it, so the buffer fills immediately.
	p := newRequestLogPusher(client, gatewaytest.NewClock(), nil, nil, requestLogPushConfig{BufferSize: 1, BatchSize: 10, FlushInterval: time.Hour})

	if ok := p.enqueue(RequestLogRecord{RequestID: "req_1"}); !ok {
		t.Fatal("enqueue() = false on the first record, want true (buffer has room)")
	}
	if ok := p.enqueue(RequestLogRecord{RequestID: "req_2"}); ok {
		t.Fatal("enqueue() = true on the second record, want false (buffer is full and must drop, not block)")
	}
}

// TestRequestLogPusherFlushesOnTimerTick exercises run's `case <-ticker:`
// branch specifically: fewer records than BatchSize are enqueued, so the
// only thing that can trigger a flush is the FlushInterval timer, not the
// batch-size threshold. It advances a fake clock rather than waiting on the
// real one, mirroring jobsync_test.go's
// TestJobSyncerAdvancesANonTerminalJobWithoutBeingPolled — requestLogPusher.run
// was explicitly modeled on jobSyncer.run's own timer-driven tick.
//
// TestRequestLogPusherFlushesOnTimerTick 专门检验 run 的 `case <-ticker:` 分支：
// 入队的记录数少于 BatchSize，因此唯一能触发 flush 的只有 FlushInterval
// 定时器，而不是攒批阈值。它推进一个假时钟而不是等待真实时钟，仿照
// jobsync_test.go 的 TestJobSyncerAdvancesANonTerminalJobWithoutBeingPolled——
// requestLogPusher.run 正是照 jobSyncer.run 自己那套定时器驱动的写法建的模。
func TestRequestLogPusherFlushesOnTimerTick(t *testing.T) {
	client := &fakePushClient{}
	clock := gatewaytest.NewClock()
	p := newRequestLogPusher(client, clock, nil, nil, requestLogPushConfig{BufferSize: 100, BatchSize: 100, FlushInterval: time.Second})
	go p.run()
	defer p.Stop()

	p.enqueue(RequestLogRecord{RequestID: "req_1"})

	gatewaytest.WaitFor(t, "the pusher to arm its flush timer", func() bool { return clock.PendingTimers() >= 1 })
	if got := client.callCount(); got != 0 {
		t.Fatalf("PushRequestLogs call count before the flush timer fired = %d, want 0 (only %d record enqueued, below BatchSize)", got, 1)
	}

	clock.Advance(time.Second)
	gatewaytest.WaitFor(t, "PushRequestLogs to be called after the flush timer fired", func() bool {
		return client.callCount() > 0
	})
}

// TestRequestLogPusherStopReturnsPromptly confirms that Stop returns within a
// bounded time. It does not assert that a flush specifically happened: run's
// select between <-p.stop and <-p.ch is non-deterministic, so the enqueued
// record may already have been picked up by the run loop before Stop closes
// p.stop, or it may be flushed by Stop-triggered final flush instead — both
// are correct outcomes. The one guarantee this test pins down is that Stop
// does not hang.
//
// TestRequestLogPusherStopReturnsPromptly 确认 Stop 会在有限时间内返回。它不
// 断言"确实发生了一次 flush"：run 里 <-p.stop 与 <-p.ch 之间的 select 是不确定
// 的，入队的记录可能在 Stop 关闭 p.stop 之前就已被 run 循环取走，也可能是被
// Stop 触发的最后一次 flush 处理——两种结果都对。本测试钉住的唯一保证是
// Stop 不会挂起。
func TestRequestLogPusherStopReturnsPromptly(t *testing.T) {
	client := &fakePushClient{}
	p := newRequestLogPusher(client, gatewaytest.NewClock(), nil, nil, requestLogPushConfig{BufferSize: 100, BatchSize: 100, FlushInterval: time.Hour})
	go p.run()
	p.enqueue(RequestLogRecord{RequestID: "req_1"})

	done := make(chan struct{})
	go func() { p.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(gatewaytest.Timeout):
		t.Fatal("Stop() did not return; the run loop likely did not exit")
	}
}

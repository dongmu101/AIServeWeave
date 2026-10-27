package httpapi

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// blockingResponsesClient's CreateResponseTurn blocks until release is
// closed, the same "hold the slot open on purpose" technique
// TestRequestLogPusherDropsWhenBufferIsFull's unstarted pusher achieves by
// never draining — here the persister's own goroutine drains immediately,
// so this file has to make the call itself the thing that stays open.
//
// blockingResponsesClient 的 CreateResponseTurn 会阻塞到 release 被关闭为
// 止——与 TestRequestLogPusherDropsWhenBufferIsFull 里"不启动 run() 所以缓冲
// 立刻占满"是同一种"刻意让占用保持敞开"的手法，只是这里持久化器自己的
// goroutine 会立刻消费，因此本文件必须让调用本身保持敞开。
type blockingResponsesClient struct {
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (c *blockingResponsesClient) CreateResponseTurn(_ context.Context, _, _, _, _ string, _ json.RawMessage) error {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	<-c.release
	return nil
}

func (c *blockingResponsesClient) GetResponseTurn(_ context.Context, _, _ string) (string, json.RawMessage, error) {
	return "", nil, ErrResponseTurnNotFound
}

func (c *blockingResponsesClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestResponsePersisterDropsWhenConcurrencyIsFull is the response-turn
// persister's version of TestRequestLogPusherDropsWhenBufferIsFull: a
// caller must never block on persist, so a full concurrency limit drops the
// turn and counts it instead of waiting for a slot.
//
// TestResponsePersisterDropsWhenConcurrencyIsFull 是轮次持久化器版本的
// TestRequestLogPusherDropsWhenBufferIsFull：调用方绝不能阻塞在 persist 上，
// 因此并发上限已满时会丢弃这一轮并计数，而不是等待一个空位。
func TestResponsePersisterDropsWhenConcurrencyIsFull(t *testing.T) {
	client := &blockingResponsesClient{release: make(chan struct{})}
	defer close(client.release)
	mx := metricstest.New()
	p := newResponsePersister(client, 1, time.Second, discardLogger(), newRecorder(mx))

	// The first call's channel send happens synchronously inside persist,
	// before its goroutine is even scheduled — so by the time persist
	// returns, the sole slot is already taken regardless of how the
	// blocking goroutine below is scheduled.
	//
	// 第一次调用的 channel 发送是在 persist 内部同步发生的，甚至在它的
	// goroutine 被调度之前——因此 persist 一返回，唯一的空位就已经被占满，
	// 无论下面那个阻塞的 goroutine 何时才被调度都不影响这一点。
	p.persist("resp_1", "tnt_1", "", "model", json.RawMessage(`[]`))
	p.persist("resp_2", "tnt_1", "", "model", json.RawMessage(`[]`))
	p.persist("resp_3", "tnt_1", "", "model", json.RawMessage(`[]`))

	if got := mx.Sum(MetricResponsePersistDroppedTotal, nil); got != 2 {
		t.Fatalf("%s = %v, want 2 (two turns dropped while the one slot was held)", MetricResponsePersistDroppedTotal, got)
	}
	gatewaytest.WaitFor(t, "the first call to actually reach CreateResponseTurn", func() bool {
		return client.callCount() == 1
	})
}

// TestResponsePersisterCountsAFailedCall covers the other metric: a turn
// that reached the network but whose call failed is distinct from one
// dropped for lack of a concurrency slot.
//
// TestResponsePersisterCountsAFailedCall 覆盖另一个指标：一轮已经发起了
// 调用、却失败的情形，与因缺少并发空位而被丢弃的情形不同。
func TestResponsePersisterCountsAFailedCall(t *testing.T) {
	client := &failingResponsesClient{}
	mx := metricstest.New()
	p := newResponsePersister(client, 4, time.Second, discardLogger(), newRecorder(mx))

	p.persist("resp_1", "tnt_1", "", "model", json.RawMessage(`[]`))

	gatewaytest.WaitFor(t, "the failed counter to record the failed call", func() bool {
		return mx.Sum(MetricResponsePersistFailedTotal, nil) > 0
	})
}

type failingResponsesClient struct{}

func (failingResponsesClient) CreateResponseTurn(context.Context, string, string, string, string, json.RawMessage) error {
	return context.DeadlineExceeded
}

func (failingResponsesClient) GetResponseTurn(context.Context, string, string) (string, json.RawMessage, error) {
	return "", nil, ErrResponseTurnNotFound
}

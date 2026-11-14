package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// onePassQueue is a scheduler.WaitQueue that runs attempt at most once more,
// recording that the scheduler asked it to wait.
type onePassQueue struct {
	waits atomic.Int32
	full  bool
	key   string
}

func (q *onePassQueue) Wait(_ context.Context, key string, firstErr error, attempt func() (error, bool)) error {
	q.key = key
	q.waits.Add(1)
	if q.full {
		return firstErr
	}
	err, _ := attempt()
	return err
}

// busyThenWorkingHandler answers the first chat with backpressure, then serves.
func busyThenWorkingHandler(source string, count *atomic.Int32) gatewaytest.SlotHandler {
	working := chatHandler(source, nil)
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if count.Add(1) == 1 {
			return &gatewaytest.WireError{Code: "backpressure", Message: "no idle slot", Retryable: true}
		}
		return working(req, body, reply)
	}
}

func TestChatWaitsOnTheInjectedQueueOnlyWhenEveryCandidateIsBusy(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	handler := busyThenWorkingHandler("node-a", &count)
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), handler, handler)

	queue := &onePassQueue{}
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, ChatQueue: queue})
	resp, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "served by node-a" || queue.waits.Load() != 1 {
		t.Fatalf("content=%q waits=%d, want served after one queued wait", resp.Message.Content, queue.waits.Load())
	}
	if queue.key != "qwen3:8b" {
		t.Fatalf("lane key = %q, want the requested model", queue.key)
	}

	// A request that succeeds on the first pass never touches the queue.
	if _, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"}); err != nil {
		t.Fatalf("second Chat: %v", err)
	}
	if queue.waits.Load() != 1 {
		t.Fatalf("waits = %d, want the unsaturated request to skip the queue", queue.waits.Load())
	}
}

func TestChatQueueRefusalReturnsTheBusyError(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), backpressureHandler(&count))

	queue := &onePassQueue{full: true}
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, ChatQueue: queue})
	_, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) || rerr.Code != runtime.ErrorBackpressure {
		t.Fatalf("err = %v, want the first pass's backpressure", err)
	}
}

func TestChatNeverWaitsOnANonBusyFailure(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), nonRetryableHandler(&count))

	queue := &onePassQueue{}
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, ChatQueue: queue})
	if _, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"}); err == nil {
		t.Fatal("Chat succeeded, want the non-retryable failure")
	}
	if queue.waits.Load() != 0 {
		t.Fatal("a non-busy failure must not enter the queue")
	}
}

// TestSubmitWorkflowMakesExactlyOnePass pins that waiting for workflow
// capacity lives above the scheduler (jobqueue): a busy node answers at once
// and is contacted exactly once.
func TestSubmitWorkflowMakesExactlyOnePass(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"), backpressureHandler(&count))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	_, _, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(`{}`)})
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) || rerr.Code != runtime.ErrorBackpressure || !scheduler.SubmitRetryable(err) {
		t.Fatalf("err = %v, want a retryable backpressure answered at once", err)
	}
	if got := count.Load(); got != 1 {
		t.Errorf("the node was submitted to %d times, want 1", got)
	}
	if n := h.Clock.PendingTimers(); n != 0 {
		t.Errorf("PendingTimers() = %d, want 0: the scheduler must not wait", n)
	}
}

// TestSubmitWorkflowNonRetryableFailureIsNotRetryable pins the classification
// the queue relies on to never resubmit a workflow the backend may have seen.
func TestSubmitWorkflowNonRetryableFailureIsNotRetryable(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	handler := func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		count.Add(1)
		return &gatewaytest.WireError{Code: "upstream_error", Message: "backend said no", Retryable: true}
	}
	connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"), handler)

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	_, _, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(`{}`)})
	if err == nil || scheduler.SubmitRetryable(err) {
		t.Fatalf("err = %v, want a failure SubmitRetryable rejects", err)
	}
	if got := count.Load(); got != 1 {
		t.Errorf("the node was submitted to %d times, want 1", got)
	}
}

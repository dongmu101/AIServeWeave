package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// flakyThenWorkingWorkflowHandler answers the first failThreshold submits
// with a retryable backpressure error — the "every candidate busy" state
// STATUS.md's P2 bounded queueing waits on — then answers every submit
// after that as workflowHandler would.
func flakyThenWorkingWorkflowHandler(source string, failThreshold int32, count *atomic.Int32) gatewaytest.SlotHandler {
	working := workflowHandler(source, nil)
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		n := count.Add(1)
		if req.GetOperation() == tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT && n <= failThreshold {
			return &gatewaytest.WireError{Code: "backpressure", Message: "no capacity", Retryable: true}
		}
		return working(req, body, reply)
	}
}

// TestSubmitWorkflowQueuesUntilCapacityFreesUp is STATUS.md's P2 bounded task
// queueing's success path: every candidate is busy on the first attempt, but
// capacity frees up before QueueMaxWait elapses, so the submission that
// would have failed immediately before this feature existed now succeeds.
func TestSubmitWorkflowQueuesUntilCapacityFreesUp(t *testing.T) {
	mx := metricstest.New()
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	// Fails the first two submits, then succeeds — so the queue loop must
	// re-poll at least twice before the request is served.
	connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"),
		flakyThenWorkingWorkflowHandler("node-a", 2, &count))

	sched := scheduler.New(h.Srv, scheduler.Config{
		Clock: h.Clock, Metrics: mx,
		QueueMaxWait: 10 * time.Second, QueueRetryInterval: time.Second,
	})

	type result struct {
		run       runtime.WorkflowRun
		candidate scheduler.Candidate
		err       error
	}
	done := make(chan result, 1)
	go func() {
		run, c, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(`{}`)})
		done <- result{run, c, err}
	}()

	// The first attempt runs synchronously before any timer is armed, so by
	// the time a retry timer shows up the failing attempt has already
	// happened; advancing twice drives the handler from fail, fail, to
	// success.
	gatewaytest.WaitFor(t, "the queue's retry timer to arm", func() bool { return h.Clock.PendingTimers() == 1 })
	h.Clock.Advance(time.Second)
	gatewaytest.WaitFor(t, "the queue's second retry timer to arm", func() bool { return h.Clock.PendingTimers() == 1 })
	h.Clock.Advance(time.Second)

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("SubmitWorkflow() error = %v, want nil", res.err)
		}
		if res.candidate.NodeID != "node-a" || res.run.ID != "prompt-node-a" {
			t.Errorf("SubmitWorkflow() = %+v run %q, want node-a/prompt-node-a", res.candidate, res.run.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SubmitWorkflow() did not return after capacity freed up")
	}
	if got := count.Load(); got != 3 {
		t.Errorf("the node was submitted to %d times, want 3 (2 failures + 1 success)", got)
	}

	if got := mx.Sum(scheduler.MetricQueueRejectedTotal, nil); got != 0 {
		t.Errorf("%s = %v, want 0: the queue was never full", scheduler.MetricQueueRejectedTotal, got)
	}
	if s := mx.Find(scheduler.MetricQueueWaitSeconds, map[string]string{scheduler.LabelOutcome: "resolved"}); s == nil {
		t.Errorf("%s{outcome=resolved} was never observed", scheduler.MetricQueueWaitSeconds)
	}
}

// TestSubmitWorkflowQueueTimesOutWhenCapacityNeverFreesUp confirms the wait
// is bounded: a candidate that stays busy forever eventually gets the same
// failure back a caller would have seen immediately without queueing, once
// QueueMaxWait elapses.
func TestSubmitWorkflowQueueTimesOutWhenCapacityNeverFreesUp(t *testing.T) {
	mx := metricstest.New()
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"), backpressureWorkflowHandler(&count))

	sched := scheduler.New(h.Srv, scheduler.Config{
		Clock: h.Clock, Metrics: mx,
		QueueMaxWait: 3 * time.Second, QueueRetryInterval: time.Second,
	})

	done := make(chan error, 1)
	go func() {
		_, _, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(`{}`)})
		done <- err
	}()

	for range 3 {
		gatewaytest.WaitFor(t, "the queue's retry timer to arm", func() bool { return h.Clock.PendingTimers() == 1 })
		h.Clock.Advance(time.Second)
	}

	select {
	case err := <-done:
		var rerr *runtime.RuntimeError
		if !errors.As(err, &rerr) || rerr.Code != runtime.ErrorBackpressure {
			t.Fatalf("SubmitWorkflow() error = %v, want a backpressure RuntimeError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SubmitWorkflow() did not return once the queue's wait budget was exhausted")
	}

	if s := mx.Find(scheduler.MetricQueueWaitSeconds, map[string]string{scheduler.LabelOutcome: "timeout"}); s == nil {
		t.Errorf("%s{outcome=timeout} was never observed", scheduler.MetricQueueWaitSeconds)
	}
}

// TestSubmitWorkflowQueueRejectsBeyondMaxWaiters is the queue's other bound:
// once QueueMaxWaiters submissions are already waiting, one more is refused
// immediately rather than becoming an unbounded third waiter.
func TestSubmitWorkflowQueueRejectsBeyondMaxWaiters(t *testing.T) {
	mx := metricstest.New()
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"), backpressureWorkflowHandler(&count))

	sched := scheduler.New(h.Srv, scheduler.Config{
		Clock: h.Clock, Metrics: mx,
		QueueMaxWait: 10 * time.Second, QueueRetryInterval: time.Second, QueueMaxWaiters: 1,
	})

	// The first submission occupies the queue's single slot and blocks
	// there until the test lets it go.
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(`{}`)})
		firstDone <- err
	}()
	gatewaytest.WaitFor(t, "the first submission to occupy the queue", func() bool { return h.Clock.PendingTimers() == 1 })

	// A second submission arrives while the queue is full and must be
	// rejected immediately, without waiting for a timer.
	_, _, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(`{}`)})
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) || rerr.Code != runtime.ErrorBackpressure {
		t.Fatalf("second SubmitWorkflow() error = %v, want a backpressure RuntimeError returned without queueing", err)
	}
	if got := mx.Sum(scheduler.MetricQueueRejectedTotal, nil); got != 1 {
		t.Errorf("%s = %v, want 1", scheduler.MetricQueueRejectedTotal, got)
	}

	// Let the first submission's wait run out so the test does not leak it.
	h.Clock.Advance(10 * time.Second)
	<-firstDone
}

// TestSubmitWorkflowDoesNotQueueWhenNoCandidateExists confirms
// attemptSubmitWorkflow's ErrNoCapableNode path never enters the queue, even
// with queueing configured: retrying cannot make a capability appear.
func TestSubmitWorkflowDoesNotQueueWhenNoCandidateExists(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, QueueMaxWait: 10 * time.Second})
	_, _, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(`{}`)})
	if !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Errorf("err = %v, want ErrNoCapableNode returned immediately", err)
	}
	if n := h.Clock.PendingTimers(); n != 0 {
		t.Errorf("PendingTimers() = %d, want 0: no queue wait should have started", n)
	}
}

// TestSubmitWorkflowDoesNotQueueWhenTheOnlyCandidateLacksCapacity is the
// interaction the P2 resource-aware-scheduling design doc's §六.4 called out
// by name: admission-threshold filtering (nodeHasCapacity) must exclude an
// undersized node before bounded queueing ever gets a turn, because no
// amount of waiting turns a declared-insufficient GPU memory total into a
// sufficient one. This is the workflow-path sibling of
// TestSubmitWorkflowDoesNotQueueWhenNoCandidateExists: there the gap is no
// node at all, here it is a node that exists but is filtered before
// pickBy's ranking ever sees it, so attemptSubmitWorkflow must observe the
// same zero-candidate outcome and never mark itself exhausted.
//
// TestSubmitWorkflowDoesNotQueueWhenTheOnlyCandidateLacksCapacity 是 P2
// 资源感知调度设计文档 §六.4 点名的交互：准入门槛过滤（nodeHasCapacity）必须在
// 有界排队轮到之前就排除一个显存不够的节点，因为无论等多久，一个声明不足的显存
// 总量都不会变得足够。它是 TestSubmitWorkflowDoesNotQueueWhenNoCandidateExists
// 在工作流路径上的姊妹测试：那边的缺口是完全没有节点，这里是节点存在但在
// pickBy 的排序看到它之前就被过滤掉了，因此 attemptSubmitWorkflow 必须观察到
// 同样的零候选结果，绝不能把自己标记为"耗尽重试"。
func TestSubmitWorkflowDoesNotQueueWhenTheOnlyCandidateLacksCapacity(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	h.ConnectWithResources(t, "node-small", "comfy-1", &tunnelv1.NodeResources{GpuMemoryBytes: 8 << 30},
		workflowCapableSnapshot("comfy-1"), workflowHandler("node-small", &count))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, QueueMaxWait: 10 * time.Second})
	_, _, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{
		Template: json.RawMessage(`{}`), MinGPUMemoryBytes: 16 << 30,
	})
	if !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Errorf("err = %v, want ErrNoCapableNode returned immediately", err)
	}
	if n := h.Clock.PendingTimers(); n != 0 {
		t.Errorf("PendingTimers() = %d, want 0: a capacity-excluded candidate must not start a queue wait", n)
	}
	if count.Load() != 0 {
		t.Errorf("the undersized node was contacted %d times, want 0", count.Load())
	}
}

// TestSubmitWorkflowDoesNotQueueOnANonRetryableFailure confirms a failure
// submitRetryable rejects skips the queue even with queueing configured: an
// upstream error may already have queued the workflow on the backend, and
// waiting to retry it would risk a second generation.
func TestSubmitWorkflowDoesNotQueueOnANonRetryableFailure(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	handler := func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		count.Add(1)
		return &gatewaytest.WireError{Code: "upstream_error", Message: "backend said no", Retryable: true}
	}
	connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"), handler)

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, QueueMaxWait: 10 * time.Second})
	_, _, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("SubmitWorkflow() error = nil, want the non-retryable failure returned")
	}
	if n := h.Clock.PendingTimers(); n != 0 {
		t.Errorf("PendingTimers() = %d, want 0: a non-retryable failure must not queue", n)
	}
	if got := count.Load(); got != 1 {
		t.Errorf("the node was submitted to %d times, want 1 (no retry, queued or otherwise)", got)
	}
}

// TestSubmitWorkflowQueueingDisabledByDefaultMatchesTodaysBehavior confirms
// the zero value of Config.QueueMaxWait keeps failing immediately, the same
// as every Scheduler built before this feature existed.
func TestSubmitWorkflowQueueingDisabledByDefaultMatchesTodaysBehavior(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"), backpressureWorkflowHandler(&count))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	_, _, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(`{}`)})
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) || rerr.Code != runtime.ErrorBackpressure {
		t.Fatalf("SubmitWorkflow() error = %v, want a backpressure RuntimeError returned immediately", err)
	}
	if got := count.Load(); got != 1 {
		t.Errorf("the node was submitted to %d times, want 1: no queueing without QueueMaxWait", got)
	}
}

// backpressureWorkflowHandler always answers a workflow submit with a
// retryable backpressure error.
func backpressureWorkflowHandler(count *atomic.Int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if count != nil {
			count.Add(1)
		}
		return &gatewaytest.WireError{Code: "backpressure", Message: "no capacity", Retryable: true}
	}
}

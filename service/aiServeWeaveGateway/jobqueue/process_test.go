package jobqueue_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/jobqueue"
	"AIServeWeave/service/aiServeWeaveGateway/jobqueue/jobqueuetest"
)

// autoClock advances itself by d whenever a timer of d is armed, so hold and
// deadline loops run instantly and deterministically.
type autoClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *autoClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *autoClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- now
	return ch, func() bool { return false }
}

var (
	errBusy      = errors.New("busy")
	errUpstream  = errors.New("upstream")
	errForbidden = errors.New("forbidden")
)

func classify(err error) jobqueue.Outcome {
	switch {
	case errors.Is(err, errBusy):
		return jobqueue.OutcomeRetry
	case errors.Is(err, errForbidden):
		return jobqueue.OutcomeDenied
	default:
		return jobqueue.OutcomeUnknown
	}
}

// fakeDispatcher answers Submit from a script and records what happened.
type fakeDispatcher struct {
	mu         sync.Mutex
	answers    []error
	submits    int
	dispatched []jobqueue.Binding
	cancelled  int
	onSubmit   func()
}

func (d *fakeDispatcher) Submit(context.Context, jobqueue.Task) (jobqueue.Binding, error) {
	d.mu.Lock()
	d.submits++
	var err error
	if len(d.answers) > 0 {
		err, d.answers = d.answers[0], d.answers[1:]
	}
	hook := d.onSubmit
	d.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return jobqueue.Binding{}, err
	}
	return jobqueue.Binding{NodeID: "node-a", RuntimeID: "comfy-1", RunID: "prompt-1"}, nil
}

func (d *fakeDispatcher) Dispatched(_ jobqueue.Task, b jobqueue.Binding) {
	d.mu.Lock()
	d.dispatched = append(d.dispatched, b)
	d.mu.Unlock()
}

func (d *fakeDispatcher) CancelDispatched(context.Context, jobqueue.Task, jobqueue.Binding) {
	d.mu.Lock()
	d.cancelled++
	d.mu.Unlock()
}

type fixture struct {
	t         *testing.T
	clock     *autoClock
	store     *jobqueuetest.Store
	transport *jobqueuetest.Transport
	replica   *jobqueuetest.Handle
	queue     *jobqueue.Queue
	disp      *fakeDispatcher
}

func newFixture(t *testing.T, mutate func(*jobqueue.Config)) *fixture {
	t.Helper()
	f := &fixture{t: t, clock: &autoClock{now: time.Unix(1_700_000_000, 0)}, store: jobqueuetest.NewStore(),
		transport: jobqueuetest.NewTransport(), disp: &fakeDispatcher{}}
	f.replica = f.transport.Replica()
	cfg := jobqueue.Config{Store: f.store, Transport: f.replica, Classify: classify, ReplicaID: "r1",
		MaxWait: 2 * time.Minute, MaxLength: 4, RetryInterval: time.Second, Clock: f.clock}
	if mutate != nil {
		mutate(&cfg)
	}
	q, err := jobqueue.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Start(f.disp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Shutdown)
	f.queue = q
	return f
}

func (f *fixture) enqueue(jobID string) {
	f.t.Helper()
	now := f.clock.Now()
	if _, err := f.queue.Enqueue(context.Background(), jobqueue.Task{
		Kind: jobqueue.KindRun, JobID: jobID, TenantID: "t1", WorkflowID: "wf", EnqueuedAt: now, Deadline: now.Add(f.queue.MaxWait()),
	}); err != nil {
		f.t.Fatalf("Enqueue: %v", err)
	}
}

func (f *fixture) record(jobID string) jobqueue.Record {
	f.t.Helper()
	rec, ok, err := f.queue.Lookup(context.Background(), jobID)
	if err != nil || !ok {
		f.t.Fatalf("Lookup(%s) = %v %v", jobID, ok, err)
	}
	return rec
}

func (f *fixture) deliver(jobID string) error {
	return f.transport.Deliver(context.Background(), f.replica, jobID)
}

func TestEnqueueAdmitsUpToTheClusterLength(t *testing.T) {
	f := newFixture(t, func(c *jobqueue.Config) { c.MaxLength = 2 })
	f.enqueue("job_1")
	f.enqueue("job_2")
	_, err := f.queue.Enqueue(context.Background(), jobqueue.Task{JobID: "job_3", Deadline: f.clock.Now().Add(time.Minute)})
	if !errors.Is(err, jobqueue.ErrQueueFull) {
		t.Fatalf("Enqueue beyond the length = %v, want ErrQueueFull", err)
	}
	if position, _ := f.queue.Position(context.Background(), "job_2"); position != 2 {
		t.Fatalf("position = %d, want 2", position)
	}
}

func TestEnqueueRefusesAnOversizedTask(t *testing.T) {
	f := newFixture(t, nil)
	big := []byte(`"` + strings.Repeat("a", jobqueue.MaxPayloadBytes) + `"`)
	_, err := f.queue.Enqueue(context.Background(), jobqueue.Task{JobID: "job_big", Graph: big})
	if !errors.Is(err, jobqueue.ErrTooLarge) {
		t.Fatalf("Enqueue = %v, want ErrTooLarge", err)
	}
}

func TestEnqueueTransportFailureWithdrawsTheJob(t *testing.T) {
	f := newFixture(t, nil)
	f.transport.SetEnqueueErr(errors.New("asynq down"))
	_, err := f.queue.Enqueue(context.Background(), jobqueue.Task{JobID: "job_1", Deadline: f.clock.Now().Add(time.Minute)})
	if !errors.Is(err, jobqueue.ErrUnavailable) {
		t.Fatalf("Enqueue = %v, want ErrUnavailable", err)
	}
	if rec := f.record("job_1"); rec.State != jobqueue.StateCancelled {
		t.Fatalf("state = %s, want the admitted record withdrawn", rec.State)
	}
}

func TestEnqueueStoreFailureIsUnavailable(t *testing.T) {
	f := newFixture(t, nil)
	f.store.SetErr(errors.New("redis down"))
	_, err := f.queue.Enqueue(context.Background(), jobqueue.Task{JobID: "job_1"})
	if !errors.Is(err, jobqueue.ErrUnavailable) {
		t.Fatalf("Enqueue = %v, want ErrUnavailable", err)
	}
}

func TestDeliveryDispatchesAndRecordsTheBinding(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	if err := f.deliver("job_1"); err != nil {
		t.Fatal(err)
	}
	rec := f.record("job_1")
	if rec.State != jobqueue.StateDispatched || rec.Binding.NodeID != "node-a" || rec.Binding.RunID != "prompt-1" {
		t.Fatalf("record = %+v", rec)
	}
	if len(f.disp.dispatched) != 1 {
		t.Fatal("the local job store was not told")
	}
	if position, _ := f.queue.Position(context.Background(), "job_1"); position != 0 {
		t.Fatalf("a dispatched job still holds position %d", position)
	}
}

func TestBusyDeliveryHoldsThenRequeues(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	busy := make([]error, 100)
	for i := range busy {
		busy[i] = errBusy
	}
	f.disp.answers = busy
	if err := f.deliver("job_1"); !errors.Is(err, jobqueue.ErrRequeue) {
		t.Fatalf("deliver = %v, want ErrRequeue after holding", err)
	}
	if f.disp.submits < 2 {
		t.Fatalf("submits = %d, want the worker to re-poll while holding", f.disp.submits)
	}
	if rec := f.record("job_1"); rec.State != jobqueue.StateQueued {
		t.Fatalf("state = %s, want queued again for another replica", rec.State)
	}
	// Capacity frees up: the next delivery dispatches.
	f.disp.answers = nil
	if err := f.deliver("job_1"); err != nil {
		t.Fatal(err)
	}
	if rec := f.record("job_1"); rec.State != jobqueue.StateDispatched {
		t.Fatalf("state = %s, want dispatched", rec.State)
	}
}

func TestDeadlineFailsWithQueueTimeout(t *testing.T) {
	f := newFixture(t, func(c *jobqueue.Config) { c.MaxWait = 3 * time.Second })
	f.enqueue("job_1")
	busy := make([]error, 100)
	for i := range busy {
		busy[i] = errBusy
	}
	f.disp.answers = busy
	if err := f.deliver("job_1"); err != nil {
		t.Fatalf("deliver = %v, want a terminal nil", err)
	}
	if rec := f.record("job_1"); rec.State != jobqueue.StateFailed || rec.ErrorCode != jobqueue.CodeQueueTimeout {
		t.Fatalf("record = %+v, want failed/queue_timeout", rec)
	}
}

func TestUnknownOutcomeIsNeverResubmitted(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	f.disp.answers = []error{errUpstream}
	if err := f.deliver("job_1"); err != nil {
		t.Fatal(err)
	}
	if rec := f.record("job_1"); rec.State != jobqueue.StateFailed || rec.ErrorCode != jobqueue.CodeSubmitOutcomeUnknown {
		t.Fatalf("record = %+v", rec)
	}
	if f.disp.submits != 1 {
		t.Fatalf("submits = %d, want exactly 1", f.disp.submits)
	}
}

func TestDeniedFailsWithNodeNotAllowed(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	f.disp.answers = []error{errForbidden}
	_ = f.deliver("job_1")
	if rec := f.record("job_1"); rec.ErrorCode != jobqueue.CodeNodeNotAllowed {
		t.Fatalf("record = %+v", rec)
	}
}

// TestRedeliveryAfterACrashMidSubmitDoesNotResubmit is the at-least-once
// hazard: a worker recorded its intent, possibly reached ComfyUI, and died.
func TestRedeliveryAfterACrashMidSubmitDoesNotResubmit(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	f.store.SetState("job_1", jobqueue.StateDispatching)
	if err := f.deliver("job_1"); err != nil {
		t.Fatal(err)
	}
	if f.disp.submits != 0 {
		t.Fatalf("submits = %d, want 0", f.disp.submits)
	}
	if rec := f.record("job_1"); rec.State != jobqueue.StateFailed || rec.ErrorCode != jobqueue.CodeSubmitOutcomeUnknown {
		t.Fatalf("record = %+v", rec)
	}
}

func TestCancelWhileQueuedSkipsDispatch(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	result, err := f.queue.Cancel(context.Background(), "job_1")
	if err != nil || result != jobqueue.CancelDone {
		t.Fatalf("Cancel = %v %v", result, err)
	}
	if len(f.transport.Pending()) != 0 {
		t.Fatal("the cancelled task was not dropped from the transport")
	}
	if result, _ := f.queue.Cancel(context.Background(), "job_1"); result != jobqueue.CancelFinished {
		t.Fatalf("second Cancel = %v, want finished", result)
	}
}

func TestCancelDuringSubmitInterruptsTheRun(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	f.disp.onSubmit = func() {
		result, err := f.queue.Cancel(context.Background(), "job_1")
		if err != nil || result != jobqueue.CancelRequested {
			t.Errorf("Cancel during submit = %v %v", result, err)
		}
	}
	if err := f.deliver("job_1"); err != nil {
		t.Fatal(err)
	}
	if f.disp.cancelled != 1 {
		t.Fatal("a cancel requested during the submit must interrupt the dispatched run")
	}
}

func TestADispatchedOrCancelledJobIsNotRunAgain(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	if err := f.deliver("job_1"); err != nil {
		t.Fatal(err)
	}
	// A duplicate delivery (asynq is at least once) finds it dispatched.
	_ = f.replica.Enqueue(context.Background(), "job_1", mustPayload(t, "job_1"), time.Minute)
	if err := f.deliver("job_1"); err != nil {
		t.Fatal(err)
	}
	if f.disp.submits != 1 {
		t.Fatalf("submits = %d, want 1", f.disp.submits)
	}
}

func TestAwaitDispatchReportsPositionsAndTheOutcome(t *testing.T) {
	// The auto-advancing clock moves on every await poll, so the deadline is
	// far away to keep the deliveries below from timing out.
	f := newFixture(t, func(c *jobqueue.Config) { c.MaxWait = 100000 * time.Hour })
	f.enqueue("job_1")
	f.enqueue("job_2")

	var positions []int
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	rec, err := f.queue.AwaitDispatch(ctx, "job_2", func(p int) { positions = append(positions, p) })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || rec.State != jobqueue.StateQueued {
		t.Fatalf("AwaitDispatch while queued = %+v %v", rec, err)
	}
	if len(positions) == 0 || positions[0] != 2 || positions[len(positions)-1] != 2 {
		t.Fatalf("positions = %v, want every poll to report position 2", positions[:min(len(positions), 5)])
	}

	_ = f.deliver("job_1")
	_ = f.deliver("job_2")
	rec, err = f.queue.AwaitDispatch(context.Background(), "job_2", nil)
	if err != nil || rec.State != jobqueue.StateDispatched || rec.Binding.RunID != "prompt-1" {
		t.Fatalf("AwaitDispatch after dispatch = %+v %v", rec, err)
	}
}

func mustPayload(t *testing.T, jobID string) []byte {
	t.Helper()
	return []byte(`{"v":1,"kind":"run","job_id":"` + jobID + `","workflow_id":"wf","graph":null,"enqueued_at":"2023-11-14T22:13:20Z","deadline":"2100-01-01T00:00:00Z"}`)
}

// TestCancelDuringABusyAttemptIsNotLost: the cancel lands while a worker
// holds the job between a busy submit and its requeue. The job must end
// cancelled, never be submitted again, and never reach a node.
func TestCancelDuringABusyAttemptIsNotLost(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	busy := make([]error, 100)
	for i := range busy {
		busy[i] = errBusy
	}
	f.disp.answers = busy
	cancelled := false
	f.disp.onSubmit = func() {
		if !cancelled {
			cancelled = true
			if result, _ := f.queue.Cancel(context.Background(), "job_1"); result != jobqueue.CancelRequested {
				t.Errorf("cancel while dispatching = %v", result)
			}
		}
	}
	if err := f.deliver("job_1"); err != nil {
		t.Fatalf("deliver = %v, want the cancel to end the delivery", err)
	}
	if rec := f.record("job_1"); rec.State != jobqueue.StateCancelled {
		t.Fatalf("state = %s, want cancelled", rec.State)
	}
	if f.disp.submits != 1 {
		t.Fatalf("submits = %d, want no further attempt after the cancel", f.disp.submits)
	}
}

// TestTerminalWriteFailureHandsTheTaskBack: a deadline reached while Redis
// rejects the write must not end the delivery as if it were recorded.
func TestTerminalWriteFailureHandsTheTaskBack(t *testing.T) {
	f := newFixture(t, func(c *jobqueue.Config) { c.MaxWait = time.Second })
	f.enqueue("job_1")
	f.disp.answers = []error{errBusy, errBusy, errBusy}
	f.disp.onSubmit = func() { f.store.SetErr(errors.New("redis down")) }
	if err := f.deliver("job_1"); !errors.Is(err, jobqueue.ErrRequeue) {
		t.Fatalf("deliver = %v, want the task handed back", err)
	}
	f.store.SetErr(nil)
	// The redelivery finds the intent left behind and ends the job visibly.
	if err := f.deliver("job_1"); err != nil {
		t.Fatal(err)
	}
	if rec := f.record("job_1"); rec.State != jobqueue.StateFailed {
		t.Fatalf("state = %s, want a decided failure", rec.State)
	}
}

// TestAJobWithNoTaskBehindItExpires: an enqueue that crashed after Admit
// leaves a record no worker will ever read; reading it past the horizon ends it.
func TestAJobWithNoTaskBehindItExpires(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	_ = f.replica.Delete(context.Background(), "job_1") // the task never made it
	f.store.Age("job_1", 24*time.Hour)
	rec, ok, err := f.queue.Lookup(context.Background(), "job_1")
	if err != nil || !ok || rec.State != jobqueue.StateFailed || rec.ErrorCode != jobqueue.CodeQueueTimeout {
		t.Fatalf("Lookup of an orphaned record = %+v %v %v, want failed/queue_timeout", rec, ok, err)
	}
	if position, _ := f.queue.Position(context.Background(), "job_1"); position != 0 {
		t.Fatalf("an expired record still holds position %d", position)
	}
}

// TestAwaitDispatchRidesOutATransientStoreError: a few failed reads are
// tolerated; a sustained failure is reported.
func TestAwaitDispatchRidesOutATransientStoreError(t *testing.T) {
	f := newFixture(t, func(c *jobqueue.Config) { c.MaxWait = 100000 * time.Hour })
	f.enqueue("job_1")
	f.store.FailSnapshots(3)
	polls := 0
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := f.queue.AwaitDispatch(ctx, "job_1", func(int) { polls++ })
	if !errors.Is(err, context.DeadlineExceeded) || polls == 0 {
		t.Fatalf("AwaitDispatch = %v polls=%d, want it to resume polling after the blip", err, polls)
	}

	f.store.FailSnapshots(1000)
	if _, err := f.queue.AwaitDispatch(context.Background(), "job_1", nil); err == nil {
		t.Fatal("a sustained store failure must be reported")
	}
}

func TestBindingWriteFailureStillRecordsTheRunLocally(t *testing.T) {
	f := newFixture(t, nil)
	f.enqueue("job_1")
	f.disp.onSubmit = func() { f.store.SetErr(errors.New("redis down")) }
	if err := f.deliver("job_1"); !errors.Is(err, jobqueue.ErrRequeue) {
		t.Fatalf("deliver = %v, want the task handed back after the binding write failed", err)
	}
	if len(f.disp.dispatched) != 1 {
		t.Fatal("the run a node accepted must still reach the local job store")
	}
	f.store.SetErr(nil)
	if err := f.deliver("job_1"); err != nil {
		t.Fatal(err)
	}
	if f.disp.submits != 1 {
		t.Fatalf("submits = %d, want the accepted run never submitted again", f.disp.submits)
	}
}

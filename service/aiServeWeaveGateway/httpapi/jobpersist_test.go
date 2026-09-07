package httpapi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// fakePersistClient answers CreateJob and UpdateJobState from scripted
// results, and records every call it received — the fields it was given as
// well as which job id — so a test can assert both on the persister's
// dispatch pattern and on exactly what it sent.
//
// fakePersistClient 从脚本化的结果里应答 CreateJob 与 UpdateJobState，并记录
// 收到的每一次调用——包括拿到的字段与是哪个 job id——好让测试既能断言持久化器
// 的分派模式，也能断言它究竟发送了什么。
type fakePersistClient struct {
	mu            sync.Mutex
	createCalls   []createCall
	updateCalls   []updateCall
	inFlight      int
	maxInFlight   int
	createErr     func(jobID string) error
	updateErr     func(jobID string) error
	updateApplied bool
	artifactCalls []artifactCall
	artifactErr   func(jobID, artifactID string) error
}

type artifactCall struct {
	jobID, artifactID, tenantID, filename, subfolder, artifactType string
}

type createCall struct {
	jobID, tenantID, workflowID, workflowVersion, nodeID, runtimeID, backendRunID, state string
	observedSeq                                                                          int64
}

type updateCall struct {
	tenantID, jobID, state, errorSummary string
	observedSeq                          int64
}

func newFakePersistClient() *fakePersistClient {
	return &fakePersistClient{updateApplied: true}
}

func (f *fakePersistClient) CreateJob(_ context.Context, jobID, tenantID, workflowID, workflowVersion, nodeID, runtimeID, backendRunID, state string, observedSeq int64) error {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.createCalls = append(f.createCalls, createCall{jobID, tenantID, workflowID, workflowVersion, nodeID, runtimeID, backendRunID, state, observedSeq})
	f.mu.Unlock()

	time.Sleep(time.Millisecond)

	f.mu.Lock()
	f.inFlight--
	errFn := f.createErr
	f.mu.Unlock()
	if errFn != nil {
		return errFn(jobID)
	}
	return nil
}

func (f *fakePersistClient) UpdateJobState(_ context.Context, tenantID, jobID, state, errorSummary string, observedSeq int64) (bool, error) {
	f.mu.Lock()
	f.updateCalls = append(f.updateCalls, updateCall{tenantID, jobID, state, errorSummary, observedSeq})
	errFn := f.updateErr
	applied := f.updateApplied
	f.mu.Unlock()
	if errFn != nil {
		if err := errFn(jobID); err != nil {
			return false, err
		}
	}
	return applied, nil
}

func (f *fakePersistClient) CreateJobArtifact(_ context.Context, jobID, artifactID, tenantID, filename, subfolder, artifactType string) error {
	f.mu.Lock()
	f.artifactCalls = append(f.artifactCalls, artifactCall{jobID, artifactID, tenantID, filename, subfolder, artifactType})
	errFn := f.artifactErr
	f.mu.Unlock()
	if errFn != nil {
		return errFn(jobID, artifactID)
	}
	return nil
}

func (f *fakePersistClient) createCallCount(jobID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.createCalls {
		if c.jobID == jobID {
			n++
		}
	}
	return n
}

func (f *fakePersistClient) updateCallCount(jobID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.updateCalls {
		if c.jobID == jobID {
			n++
		}
	}
	return n
}

func (f *fakePersistClient) lastUpdate(jobID string) (updateCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.updateCalls) - 1; i >= 0; i-- {
		if f.updateCalls[i].jobID == jobID {
			return f.updateCalls[i], true
		}
	}
	return updateCall{}, false
}

var errUnreachable = errors.New("fake: the control plane did not answer")

func TestJobPersisterCreatesThenUpdatesAJobAsItObservesChanges(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())

	client := newFakePersistClient()
	p := newJobPersister(jobs, client, clock, discardLogger(), jobPersistConfig{Interval: time.Second})

	p.tick()
	if got := client.createCallCount("job_1"); got != 1 {
		t.Fatalf("create calls after first tick = %d, want 1", got)
	}
	if got := client.updateCallCount("job_1"); got != 0 {
		t.Fatalf("update calls after first tick (nothing observed since create) = %d, want 0", got)
	}

	// A real observation (state actually changes) makes the job due again.
	//
	// 一次真实观测（状态确有变化）会让这个 job 重新到期。
	jobs.update("job_1", runtime.WorkflowStatus{State: runtime.WorkflowRunning}, clock.Now())
	clock.Advance(time.Second)
	p.tick()
	if got := client.createCallCount("job_1"); got != 1 {
		t.Errorf("create calls after the second tick = %d, want still 1 (already persisted)", got)
	}
	last, ok := client.lastUpdate("job_1")
	if !ok {
		t.Fatal("no UpdateJobState call was recorded")
	}
	if last.state != "running" || last.observedSeq != 1 {
		t.Errorf("last update = %+v, want state=running observed_seq=1", last)
	}

	// Nothing changed: the next tick has nothing to report.
	//
	// 什么都没变：下一轮无事可报。
	clock.Advance(time.Second)
	p.tick()
	if got := client.updateCallCount("job_1"); got != 1 {
		t.Errorf("update calls after a tick with no new observation = %d, want still 1", got)
	}
}

func TestJobPersisterRetriesTheSameJobIDOnAmbiguousFailureRatherThanResubmitting(t *testing.T) {
	// This test's structural point is as important as its assertions:
	// jobPersister has no scheduler dependency at all — see its struct
	// definition — so it is architecturally incapable of resubmitting a
	// workflow to a node. The only thing a failed attempt can do is ask
	// again about the very same job id, which is what is asserted below.
	//
	// 本测试的结构性意义与它的断言同样重要：jobPersister 根本没有 scheduler
	// 依赖——见它的结构体定义——因此它在架构上就不可能把工作流重新提交给某个
	// 节点。一次失败的尝试唯一能做的，就是再次就同一个 job id 发问，这正是
	// 下面所断言的。
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())

	client := newFakePersistClient()
	client.createErr = func(string) error { return errUnreachable }
	p := newJobPersister(jobs, client, clock, discardLogger(), jobPersistConfig{Interval: time.Second, MaxBackoff: 8 * time.Second})

	p.tick()
	if got := client.createCallCount("job_1"); got != 1 {
		t.Fatalf("create calls after tick 1 = %d, want 1", got)
	}

	clock.Advance(time.Second)
	p.tick()
	if got := client.createCallCount("job_1"); got != 2 {
		t.Fatalf("create calls after tick 2 = %d, want 2 (retried with the same job id)", got)
	}
	c := client.createCalls[len(client.createCalls)-1]
	if c.jobID != "job_1" || c.backendRunID != "run-1" {
		t.Errorf("retry sent {job_id: %q, backend_run_id: %q}, want the original identifiers unchanged", c.jobID, c.backendRunID)
	}

	client.createErr = nil
	clock.Advance(2 * time.Second)
	p.tick()
	if got := client.createCallCount("job_1"); got != 3 {
		t.Fatalf("create calls once the control plane recovers = %d, want 3", got)
	}
	j, _ := jobs.forPersist("job_1")
	if !j.persisted {
		t.Error("job.persisted = false after a successful create, want true")
	}
}

func TestJobPersisterBoundsBatchSizeAndConcurrency(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	const total = 20
	for i := range total {
		addTestJob(jobs, "job_"+string(rune('a'+i)), "tenant-a", "run_"+string(rune('a'+i)), clock.Now())
	}

	client := newFakePersistClient()
	const batch, concurrency = 5, 2
	p := newJobPersister(jobs, client, clock, discardLogger(), jobPersistConfig{
		Interval: time.Second, BatchSize: batch, Concurrency: concurrency,
	})

	p.tick()

	client.mu.Lock()
	calls, maxInFlight := len(client.createCalls), client.maxInFlight
	client.mu.Unlock()

	if calls != batch {
		t.Errorf("create calls made in one tick = %d, want exactly %d (the configured batch size)", calls, batch)
	}
	if maxInFlight > concurrency {
		t.Errorf("max concurrent calls = %d, want at most %d", maxInFlight, concurrency)
	}
}

func TestJobPersisterPersistsATerminalJobEvenThoughTheSyncerWouldStopPollingIt(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())
	jobs.update("job_1", runtime.WorkflowStatus{State: runtime.WorkflowSucceeded}, clock.Now())

	client := newFakePersistClient()
	p := newJobPersister(jobs, client, clock, discardLogger(), jobPersistConfig{Interval: time.Second})
	p.tick()

	if got := client.createCallCount("job_1"); got != 1 {
		t.Fatalf("create calls for a terminal job = %d, want 1 — dueForPersist must not exclude terminal jobs", got)
	}
	last, ok := client.lastUpdate("job_1")
	if !ok || last.state != "succeeded" {
		t.Fatalf("last update = %+v, ok=%v, want state=succeeded", last, ok)
	}
}

func TestJobPersisterNudgeWakesTheLoopSoonerThanItsInterval(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)

	client := newFakePersistClient()
	p := newJobPersister(jobs, client, clock, discardLogger(), jobPersistConfig{Interval: time.Hour})
	go p.run()
	defer p.Stop()

	gatewaytest.WaitFor(t, "the persister to arm its first timer", func() bool { return clock.PendingTimers() >= 1 })
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())
	p.nudge()

	gatewaytest.WaitFor(t, "the nudge to trigger a create call", func() bool {
		return client.createCallCount("job_1") > 0
	})
}

func TestJobPersisterNudgeOnANilPersisterIsANoOp(t *testing.T) {
	var p *jobPersister
	p.nudge() // must not panic
}

func TestJobPersisterStopWaitsForTheInFlightTickAndExitsPromptly(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())

	started := make(chan struct{})
	release := make(chan struct{})
	client := newFakePersistClient()
	client.createErr = func(string) error {
		close(started)
		<-release
		return nil
	}
	p := newJobPersister(jobs, client, clock, discardLogger(), jobPersistConfig{Interval: time.Second})
	go p.run()

	gatewaytest.WaitFor(t, "the persister to arm its first timer", func() bool { return clock.PendingTimers() >= 1 })
	clock.Advance(time.Second)
	<-started

	stopped := make(chan struct{})
	go func() {
		p.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop returned before the in-flight call finished")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	select {
	case <-stopped:
	case <-time.After(gatewaytest.Timeout):
		t.Fatal("Stop did not return after the in-flight call finished")
	}
}

func TestJobPersisterReportsArtifactsMintedByListArtifacts(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())
	jobs.recordArtifacts("job_1", []runtime.ArtifactRef{
		{RunID: "run-1", Filename: "out.png", Subfolder: "", Type: "output"},
	})

	client := newFakePersistClient()
	p := newJobPersister(jobs, client, clock, discardLogger(), jobPersistConfig{Interval: time.Second})

	p.tick()

	client.mu.Lock()
	calls := append([]artifactCall(nil), client.artifactCalls...)
	client.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("artifact calls after first tick = %d, want 1", len(calls))
	}
	if calls[0].jobID != "job_1" || calls[0].tenantID != "tenant-a" || calls[0].filename != "out.png" {
		t.Errorf("artifact call = %+v, want job_1/tenant-a/out.png", calls[0])
	}

	_, pending, ok := jobs.artifactsForPersist("job_1")
	if !ok || len(pending) != 0 {
		t.Errorf("pending artifacts after a successful report = %v, want none", pending)
	}

	// A tick with nothing new pending makes no further calls.
	//
	// 没有新的待确认产物时，下一轮不会再发起调用。
	clock.Advance(time.Second)
	p.tick()
	client.mu.Lock()
	calls = append([]artifactCall(nil), client.artifactCalls...)
	client.mu.Unlock()
	if len(calls) != 1 {
		t.Errorf("artifact calls after a tick with nothing new pending = %d, want still 1", len(calls))
	}
}

func TestJobPersisterRetriesOnlyTheArtifactsStillOwedAfterAPartialFailure(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())
	jobs.recordArtifacts("job_1", []runtime.ArtifactRef{
		{RunID: "run-1", Filename: "a.png", Type: "output"},
		{RunID: "run-1", Filename: "b.png", Type: "output"},
	})

	client := newFakePersistClient()
	client.artifactErr = func(_, artifactID string) error {
		if artifactID == "" {
			return nil
		}
		return errUnreachable
	}
	_, initial, _ := jobs.artifactsForPersist("job_1")
	failing := initial[0].ArtifactID
	client.artifactErr = func(_, artifactID string) error {
		if artifactID == failing {
			return errUnreachable
		}
		return nil
	}

	p := newJobPersister(jobs, client, clock, discardLogger(), jobPersistConfig{Interval: time.Second, MaxBackoff: 8 * time.Second})
	p.tick()

	_, pending, ok := jobs.artifactsForPersist("job_1")
	if !ok || len(pending) != 1 || pending[0].ArtifactID != failing {
		t.Fatalf("pending artifacts after a partial failure = %+v, want only %q", pending, failing)
	}

	client.artifactErr = nil
	clock.Advance(2 * time.Second)
	p.tick()

	_, pending, ok = jobs.artifactsForPersist("job_1")
	if !ok || len(pending) != 0 {
		t.Errorf("pending artifacts once the control plane recovers = %v, want none", pending)
	}
	client.mu.Lock()
	total := len(client.artifactCalls)
	client.mu.Unlock()
	if total != 3 {
		t.Errorf("total artifact calls = %d, want 3 (2 in the first batch, 1 retry)", total)
	}
}

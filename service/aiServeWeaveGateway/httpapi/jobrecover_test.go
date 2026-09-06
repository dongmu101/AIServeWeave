package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// fakeRecoverCandidates answers WorkflowCapableCandidates with a fixed,
// swappable list, so a test can simulate a fleet without a tunnel.
//
// fakeRecoverCandidates 用一份固定、可替换的列表应答 WorkflowCapableCandidates，
// 好让测试无需隧道即可模拟一个机群。
type fakeRecoverCandidates struct {
	mu         sync.Mutex
	candidates []scheduler.Candidate
}

func (f *fakeRecoverCandidates) WorkflowCapableCandidates() []scheduler.Candidate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scheduler.Candidate(nil), f.candidates...)
}

func (f *fakeRecoverCandidates) set(candidates ...scheduler.Candidate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.candidates = candidates
}

// fakeRecoveryClient answers ListActiveJobsForRoute from a scripted,
// per-route table and records which routes were asked about.
//
// fakeRecoveryClient 从一份按路由编排的脚本化表中应答 ListActiveJobsForRoute，
// 并记录被问起过哪些路由。
type fakeRecoveryClient struct {
	mu      sync.Mutex
	asked   []scheduler.Candidate
	byRoute map[scheduler.Candidate][]RecoveredJob
	err     error
}

func newFakeRecoveryClient() *fakeRecoveryClient {
	return &fakeRecoveryClient{byRoute: map[scheduler.Candidate][]RecoveredJob{}}
}

func (f *fakeRecoveryClient) ListActiveJobsForRoute(_ context.Context, nodeID, runtimeID string) ([]RecoveredJob, error) {
	c := scheduler.Candidate{NodeID: nodeID, RuntimeID: runtimeID}
	f.mu.Lock()
	f.asked = append(f.asked, c)
	err := f.err
	jobs := f.byRoute[c]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

func (f *fakeRecoveryClient) forRoute(c scheduler.Candidate, jobs ...RecoveredJob) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byRoute[c] = jobs
}

func (f *fakeRecoveryClient) askedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

func TestJobRecovererAddsAJobItDidNotAlreadyKnowAboutWithSeededSequence(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	route := scheduler.Candidate{NodeID: "node-1", RuntimeID: "comfy-1"}

	client := newFakeRecoveryClient()
	client.forRoute(route, RecoveredJob{
		JobID: "job_1", TenantID: "tenant-a", WorkflowID: "wf",
		NodeID: route.NodeID, RuntimeID: route.RuntimeID, BackendRunID: "prompt-1",
		State: "running", ObservedSeq: 7, CreatedAt: clock.Now(), UpdatedAt: clock.Now(),
	})
	sched := &fakeRecoverCandidates{}
	sched.set(route)
	r := newJobRecoverer(jobs, sched, client, clock, discardLogger(), jobRecoverConfig{})

	r.tick()

	j, ok := jobs.get("job_1", "tenant-a")
	if !ok {
		t.Fatal("job_1 was not recovered")
	}
	if j.State != runtime.WorkflowRunning || j.Candidate != route || j.RunID != "prompt-1" {
		t.Errorf("recovered job = %+v, want state=running candidate=%v run_id=prompt-1", j, route)
	}
	if j.ObservedSeq != 7 {
		t.Errorf("recovered job.ObservedSeq = %d, want 7 (seeded from the control plane, not reset to 0)", j.ObservedSeq)
	}

	full, ok := jobs.forPersist("job_1")
	if !ok {
		t.Fatal("job_1 not found via forPersist")
	}
	if !full.persisted || full.persistedSeq != 7 {
		t.Errorf("recovered job persistence bookkeeping = {persisted: %v, persistedSeq: %d}, want {true, 7} — otherwise jobPersister would treat an already-current record as needing a redundant write", full.persisted, full.persistedSeq)
	}
	if full.needsPersist() {
		t.Error("a freshly recovered job reports needsPersist() = true, want false — its control-plane record is already current by definition")
	}
}

func TestJobRecovererDoesNotOverwriteAJobThisReplicaAlreadyKnows(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	route := scheduler.Candidate{NodeID: "node-1", RuntimeID: "comfy-1"}
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())
	// Give the locally-known job a real, non-terminal state so it stays
	// comparable to a "the control plane thinks it is further along" reply.
	jobs.update("job_1", runtime.WorkflowStatus{State: runtime.WorkflowRunning}, clock.Now())

	client := newFakeRecoveryClient()
	client.forRoute(route, RecoveredJob{
		JobID: "job_1", TenantID: "tenant-a", WorkflowID: "wf",
		NodeID: route.NodeID, RuntimeID: route.RuntimeID, BackendRunID: "run-1",
		State: "succeeded", ObservedSeq: 99, CreatedAt: clock.Now(), UpdatedAt: clock.Now(),
	})
	sched := &fakeRecoverCandidates{}
	sched.set(route)
	r := newJobRecoverer(jobs, sched, client, clock, discardLogger(), jobRecoverConfig{})

	r.tick()

	j, _ := jobs.get("job_1", "tenant-a")
	if j.State != runtime.WorkflowRunning {
		t.Errorf("job.State = %v, want unchanged (running) — recovery must not clobber a job this replica already tracks", j.State)
	}
}

func TestJobRecovererAsksOnlyCurrentlyConnectedRoutes(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	client := newFakeRecoveryClient()
	sched := &fakeRecoverCandidates{}
	sched.set(
		scheduler.Candidate{NodeID: "node-1", RuntimeID: "comfy-1"},
		scheduler.Candidate{NodeID: "node-2", RuntimeID: "comfy-2"},
	)
	r := newJobRecoverer(jobs, sched, client, clock, discardLogger(), jobRecoverConfig{Concurrency: 1})

	r.tick()

	if got := client.askedCount(); got != 2 {
		t.Errorf("routes asked about = %d, want exactly 2 (one per currently connected candidate)", got)
	}
}

func TestJobRecovererFailureLogsAndMovesOnWithoutPanicking(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	client := newFakeRecoveryClient()
	client.err = errUnreachable
	sched := &fakeRecoverCandidates{}
	sched.set(scheduler.Candidate{NodeID: "node-1", RuntimeID: "comfy-1"})
	r := newJobRecoverer(jobs, sched, client, clock, discardLogger(), jobRecoverConfig{})

	r.tick() // must not panic

	if got := client.askedCount(); got != 1 {
		t.Errorf("routes asked about = %d, want 1", got)
	}
}

func TestJobRecovererStopWaitsForTheInFlightTickAndExitsPromptly(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)

	started := make(chan struct{})
	release := make(chan struct{})
	client := &blockingRecoveryClient{started: started, release: release}
	sched := &fakeRecoverCandidates{}
	sched.set(scheduler.Candidate{NodeID: "node-1", RuntimeID: "comfy-1"})
	r := newJobRecoverer(jobs, sched, client, clock, discardLogger(), jobRecoverConfig{Interval: time.Second})
	go r.run()

	gatewaytest.WaitFor(t, "the recoverer to arm its first timer", func() bool { return clock.PendingTimers() >= 1 })
	clock.Advance(time.Second)
	<-started

	stopped := make(chan struct{})
	go func() {
		r.Stop()
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

type blockingRecoveryClient struct {
	started, release chan struct{}
}

func (b *blockingRecoveryClient) ListActiveJobsForRoute(context.Context, string, string) ([]RecoveredJob, error) {
	close(b.started)
	<-b.release
	return nil, nil
}

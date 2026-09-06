package httpapi

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// fakeAsker answers WorkflowStatus from a scripted, keyed set of results and
// counts concurrent and total calls, so a test can assert on the syncer's
// dispatch pattern without a tunnel.
//
// fakeAsker 从一份按 job id 编排的结果表里应答 WorkflowStatus，并计数并发与总调用
// 次数，好让测试无需隧道就能断言同步器的分派模式。
type fakeAsker struct {
	mu          sync.Mutex
	calls       map[string]int
	inFlight    int
	maxInFlight int
	// result, keyed by runID, lets each test job script its own outcome.
	//
	// result 按 runID 编排，让每个测试 job 都能脚本化自己的结果。
	result func(runID string) (runtime.WorkflowStatus, error)
}

func (f *fakeAsker) WorkflowStatus(_ context.Context, _ scheduler.Candidate, runID string) (runtime.WorkflowStatus, error) {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[runID]++
	f.mu.Unlock()

	// A small sleep gives concurrent goroutines a chance to overlap, which is
	// what maxInFlight is measuring.
	//
	// 一次小睡眠给并发协程一个重叠的机会，这正是 maxInFlight 要度量的东西。
	time.Sleep(time.Millisecond)

	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()
	return f.result(runID)
}

func (f *fakeAsker) callCount(runID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[runID]
}

// addTestJob inserts a non-terminal job directly into the store, bypassing
// the HTTP submit path — these tests exercise the syncer's own scheduling,
// not submission.
//
// addTestJob 直接把一个非终态 job 插入存储，绕开 HTTP 提交路径——这些测试要验证的
// 是同步器自己的调度，而不是提交。
func addTestJob(s *jobStore, id, tenantID, runID string, now time.Time) {
	s.add(job{
		ID:         id,
		WorkflowID: "wf",
		TenantID:   tenantID,
		Candidate:  scheduler.Candidate{NodeID: "node-" + id, RuntimeID: "rt-" + id},
		RunID:      runID,
		State:      runtime.WorkflowPending,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

func TestJobSyncerAdvancesANonTerminalJobWithoutBeingPolled(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())

	asker := &fakeAsker{result: func(string) (runtime.WorkflowStatus, error) {
		return runtime.WorkflowStatus{State: runtime.WorkflowSucceeded}, nil
	}}
	syncer := newJobSyncer(jobs, asker, clock, discardLogger(), jobSyncConfig{Interval: time.Second})
	go syncer.run()
	defer syncer.Stop()

	gatewaytest.WaitFor(t, "the syncer to arm its first timer", func() bool { return clock.PendingTimers() >= 1 })
	clock.Advance(time.Second)
	gatewaytest.WaitFor(t, "the syncer to observe the terminal status", func() bool {
		j, _ := jobs.get("job_1", "tenant-a")
		return j.State == runtime.WorkflowSucceeded
	})

	if got := asker.callCount("run-1"); got == 0 {
		t.Fatal("WorkflowStatus was never called; the syncer did not sync the job on its own")
	}
}

func TestJobSyncerBoundsBatchSizeAndConcurrency(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	const total = 20
	for i := range total {
		addTestJob(jobs, "job_"+string(rune('a'+i)), "tenant-a", "run_"+string(rune('a'+i)), clock.Now())
	}

	asker := &fakeAsker{result: func(string) (runtime.WorkflowStatus, error) {
		return runtime.WorkflowStatus{State: runtime.WorkflowRunning}, nil
	}}
	const batch, concurrency = 5, 2
	syncer := newJobSyncer(jobs, asker, clock, discardLogger(), jobSyncConfig{
		Interval: time.Second, BatchSize: batch, Concurrency: concurrency,
	})

	syncer.tick()

	asker.mu.Lock()
	totalCalls, maxInFlight := 0, asker.maxInFlight
	for _, n := range asker.calls {
		totalCalls += n
	}
	asker.mu.Unlock()

	if totalCalls != batch {
		t.Errorf("calls made in one tick = %d, want exactly %d (the configured batch size)", totalCalls, batch)
	}
	if maxInFlight > concurrency {
		t.Errorf("max concurrent calls = %d, want at most %d", maxInFlight, concurrency)
	}
}

func TestJobSyncerBacksOffAJobWhoseNodeIsGoneRatherThanReSyncingItEveryTick(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())

	nodeGone := &runtime.RuntimeError{Code: runtime.ErrorConnection, Message: "node is not connected to this replica"}
	asker := &fakeAsker{result: func(string) (runtime.WorkflowStatus, error) {
		return runtime.WorkflowStatus{}, nodeGone
	}}
	syncer := newJobSyncer(jobs, asker, clock, discardLogger(), jobSyncConfig{
		Interval: time.Second, MaxBackoff: 8 * time.Second,
	})

	// Failure 1: backoff(1) equals the base interval, so the job is due again
	// after exactly one more interval — the same cadence a healthy job would
	// get, since one failure alone is not yet evidence of a gone node.
	//
	// 第一次失败：backoff(1) 等于基础间隔，因此再过恰好一个间隔这个 job 就会
	// 重新到期——与一个健康 job 相同的节奏，因为仅一次失败还不足以证明节点已经
	// 消失。
	syncer.tick()
	if got := asker.callCount("run-1"); got != 1 {
		t.Fatalf("calls after tick 1 = %d, want 1", got)
	}
	clock.Advance(time.Second)

	// Failure 2: backoff(2) doubles to two intervals, so advancing by only
	// one more must not make it due — this is the doubling this test exists
	// to catch.
	//
	// 第二次失败：backoff(2) 翻倍为两个间隔，因此只再推进一个间隔不应让它到期——
	// 这次翻倍正是本测试要抓的东西。
	syncer.tick()
	if got := asker.callCount("run-1"); got != 2 {
		t.Fatalf("calls after tick 2 = %d, want 2", got)
	}
	clock.Advance(time.Second)
	syncer.tick()
	if got := asker.callCount("run-1"); got != 2 {
		t.Fatalf("calls after +1 interval past failure 2 = %d, want still 2 (backoff should have doubled to two intervals)", got)
	}

	// Advancing past the doubled deadline makes it due again.
	//
	// 推进过翻倍后的截止时间，它会再次到期。
	clock.Advance(time.Second)
	syncer.tick()
	if got := asker.callCount("run-1"); got != 3 {
		t.Fatalf("calls after the doubled backoff window elapsed = %d, want 3", got)
	}

	j, ok := jobs.get("job_1", "tenant-a")
	if !ok {
		t.Fatal("job_1 was not found")
	}
	if j.State != runtime.WorkflowPending {
		t.Errorf("state = %v, want unchanged (still WorkflowPending) — a node being unreachable is not evidence the run changed", j.State)
	}
}

func TestJobSyncerForegroundObservationClearsBackgroundBackoff(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())

	nodeGone := &runtime.RuntimeError{Code: runtime.ErrorConnection}
	asker := &fakeAsker{result: func(string) (runtime.WorkflowStatus, error) {
		return runtime.WorkflowStatus{}, nodeGone
	}}
	syncer := newJobSyncer(jobs, asker, clock, discardLogger(), jobSyncConfig{
		Interval: time.Second, MaxBackoff: time.Minute,
	})
	syncer.tick() // one failure, now backed off well past +1s

	// A foreground poll succeeds (e.g. the node came back and the caller
	// happened to ask directly) and must reset the backoff the failed
	// background attempt accumulated.
	//
	// 一次前台轮询成功了（比如节点恢复了，而调用方恰好直接问了一句），必须重置
	// 失败的后台尝试累积的退避。
	jobs.update("job_1", runtime.WorkflowStatus{State: runtime.WorkflowRunning}, clock.Now())

	clock.Advance(time.Second)
	syncer.tick()
	if got := asker.callCount("run-1"); got != 2 {
		t.Fatalf("calls after the foreground update reset backoff = %d, want 2 (due again after one interval)", got)
	}
}

func TestJobSyncerStopWaitsForTheInFlightTickAndExitsPromptly(t *testing.T) {
	clock := gatewaytest.NewClock()
	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", clock.Now())

	started := make(chan struct{})
	release := make(chan struct{})
	asker := &fakeAsker{result: func(string) (runtime.WorkflowStatus, error) {
		close(started)
		<-release
		return runtime.WorkflowStatus{State: runtime.WorkflowRunning}, nil
	}}
	syncer := newJobSyncer(jobs, asker, clock, discardLogger(), jobSyncConfig{Interval: time.Second})
	go syncer.run()

	gatewaytest.WaitFor(t, "the syncer to arm its first timer", func() bool { return clock.PendingTimers() >= 1 })
	clock.Advance(time.Second)
	<-started

	stopped := make(chan struct{})
	go func() {
		syncer.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop returned before the in-flight sync call finished")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	select {
	case <-stopped:
	case <-time.After(gatewaytest.Timeout):
		t.Fatal("Stop did not return after the in-flight call finished")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

package httpapi

import (
	"testing"
	"time"

	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/common/runtime"
)

// TestJobStoreRecordsATerminalTransitionExactlyOnce asserts jobStore.update
// records MetricWorkflowJobsTotal/MetricWorkflowJobDurationSeconds on the
// first observation of a terminal state, and never again for the same job
// even though real callers (jobSyncer, jobStatus polling, SSE) keep calling
// update after that (STATUS.md's A06).
//
// TestJobStoreRecordsATerminalTransitionExactlyOnce 断言 jobStore.update 在
// 首次观测到终态时记录 MetricWorkflowJobsTotal/
// MetricWorkflowJobDurationSeconds，即便真实调用方（jobSyncer、jobStatus
// 轮询、SSE）之后仍会持续调用 update，也绝不为同一个 job 重复记录
// （STATUS.md 的 A06）。
func TestJobStoreRecordsATerminalTransitionExactlyOnce(t *testing.T) {
	mx := metricstest.New()
	s := newJobStore(0)
	s.metrics = newRecorder(mx)
	now := time.Unix(1700000000, 0)

	addTestJob(s, "job_1", "tenant-a", "run-1", now)

	now = now.Add(5 * time.Second)
	s.update("job_1", runtime.WorkflowStatus{State: runtime.WorkflowRunning}, now)
	if got := mx.Sum(MetricWorkflowJobsTotal, nil); got != 0 {
		t.Fatalf("%s = %v after a non-terminal update, want 0", MetricWorkflowJobsTotal, got)
	}

	now = now.Add(10 * time.Second)
	s.update("job_1", runtime.WorkflowStatus{State: runtime.WorkflowSucceeded}, now)

	succeeded := map[string]string{LabelResult: ResultSucceeded}
	if got := mx.Sum(MetricWorkflowJobsTotal, succeeded); got != 1 {
		t.Errorf("%s{result=succeeded} = %v, want 1", MetricWorkflowJobsTotal, got)
	}
	series := mx.Find(MetricWorkflowJobDurationSeconds, succeeded)
	if series == nil {
		t.Fatal("no duration observed for the terminal transition")
	}
	if got := series.Value(); got != 15 {
		t.Errorf("duration = %vs, want 15s (submission to terminal)", got)
	}

	// A later poll that still reports the same terminal state (the ordinary
	// shape of a status call after a job finished) must not double-count.
	//
	// 之后一次仍报告同一终态的轮询（job 结束后一次状态查询的常见形态）不应
	// 重复计数。
	now = now.Add(time.Second)
	s.update("job_1", runtime.WorkflowStatus{State: runtime.WorkflowSucceeded}, now)
	if got := mx.Sum(MetricWorkflowJobsTotal, succeeded); got != 1 {
		t.Errorf("%s{result=succeeded} = %v after a repeated terminal poll, want it to stay 1", MetricWorkflowJobsTotal, got)
	}
}

// TestJobStoreRecordsOutOfMemoryOnlyForAClassifiedFailure asserts the OOM
// counter fires only when the terminal status actually carries
// OutOfMemory, and that an ordinary failure or a cancellation never trips
// it (STATUS.md's A06).
//
// TestJobStoreRecordsOutOfMemoryOnlyForAClassifiedFailure 断言 OOM 计数器
// 只在终态状态确实携带 OutOfMemory 时才触发，普通失败或取消绝不会触发它
// （STATUS.md 的 A06）。
func TestJobStoreRecordsOutOfMemoryOnlyForAClassifiedFailure(t *testing.T) {
	mx := metricstest.New()
	s := newJobStore(0)
	s.metrics = newRecorder(mx)
	now := time.Unix(1700000000, 0)

	addTestJob(s, "job_oom", "tenant-a", "run-oom", now)
	s.update("job_oom", runtime.WorkflowStatus{State: runtime.WorkflowFailed, OutOfMemory: true}, now.Add(time.Second))

	addTestJob(s, "job_plain", "tenant-a", "run-plain", now)
	s.update("job_plain", runtime.WorkflowStatus{State: runtime.WorkflowFailed, OutOfMemory: false}, now.Add(time.Second))

	addTestJob(s, "job_cancelled", "tenant-a", "run-cancelled", now)
	s.update("job_cancelled", runtime.WorkflowStatus{State: runtime.WorkflowCancelled}, now.Add(time.Second))

	if got := mx.Sum(MetricWorkflowJobOOMTotal, nil); got != 1 {
		t.Errorf("%s = %v, want exactly 1 (only job_oom)", MetricWorkflowJobOOMTotal, got)
	}
	if got := mx.Sum(MetricWorkflowJobsTotal, map[string]string{LabelResult: ResultFailed}); got != 2 {
		t.Errorf("%s{result=failed} = %v, want 2 (job_oom and job_plain)", MetricWorkflowJobsTotal, got)
	}
	if got := mx.Sum(MetricWorkflowJobsTotal, map[string]string{LabelResult: ResultCancelled}); got != 1 {
		t.Errorf("%s{result=cancelled} = %v, want 1", MetricWorkflowJobsTotal, got)
	}
}

// TestJobStoreToleratesANilRecorder asserts update works when no metrics
// sink was wired, which is most tests in this package and any deployment
// without -metrics-addr.
//
// TestJobStoreToleratesANilRecorder 断言在未接入任何指标下沉端时 update 依然
// 正常工作，这是本包多数测试以及任何未配置 -metrics-addr 部署的情形。
func TestJobStoreToleratesANilRecorder(t *testing.T) {
	s := newJobStore(0)
	now := time.Unix(1700000000, 0)
	addTestJob(s, "job_1", "tenant-a", "run-1", now)
	s.update("job_1", runtime.WorkflowStatus{State: runtime.WorkflowSucceeded}, now.Add(time.Second))
}

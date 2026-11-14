package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"AIServeWeave/common/accountaccess"
	"AIServeWeave/common/modelaccess"
	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/common/reqid"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/jobqueue"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// DurabilitySharedQueue means the job is waiting in the cluster-wide Redis
// queue and has not reached a node yet; any replica can answer for it.
//
// DurabilitySharedQueue 表示 job 正在全集群 Redis 队列中等待、尚未到达节点；任一副本都能
// 为它作答。
const DurabilitySharedQueue = "shared_queue"

// queueRetryAfter is the Retry-After of a 429 for a full workflow queue.
const queueRetryAfter = "5"

// queueAccess is the caller's scheduling context, carried inside a queued
// task so the worker that dispatches it — possibly on another replica, long
// after the request ended — applies the same node, account and model access
// the request had. It is captured at enqueue: a policy change while the job
// waits takes effect for the next submission, bounded by the queue's max wait.
//
// queueAccess 是调用方的调度上下文，随排队任务携带，使派发它的 worker——可能在另一个副本
// 上、在请求早已结束之后——套用与该请求相同的节点、账号与模型授权。它在入队时捕获：job
// 等待期间的策略变更对下一次提交生效，受队列最长等待时长约束。
type queueAccess struct {
	TenantID string                `json:"tenant_id,omitempty"`
	Accounts *accountaccess.Policy `json:"accounts,omitempty"`
	Models   *modelaccess.Policy   `json:"models,omitempty"`
	Nodes    *nodeaccess.Policy    `json:"nodes,omitempty"`
	// Authenticated is false for a request admitted without a verifier,
	// which carries no policies at all.
	Authenticated bool `json:"authenticated,omitempty"`
}

func accessFrom(ctx context.Context) queueAccess {
	identity, ok := IdentityFrom(ctx)
	if !ok {
		return queueAccess{}
	}
	return queueAccess{TenantID: identity.TenantID, Accounts: identity.Accounts, Models: identity.Models,
		Nodes: identity.Nodes, Authenticated: true}
}

// withSchedulingAccess attaches identity's policies to ctx for the
// scheduler. The auth middleware and the queue worker both use it, so a
// queued job is scheduled under exactly the rules the live request was.
//
// withSchedulingAccess 把 identity 的策略附加到 ctx 上供调度器使用。鉴权中间件与队列
// worker 都使用它，使排队 job 的调度规则与实时请求完全一致。
func withSchedulingAccess(ctx context.Context, tenantID string, accounts *accountaccess.Policy, models *modelaccess.Policy, nodes *nodeaccess.Policy) context.Context {
	if accounts != nil {
		ctx = scheduler.WithAccountAccess(ctx, *accounts)
	}
	if models != nil {
		ctx = scheduler.WithModelAccess(ctx, *models)
	}
	// Attached for every tenant, restricted or not: whether a tenant-owned
	// node admits this caller depends only on who the caller is.
	// 对每个租户都附上，无论是否受限：一台商户自有节点是否接纳该调用方，只取决于调用方是谁。
	if tenantID != "" || nodes != nil {
		var policy nodeaccess.Policy
		if nodes != nil {
			policy = *nodes
		}
		ctx = scheduler.WithNodeAccess(ctx, tenantID, policy)
	}
	return ctx
}

// classifyQueuedSubmit tells the queue whether a failed submit may be tried
// again. No capable node is retryable here, unlike for a live request: the
// worker's replica may lack a tunnel another replica has, and the job waits
// up to its deadline anyway.
//
// classifyQueuedSubmit 告诉队列一次失败的提交能否再试。与实时请求不同，这里「没有可用
// 节点」是可重试的：worker 所在副本可能缺少另一个副本拥有的隧道，而 job 本来就会等到期限。
func classifyQueuedSubmit(err error) jobqueue.Outcome {
	switch {
	case errors.Is(err, scheduler.ErrNodeNotAllowed):
		return jobqueue.OutcomeDenied
	case errors.Is(err, scheduler.ErrNoCapableNode), scheduler.SubmitRetryable(err):
		return jobqueue.OutcomeRetry
	default:
		return jobqueue.OutcomeUnknown
	}
}

// ClassifyQueuedSubmit is classifyQueuedSubmit for main.go's queue wiring.
func ClassifyQueuedSubmit(err error) jobqueue.Outcome { return classifyQueuedSubmit(err) }

// queueDispatcher is the jobqueue.Dispatcher of this replica.
type queueDispatcher struct{ h *handlers }

func (d queueDispatcher) Submit(ctx context.Context, task jobqueue.Task) (jobqueue.Binding, error) {
	var access queueAccess
	if len(task.Access) > 0 {
		if err := json.Unmarshal(task.Access, &access); err != nil {
			return jobqueue.Binding{}, err
		}
	}
	if task.RequestID != "" {
		ctx = reqid.WithValue(ctx, task.RequestID)
	}
	if access.Authenticated {
		ctx = withSchedulingAccess(ctx, access.TenantID, access.Accounts, access.Models, access.Nodes)
	}
	run, candidate, err := d.h.sched.SubmitWorkflow(ctx, runtime.WorkflowRequest{
		Template: task.Graph, ClientID: task.JobID, IdempotencyKey: task.IdempotencyKey,
	})
	if err != nil {
		return jobqueue.Binding{}, err
	}
	return jobqueue.Binding{NodeID: candidate.NodeID, RuntimeID: candidate.RuntimeID, Kind: string(candidate.Kind), RunID: run.ID}, nil
}

func (d queueDispatcher) Dispatched(task jobqueue.Task, b jobqueue.Binding) {
	h := d.h
	now := h.clock.Now()
	h.jobs.add(job{
		ID: task.JobID, WorkflowID: task.WorkflowID, WorkflowVersion: task.WorkflowVersion, TenantID: task.TenantID,
		Candidate: candidateOf(b), RunID: b.RunID, State: runtime.WorkflowPending, CreatedAt: task.EnqueuedAt, UpdatedAt: now,
	})
	h.persister.nudge()
	h.logger.Info("queued workflow dispatched",
		slog.String("job_id", task.JobID), slog.String("workflow_id", task.WorkflowID),
		slog.String("node_id", b.NodeID), slog.String("runtime_id", b.RuntimeID),
		slog.Duration("waited", now.Sub(task.EnqueuedAt)), slog.String("request_id", task.RequestID))
}

func (d queueDispatcher) CancelDispatched(ctx context.Context, task jobqueue.Task, b jobqueue.Binding) {
	if err := d.h.sched.CancelWorkflow(ctx, candidateOf(b), b.RunID); err != nil {
		d.h.logger.Warn("cancelling a queued workflow after its submit failed",
			slog.String("job_id", task.JobID), slog.String("node_id", b.NodeID), slog.Any("error", err))
	}
}

func candidateOf(b jobqueue.Binding) scheduler.Candidate {
	return scheduler.Candidate{NodeID: b.NodeID, RuntimeID: b.RuntimeID, Kind: runtime.Kind(b.Kind)}
}

// enqueueWorkflow queues one bound submission. It reports handled=false when
// the queue did not take the job and it is certain the job was not queued, so
// the caller dispatches synchronously, exactly as without a queue.
//
// enqueueWorkflow 把一次已绑定的提交加入队列。队列未接收且可以确定 job 未入队时返回
// handled=false，由调用方像没有队列时一样同步派发。
func (h *handlers) enqueueWorkflow(w http.ResponseWriter, r *http.Request, task jobqueue.Task) (position int, handled bool) {
	access, err := json.Marshal(accessFrom(r.Context()))
	if err != nil {
		return 0, false
	}
	now := h.clock.Now()
	task.Access = access
	task.RequestID = requestIDFrom(r.Context())
	task.EnqueuedAt = now
	if task.Deadline.IsZero() {
		task.Deadline = now.Add(h.queue.MaxWait())
	}
	position, err = h.queue.Enqueue(r.Context(), task)
	switch {
	case err == nil:
		return position, true
	case errors.Is(err, jobqueue.ErrQueueFull):
		w.Header().Set("Retry-After", queueRetryAfter)
		writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_error", "queue_full",
			"the workflow queue is full; retry later")
		return 0, true
	case errors.Is(err, jobqueue.ErrIndeterminate):
		writeOpenAIError(w, http.StatusServiceUnavailable, "api_error", "queue_unavailable",
			"the workflow queue could not confirm this submission; check the job before resubmitting")
		return 0, true
	default:
		// ErrUnavailable or ErrTooLarge: certainly not queued.
		h.logger.Warn("workflow queue did not take a submission; dispatching it directly",
			slog.String("job_id", task.JobID), slog.Any("error", err))
		return 0, false
	}
}

// findJob resolves jobID for tenantID: from this replica's store, or — when
// the queue is on — from the shared queue record. A job still waiting returns
// its record and queued=true; a dispatched one is restored into the local
// store first, so the existing handlers continue on it; one the queue failed
// or cancelled comes back as a terminal job that exists only in the queue.
//
// findJob 为 tenantID 解析 jobID：来自本副本的存储，或者——队列开启时——来自共享队列记录。
// 仍在等待的 job 返回其记录且 queued=true；已派发的先恢复到本地存储，让既有处理器接着处理；
// 被队列判为失败或取消的，以只存在于队列中的终态 job 返回。
func (h *handlers) findJob(ctx context.Context, jobID, tenantID string) (j job, rec jobqueue.Record, queued, found bool) {
	if j, ok := h.jobs.get(jobID, tenantID); ok {
		return j, jobqueue.Record{}, false, true
	}
	if h.queue == nil {
		return job{}, jobqueue.Record{}, false, false
	}
	rec, ok, err := h.queue.Lookup(ctx, jobID)
	if err != nil || !ok || rec.TenantID != tenantID {
		// A record of another tenant answers exactly like a missing one.
		return job{}, jobqueue.Record{}, false, false
	}
	if rec.State.Waiting() {
		return job{}, rec, true, true
	}
	return h.jobFromRecord(rec), rec, false, true
}

// jobFromRecord turns a queue record that left the queue into a job. A
// dispatched one is restored into the local store, which the persister and
// syncer then keep current.
func (h *handlers) jobFromRecord(rec jobqueue.Record) job {
	j := job{
		ID: rec.JobID, WorkflowID: rec.WorkflowID, WorkflowVersion: rec.WorkflowVersion, TenantID: rec.TenantID,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}
	switch rec.State {
	case jobqueue.StateDispatched:
		j.Candidate, j.RunID, j.State = candidateOf(rec.Binding), rec.Binding.RunID, runtime.WorkflowPending
		h.jobs.recoverIfMissing(j)
		if local, ok := h.jobs.get(j.ID, j.TenantID); ok {
			return local
		}
	case jobqueue.StateCancelled:
		j.State, j.ErrorCode, j.queueOnly = runtime.WorkflowCancelled, rec.ErrorCode, true
	default:
		j.State, j.ErrorCode, j.queueOnly = runtime.WorkflowFailed, rec.ErrorCode, true
		j.ErrorSummary = queueFailureSummary(rec.ErrorCode)
	}
	return j
}

func queueFailureSummary(code string) string {
	switch code {
	case jobqueue.CodeQueueTimeout:
		return "no workflow node had capacity before the queue wait ran out"
	case jobqueue.CodeSubmitOutcomeUnknown:
		return "the submission may have reached a node but was not confirmed; it was not resubmitted"
	case jobqueue.CodeNodeNotAllowed:
		return "no workflow node is available to this account"
	default:
		return "the job could not be dispatched"
	}
}

// renderQueuedJob renders a job still waiting in the shared queue.
func renderQueuedJob(rec jobqueue.Record, position int) jobJSON {
	return jobJSON{
		JobID: rec.JobID, WorkflowID: rec.WorkflowID, Status: StatusQueued,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, QueuePosition: position,
		Durability: DurabilitySharedQueue,
	}
}

func (h *handlers) writeQueuedJob(ctx context.Context, w http.ResponseWriter, rec jobqueue.Record, status int) {
	position, _ := h.queue.Position(ctx, rec.JobID)
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(renderQueuedJob(rec, position))
}

// awaitQueuedJob waits for a queued job to leave the queue, then resolves it
// like findJob. onPosition, when set, sees each position change.
func (h *handlers) awaitQueuedJob(ctx context.Context, rec jobqueue.Record, onPosition func(int)) (job, error) {
	final, err := h.queue.AwaitDispatch(ctx, rec.JobID, onPosition)
	if err != nil {
		return job{}, err
	}
	return h.jobFromRecord(final), nil
}

// cancelQueued cancels a job that is still in the shared queue. The answer is
// definitive when it was still queued, since no backend ever saw it.
func (h *handlers) cancelQueued(w http.ResponseWriter, r *http.Request, rec jobqueue.Record) {
	result, err := h.queue.Cancel(r.Context(), rec.JobID)
	if err != nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "api_error", "queue_unavailable", "the workflow queue cannot be reached right now")
		return
	}
	switch result {
	case jobqueue.CancelDone:
		rec.State, rec.ErrorCode, rec.UpdatedAt = jobqueue.StateCancelled, jobqueue.CodeCancelled, h.clock.Now()
		j := h.jobFromRecord(rec)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		writeJobBody(w, j)
	case jobqueue.CancelRequested:
		// A worker is submitting it right now; the run is interrupted as
		// soon as the node accepts it.
		h.writeQueuedJob(r.Context(), w, rec, http.StatusAccepted)
	default:
		// It left the queue between the lookup and the cancel: resolve it
		// again and cancel it where it now is.
		if fresh, ok, err := h.queue.Lookup(r.Context(), rec.JobID); err == nil && ok {
			j := h.jobFromRecord(fresh)
			h.cancelResolvedJob(w, r, j)
			return
		}
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "job_not_found", "no such job")
	}
}

// queueWaitDeadline is how long a queued job may wait, capped at budget when
// a synchronous caller (images) has a smaller one.
func (h *handlers) queueWaitDeadline(budget time.Duration) time.Time {
	wait := h.queue.MaxWait()
	if budget > 0 {
		wait = min(wait, budget)
	}
	return h.clock.Now().Add(wait)
}

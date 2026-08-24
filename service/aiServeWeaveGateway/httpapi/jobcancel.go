package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"AIServeWeave/common/runtime"
)

// cancelJob implements POST /v1/jobs/{job_id}/cancel.
//
// Cancellation is a request, not a verdict. ComfyUI's interrupt is
// asynchronous: the run leaves the queue, or the currently executing prompt is
// stopped, and only the backend's own history says which happened. So this
// handler returns 202 with the job as last known, and lets the status endpoint
// or the event stream report the outcome. Marking the job cancelled here would
// be this Gateway inventing a result it has not been told.
//
// cancelJob 实现 POST /v1/jobs/{job_id}/cancel。
//
// 取消是一个请求而不是一个结论。ComfyUI 的中断是异步的：要么该次运行离开队列，要么
// 当前执行中的 prompt 被停下，究竟是哪一种只有后端自己的历史说了算。因此本处理器返回
// 202 与最后已知的 job 视图，把结果留给状态端点或事件流去报告。在这里把 job 标成
// 「已取消」，就是本 Gateway 在编造一个没人告诉过它的结果。
func (h *handlers) cancelJob(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	j, ok := h.jobs.get(r.PathValue("job_id"), identity.TenantID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "job_not_found", "no such job")
		return
	}
	// A finished run is not cancellable, and answering 202 anyway would tell
	// the caller their interrupt is on its way to a backend that will never
	// receive it. 409 says the request conflicts with the job's state, which
	// is exactly what happened.
	//
	// 已结束的运行无法取消，而照样答 202 等于告诉调用方他们的中断正在送往一个永远不会
	// 收到它的后端。409 表示该请求与 job 当前状态冲突，事实正是如此。
	if j.terminal() {
		writeOpenAIError(w, http.StatusConflict, "invalid_request_error", "job_already_finished",
			"this job has already finished and cannot be cancelled")
		return
	}

	if err := h.sched.CancelWorkflow(r.Context(), j.Candidate, j.RunID); err != nil {
		// A backend that cannot interrupt is not a server fault and not the
		// caller's mistake: it is a capability this deployment's node does not
		// have. 501 says so; the generic 500 this would otherwise classify to
		// would send the caller looking for a problem on our side.
		//
		// 无法中断的后端既不是服务端故障，也不是调用方的错误：它是本部署的节点不具备的
		// 一项能力。501 就是这个意思；否则它会被归类成笼统的 500，让调用方跑去我们这边
		// 找问题。
		var rtErr *runtime.RuntimeError
		if errors.As(err, &rtErr) && rtErr.Code == runtime.ErrorCancelUnsupported {
			h.logger.Warn("the node cannot cancel workflows",
				slog.String("job_id", j.ID),
				slog.String("node_id", j.Candidate.NodeID))
			writeOpenAIError(w, http.StatusNotImplemented, "api_error", string(runtime.ErrorCancelUnsupported),
				"the node running this job cannot interrupt a workflow")
			return
		}
		handleDispatchError(w, h.logger, err)
		return
	}

	h.logger.Info("workflow cancel requested",
		slog.String("job_id", j.ID),
		slog.String("node_id", j.Candidate.NodeID),
		slog.String("request_id", requestIDFrom(r.Context())))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJobBody(w, j)
}

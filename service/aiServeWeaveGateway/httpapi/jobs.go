package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// MaxRunRequestBytes bounds a workflow run request body. The body carries
// only the declared inputs — the graph itself comes from the registered
// template, never from the caller — so a megabyte is already generous.
//
// MaxRunRequestBytes 限制工作流运行请求体的大小。请求体只携带已声明的输入——图本身
// 来自已注册的模板，从不来自调用方——因此 1 MB 已经很宽裕。
const MaxRunRequestBytes = 1 << 20

// Public job states, the vocabulary README's 统一任务状态 defines. They are
// deliberately not runtime.WorkflowState's own values: the backend's pending
// is this API's queued, and the mapping is this package's business.
//
// 公开的 job 状态，即 README「统一任务状态」定义的那套词汇。它们刻意不等同于
// runtime.WorkflowState 自己的取值：后端的 pending 就是本 API 的 queued，这层映射
// 是本包的事。
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// runRequest is the body of POST /v1/workflows/{workflow_id}/runs. Inputs is
// only the values the template declared; there is no field through which a
// caller could supply a graph.
//
// runRequest 是 POST /v1/workflows/{workflow_id}/runs 的请求体。inputs 只包含模板
// 声明过的取值；这里没有任何字段能让调用方递进来一张图。
type runRequest struct {
	Inputs         map[string]json.RawMessage `json:"inputs"`
	IdempotencyKey string                     `json:"idempotency_key,omitempty"`
}

// jobJSON is what both endpoints return. It carries this Gateway's own job
// id and never the backend's prompt_id.
//
// jobJSON 是两个端点共同的返回体。它携带本 Gateway 自己的 job id，绝不携带后端的
// prompt_id。
type jobJSON struct {
	JobID         string    `json:"job_id"`
	WorkflowID    string    `json:"workflow_id"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	QueuePosition int       `json:"queue_position,omitempty"`
	// Error is the backend's own failure summary, already bounded and
	// structured by the ComfyUI adapter. It reaches only the tenant that
	// submitted the run, and without it a failed generation is unactionable.
	//
	// Error 是后端自己的失败摘要，长度与结构已由 ComfyUI 适配器处理过。它只到达提交
	// 该次运行的租户；没有它，一次失败的生成就无从下手。
	Error string `json:"error,omitempty"`
}

// submitRun implements POST /v1/workflows/{workflow_id}/runs.
//
// submitRun 实现 POST /v1/workflows/{workflow_id}/runs。
func (h *handlers) submitRun(w http.ResponseWriter, r *http.Request) {
	workflowID := r.PathValue("workflow_id")

	r.Body = http.MaxBytesReader(w, r.Body, MaxRunRequestBytes)
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json",
			"the request body is not valid JSON, or is over the size limit")
		return
	}

	tpl, ok := h.workflows.Lookup(workflowID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "workflow_not_found",
			"the requested workflow is not registered on this deployment")
		return
	}

	graph, err := tpl.Bind(req.Inputs)
	if err != nil {
		var inputErr *workflow.InputError
		if errors.As(err, &inputErr) {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_input",
				"input "+inputErr.Name+" "+inputErr.Reason)
			return
		}
		h.logger.Error("binding a workflow template failed", slog.String("workflow_id", workflowID), slog.Any("error", err))
		writeOpenAIError(w, http.StatusInternalServerError, "api_error", "internal_error", "internal error")
		return
	}

	jobID := "job_" + newRequestID()
	identity, _ := IdentityFrom(r.Context())
	run, candidate, err := h.sched.SubmitWorkflow(r.Context(), runtime.WorkflowRequest{
		Template: graph,
		// The public job id doubles as the backend's client_id, so an event
		// stream opened on the backend is already labelled with the identifier
		// the caller knows this run by.
		//
		// 公开 job id 同时充当后端的 client_id，这样在后端打开的事件流，天然就带着
		// 调用方所知的那个标识符。
		ClientID:       jobID,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		if errors.Is(err, scheduler.ErrNoCapableNode) {
			h.logger.Warn("no node can run workflows", slog.String("workflow_id", workflowID))
			writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "workflow_backend_not_found",
				"no connected node can run workflows right now")
			return
		}
		handleDispatchError(w, h.logger, err)
		return
	}

	now := h.clock.Now()
	j := job{
		ID:         jobID,
		WorkflowID: workflowID,
		TenantID:   identity.TenantID,
		Candidate:  candidate,
		RunID:      run.ID,
		State:      runtime.WorkflowPending,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	h.jobs.add(j)

	// The inputs are not logged: a prompt is exactly the free text README's
	// 安全红线 keeps out of logs.
	//
	// 输入不进日志：提示词正是 README「安全红线」要求不得写入日志的那种自由文本。
	h.logger.Info("workflow submitted",
		slog.String("job_id", jobID),
		slog.String("workflow_id", workflowID),
		slog.String("node_id", candidate.NodeID),
		slog.String("runtime_id", candidate.RuntimeID),
		slog.String("request_id", requestIDFrom(r.Context())))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(renderJob(j))
}

// jobStatus implements GET /v1/jobs/{job_id}. A job that has already finished
// is answered from the store; one still in flight is asked of the node that
// ran it, because README requires the status come from the backend's queue
// and history rather than from events this Gateway may have missed.
//
// jobStatus 实现 GET /v1/jobs/{job_id}。已结束的 job 直接由存储作答；仍在进行中的则
// 去问运行它的那个节点，因为 README 要求状态来自后端的队列与历史，而不是来自本
// Gateway 可能漏掉的事件。
func (h *handlers) jobStatus(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	j, ok := h.jobs.get(r.PathValue("job_id"), identity.TenantID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "job_not_found", "no such job")
		return
	}
	if j.terminal() {
		writeJob(w, j)
		return
	}

	status, err := h.sched.WorkflowStatus(r.Context(), j.Candidate, j.RunID)
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}
	now := h.clock.Now()
	h.jobs.update(j.ID, status, now)
	j.State = status.State
	j.QueuePosition = status.QueuePosition
	j.ErrorSummary = status.ErrorSummary
	j.UpdatedAt = now
	writeJob(w, j)
}

func writeJob(w http.ResponseWriter, j job) {
	w.Header().Set("Content-Type", "application/json")
	writeJobBody(w, j)
}

// writeJobBody encodes the job without touching headers, for a handler that
// has already written its own status code.
//
// writeJobBody 只编码 job 而不碰响应头，供已经自行写过状态码的处理器使用。
func writeJobBody(w http.ResponseWriter, j job) {
	_ = json.NewEncoder(w).Encode(renderJob(j))
}

func renderJob(j job) jobJSON {
	return jobJSON{
		JobID:         j.ID,
		WorkflowID:    j.WorkflowID,
		Status:        publicStatus(j.State),
		CreatedAt:     j.CreatedAt,
		UpdatedAt:     j.UpdatedAt,
		QueuePosition: j.QueuePosition,
		Error:         j.ErrorSummary,
	}
}

// publicStatus maps a backend workflow state onto README's job vocabulary.
//
// publicStatus 把后端的工作流状态映射到 README 的 job 词汇上。
func publicStatus(state runtime.WorkflowState) string {
	switch state {
	case runtime.WorkflowRunning:
		return StatusRunning
	case runtime.WorkflowSucceeded:
		return StatusSucceeded
	case runtime.WorkflowFailed:
		return StatusFailed
	case runtime.WorkflowCancelled:
		return StatusCancelled
	default:
		return StatusQueued
	}
}

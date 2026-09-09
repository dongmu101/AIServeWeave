package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// MaxRunRequestBytes bounds a plain-JSON workflow run request body (no file
// inputs). The body carries only the declared scalar inputs — the graph
// itself comes from the registered template, never from the caller — so a
// megabyte is already generous.
//
// MaxRunRequestBytes 限制纯 JSON（不含文件输入）的工作流运行请求体大小。
// 请求体只携带已声明的标量输入——图本身来自已注册的模板，从不来自调用方——
// 因此 1 MB 已经很宽裕。
const MaxRunRequestBytes = 1 << 20

// MaxWorkflowUploadBytes bounds a multipart run request that carries file
// inputs (STATUS.md's P04) — the whole request, files included, not just
// the "inputs" field MaxRunRequestBytes would otherwise cover. It is far
// larger than MaxRunRequestBytes because it is sized for real images and
// short video clips rather than a JSON scalar map.
//
// MaxWorkflowUploadBytes 限定一次携带文件输入（STATUS.md 的 P04）的
// multipart 运行请求——是整个请求，包含文件在内，而不只是 MaxRunRequestBytes
// 原本覆盖的那个 "inputs" 字段。它比 MaxRunRequestBytes 大得多，因为它是
// 按真实图片与短视频片段来设定的，而不是一个 JSON 标量映射。
const MaxWorkflowUploadBytes = 200 << 20

// maxWorkflowUploadMemory is the in-memory threshold ParseMultipartForm uses
// before spilling a part to a temporary file on disk. It bounds RAM use per
// upload without bounding the upload's own size, which MaxWorkflowUploadBytes
// already does via http.MaxBytesReader.
//
// maxWorkflowUploadMemory 是 ParseMultipartForm 在把一个分片溢写到磁盘临时
// 文件之前所用的内存阈值。它限定的是每次上传占用的内存，而不是上传本身的
// 大小——后者已经由 http.MaxBytesReader 经 MaxWorkflowUploadBytes 限定。
const maxWorkflowUploadMemory = 8 << 20

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

// submitRun implements POST /v1/workflows/{workflow_id}/runs. It accepts two
// request shapes: plain application/json (the original shape, scalar inputs
// only) and multipart/form-data (STATUS.md's P04), whose "inputs" form field
// carries the same JSON body and whose other named parts are the files a
// caller wants substituted into InputFile-typed inputs — see
// parseRunRequest.
//
// submitRun 实现 POST /v1/workflows/{workflow_id}/runs。它接受两种请求形态：
// 纯 application/json（最初的形态，只有标量输入）与 multipart/form-data
// （STATUS.md 的 P04），后者的 "inputs" 表单字段携带与前者相同的 JSON 请求体，
// 其余具名分片则是调用方想要代入 InputFile 类型输入的文件——见
// parseRunRequest。
func (h *handlers) submitRun(w http.ResponseWriter, r *http.Request) {
	workflowID := r.PathValue("workflow_id")

	req, files, err := h.parseRunRequest(w, r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			err.Error())
		return
	}
	if files != nil {
		defer files.close()
	}

	identity, _ := IdentityFrom(r.Context())
	tpl, ok := h.workflows.Lookup(workflowID)
	// A template invisible to this tenant (P03) answers exactly like a
	// missing one: existence is not something to leak to a tenant it was not
	// published for.
	//
	// 一个对本租户不可见的模板（P03）与不存在的模板答复完全相同：存在与否本身
	// 不该向一个未被发布给它的租户泄露。
	if !ok || !tpl.VisibleTo(identity.TenantID) {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "workflow_not_found",
			"the requested workflow is not registered on this deployment")
		return
	}

	var fileHeaders map[string]workflow.FileHeader
	if files != nil {
		fileHeaders = files.headers
	}
	graph, pending, err := tpl.Bind(req.Inputs, fileHeaders)
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
	// The public job id doubles as the backend's client_id, so an event
	// stream opened on the backend is already labelled with the identifier
	// the caller knows this run by.
	//
	// 公开 job id 同时充当后端的 client_id，这样在后端打开的事件流，天然就带着
	// 调用方所知的那个标识符。
	wfReq := runtime.WorkflowRequest{Template: graph, ClientID: jobID, IdempotencyKey: req.IdempotencyKey}

	var run runtime.WorkflowRun
	var candidate scheduler.Candidate
	if len(pending) == 0 {
		run, candidate, err = h.sched.SubmitWorkflow(r.Context(), wfReq)
	} else {
		run, candidate, err = h.submitWithFiles(r.Context(), wfReq, pending, files)
	}
	if err != nil {
		if errors.Is(err, scheduler.ErrNoCapableNode) {
			h.logger.Warn("no node can run workflows", slog.String("workflow_id", workflowID))
			writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "workflow_backend_not_found",
				"no connected node can run workflows right now")
			return
		}
		if errors.Is(err, errUnsupportedUploadFormat) {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_input", err.Error())
			return
		}
		handleDispatchError(w, h.logger, err)
		return
	}

	now := h.clock.Now()
	j := job{
		ID:              jobID,
		WorkflowID:      workflowID,
		WorkflowVersion: tpl.Version,
		TenantID:        identity.TenantID,
		Candidate:       candidate,
		RunID:           run.ID,
		State:           runtime.WorkflowPending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	h.jobs.add(j)
	// The persister is nudged, never waited on: this response is already
	// decided by the submit call above having succeeded, and the control
	// plane hearing about it is a side channel per the persistence
	// contract, not a condition of this 202.
	//
	// 持久化器在这里被提醒，而绝不会被等待：这个响应早已由上面的提交调用
	// 成功决定，控制面得知此事按持久化契约是一条旁路，不是这个 202 的前提
	// 条件。
	h.persister.nudge()

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

// uploadedFiles holds one multipart submit request's file parts, open and
// ready to stream. headers is what workflow.Bind needs to validate and
// account for each one without reading a byte of it; open reads one part's
// actual bytes, on demand, once submitWithFiles has a node to send them to.
//
// uploadedFiles 持有一次 multipart 提交请求的文件分片，已就绪、可供流式读取。
// headers 是 workflow.Bind 校验并记账每一个所需要的东西，不必读它的一个字节；
// open 按需读取某一个分片的真实字节，在 submitWithFiles 有了要发往的节点
// 之后才会被调用。
type uploadedFiles struct {
	form    *multipart.Form
	headers map[string]workflow.FileHeader
}

func (u *uploadedFiles) open(name string) (io.ReadCloser, error) {
	fh := u.form.File[name]
	if len(fh) == 0 {
		return nil, fmt.Errorf("httpapi: no uploaded file part named %q", name)
	}
	return fh[0].Open()
}

// close releases the temporary files ParseMultipartForm may have spilled
// large parts to. It is nil-receiver-safe so submitRun's defer needs no
// separate nil check for the common (no files) case.
//
// close 释放 ParseMultipartForm 为过大的分片溢写出的临时文件。它对 nil
// 接收者安全，这样 submitRun 的 defer 在常见情形（没有文件）下无需另外
// 判空。
func (u *uploadedFiles) close() {
	if u == nil || u.form == nil {
		return
	}
	_ = u.form.RemoveAll()
}

// parseRunRequest decodes submitRun's body in either of its two shapes.
// Plain application/json (or any other/missing content type, so existing
// callers that never set one keep working unchanged) decodes directly into
// runRequest, bounded by MaxRunRequestBytes, and returns no files.
// multipart/form-data reads the "inputs" form field as the same JSON body
// and every other form field as a named file part (STATUS.md's P04),
// bounded by MaxWorkflowUploadBytes for the whole request.
//
// parseRunRequest 用 submitRun 两种形态之一解码请求体。纯 application/json
// （或任何其它/缺失的 content type，这样从未设置过它的既有调用方行为不变）
// 直接解码进 runRequest，受 MaxRunRequestBytes 限定，且不返回任何文件。
// multipart/form-data 把 "inputs" 表单字段读作同样的 JSON 请求体，其余每个
// 表单字段都读作一个具名文件分片（STATUS.md 的 P04），整个请求受
// MaxWorkflowUploadBytes 限定。
func (h *handlers) parseRunRequest(w http.ResponseWriter, r *http.Request) (runRequest, *uploadedFiles, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		r.Body = http.MaxBytesReader(w, r.Body, MaxRunRequestBytes)
		var req runRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return runRequest{}, nil, errors.New("the request body is not valid JSON, or is over the size limit")
		}
		return req, nil, nil
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxWorkflowUploadBytes)
	if err := r.ParseMultipartForm(maxWorkflowUploadMemory); err != nil {
		return runRequest{}, nil, errors.New("the request body is not a valid multipart upload, or is over the size limit")
	}
	files := &uploadedFiles{form: r.MultipartForm, headers: make(map[string]workflow.FileHeader, len(r.MultipartForm.File))}

	var req runRequest
	if vals := r.MultipartForm.Value["inputs"]; len(vals) > 0 {
		if err := json.Unmarshal([]byte(vals[0]), &req); err != nil {
			files.close()
			return runRequest{}, nil, errors.New(`the "inputs" form field is not valid JSON`)
		}
	}
	for name, parts := range r.MultipartForm.File {
		if len(parts) == 0 {
			continue
		}
		if err := validateUploadFilename(parts[0].Filename, h.allowedUploadExtensions); err != nil {
			files.close()
			return runRequest{}, nil, err
		}
		files.headers[name] = workflow.FileHeader{Filename: parts[0].Filename, Size: parts[0].Size}
	}
	return req, files, nil
}

// submitWithFiles finishes a submission workflow.Bind could not: it picks
// one workflow-capable candidate, uploads each pending file input's bytes to
// it via scheduler.UploadInput, completes req.Template with the resulting
// InputRefs via workflow.SetGraphField, and only then submits — all against
// that one candidate, per SubmitWorkflowTo's no-retry contract (STATUS.md's
// P04; see that method's doc comment for why retrying elsewhere is not
// safe here).
//
// submitWithFiles 完成 workflow.Bind 完成不了的那部分：挑选一个具备工作流
// 能力的候选，经 scheduler.UploadInput 把每个待处理文件输入的字节上传给它，
// 用得到的 InputRef 经 workflow.SetGraphField 补完 req.Template，然后才
// 提交——全程只针对这一个候选，遵循 SubmitWorkflowTo「不重试」的约定
// （STATUS.md 的 P04；换个地方重试为何在这里不安全，见该方法的文档注释）。
func (h *handlers) submitWithFiles(ctx context.Context, req runtime.WorkflowRequest, pending []workflow.PendingFile, files *uploadedFiles) (runtime.WorkflowRun, scheduler.Candidate, error) {
	candidates := h.sched.WorkflowCapableCandidates()
	if len(candidates) == 0 {
		return runtime.WorkflowRun{}, scheduler.Candidate{}, scheduler.ErrNoCapableNode
	}
	candidate := candidates[0]

	for _, p := range pending {
		f, err := files.open(p.Name)
		if err != nil {
			return runtime.WorkflowRun{}, candidate, err
		}
		sniffed, sniffErr := validateUploadContent(f, p.Filename)
		if sniffErr != nil {
			_ = f.Close()
			return runtime.WorkflowRun{}, candidate, sniffErr
		}
		result, uploadErr := h.sched.UploadInput(ctx, candidate, runtime.InputUploadMeta{
			Filename: p.Filename,
			Size:     p.Size,
		}, sniffed)
		closeErr := f.Close()
		if uploadErr != nil {
			return runtime.WorkflowRun{}, candidate, uploadErr
		}
		if closeErr != nil {
			return runtime.WorkflowRun{}, candidate, closeErr
		}
		req.Template, err = workflow.SetGraphField(req.Template, p.Node, p.Field, result.InputRef)
		if err != nil {
			return runtime.WorkflowRun{}, candidate, err
		}
	}

	run, err := h.sched.SubmitWorkflowTo(ctx, candidate, req)
	return run, candidate, err
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
	h.persister.nudge()
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

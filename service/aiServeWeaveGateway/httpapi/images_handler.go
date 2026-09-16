package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// imagesGenerations implements POST /v1/images/generations. It binds the
// caller's prompt onto the single admin-configured ComfyUI template
// (-images-workflow-id, validated at startup — see main.go), drives that
// workflow Job to a terminal state inline via SubmitWorkflow, then answers
// with the generated image once it has bytes (or a URL) to return. Unlike
// every other endpoint in this package it is synchronous end to end: OpenAI's
// own images/generations contract has no polling or streaming variant to
// imitate.
//
// Submission goes through the same scheduler.SubmitWorkflow entry point
// submitRun uses, not a bypass: a ComfyUI node is typically Exclusive, and
// this endpoint must correctly contend and queue (STATUS.md's P2 bounded
// queueing) against ordinary /v1/workflows/{id}/runs Jobs on that same node
// rather than jumping ahead of them.
//
// imagesGenerations 实现 POST /v1/images/generations。它把调用方的提示词绑定
// 到唯一一个管理员配置的 ComfyUI 模板上（-images-workflow-id，已在启动期
// 校验——见 main.go），经 SubmitWorkflow 就地把该工作流 Job 推进到终态，
// 一旦拿到字节（或 URL）就以生成的图像作答。与本包其余每个端点不同，它从
// 头到尾都是同步的：OpenAI 自己的 images/generations 契约没有轮询或流式
// 变体可仿。
//
// 提交经由与 submitRun 相同的 scheduler.SubmitWorkflow 入口，而不是绕开它：
// ComfyUI 节点通常是 Exclusive 的，本端点必须与同一节点上普通的
// /v1/workflows/{id}/runs Job 正确竞争、排队（STATUS.md 的 P2 有界排队），
// 而不是插到它们前面。
func (h *handlers) imagesGenerations(w http.ResponseWriter, r *http.Request) {
	if h.imagesWorkflowID == "" {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "endpoint_not_configured",
			"image generation is not configured on this deployment")
		return
	}
	tpl, ok := h.workflows.Lookup(h.imagesWorkflowID)
	if !ok {
		// The configured template existed at startup (main.go validates it)
		// but has since vanished from a hot-swapped control-plane bundle
		// (P03) — a known gap this endpoint's startup check cannot close.
		// This answers like "not configured" rather than panicking.
		//
		// 已配置的模板在启动时存在过（main.go 校验过），但此后从一次热替换的
		// 控制面整包（P03）里消失了——这是本端点启动期检查无法弥合的已知缺口。
		// 这里的答复与「未配置」相同，而不是 panic。
		h.logger.Error("the configured images workflow is no longer registered", slog.String("workflow_id", h.imagesWorkflowID))
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "endpoint_not_configured",
			"image generation is not configured on this deployment")
		return
	}

	var req imagesRequest
	r.Body = http.MaxBytesReader(w, r.Body, MaxRunRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json",
			"the request body is not valid JSON, or is over the size limit")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "prompt is required")
		return
	}
	width, height, hasSize, err := parseImageSize(req.Size)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		return
	}
	if hasSize && !(templateDeclares(tpl, imagesInputWidth) && templateDeclares(tpl, imagesInputHeight)) {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "unsupported_parameter",
			"size is not supported by this gateway's configured template")
		return
	}
	if field := req.unsupported(); field != "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "unsupported_parameter",
			field+" is not supported by this gateway")
		return
	}

	inputs, err := promptInputs(req.Prompt, width, height, hasSize)
	if err != nil {
		h.logger.Error("encoding image generation inputs failed", slog.Any("error", err))
		writeOpenAIError(w, http.StatusInternalServerError, "api_error", "internal_error", "internal error")
		return
	}
	graph, _, err := tpl.Bind(inputs, nil)
	if err != nil {
		var inputErr *workflow.InputError
		if errors.As(err, &inputErr) {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_input",
				"input "+inputErr.Name+" "+inputErr.Reason)
			return
		}
		h.logger.Error("binding the images workflow template failed", slog.String("workflow_id", h.imagesWorkflowID), slog.Any("error", err))
		writeOpenAIError(w, http.StatusInternalServerError, "api_error", "internal_error", "internal error")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.imagesGenerationTimeout)
	defer cancel()

	identity, _ := IdentityFrom(r.Context())
	jobID := "img_" + newRequestID()
	run, candidate, err := h.sched.SubmitWorkflow(ctx, runtime.WorkflowRequest{Template: graph, ClientID: jobID})
	if err != nil {
		if errors.Is(err, scheduler.ErrNoCapableNode) {
			h.logger.Warn("no node can generate images", slog.String("workflow_id", h.imagesWorkflowID))
			writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "workflow_backend_not_found",
				"no connected node can generate images right now")
			return
		}
		handleDispatchError(w, h.logger, err)
		return
	}

	// The job is recorded before the poll loop, exactly like submitRun does
	// before it ever asks about status: STATUS.md's J06 route-binding
	// recovery and a caller's own GET /v1/jobs/{job_id} must be able to find
	// this run even if this handler's own wait times out below.
	//
	// job 在轮询循环之前就被记录，与 submitRun 在第一次询问状态之前的做法完全
	// 一致：STATUS.md 的 J06 路由绑定恢复，以及调用方自己的
	// GET /v1/jobs/{job_id}，都必须能找到这次运行，即便本处理器自己的等待
	// 在下面超时了。
	now := h.clock.Now()
	j := job{
		ID: jobID, WorkflowID: h.imagesWorkflowID, WorkflowVersion: tpl.Version,
		TenantID: identity.TenantID, Candidate: candidate, RunID: run.ID,
		State: runtime.WorkflowPending, CreatedAt: now, UpdatedAt: now,
	}
	h.jobs.add(j)
	h.persister.nudge()
	h.logger.Info("image generation submitted",
		slog.String("job_id", jobID), slog.String("workflow_id", h.imagesWorkflowID),
		slog.String("node_id", candidate.NodeID), slog.String("runtime_id", candidate.RuntimeID),
		slog.String("request_id", requestIDFrom(r.Context())))

	status, err := h.pollWorkflowToTerminal(ctx, candidate, run.ID)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			writeOpenAIError(w, http.StatusGatewayTimeout, "timeout_error", "timeout",
				"image generation did not finish before the configured timeout; the run continues on the node and can still be queried as a job")
			return
		}
		handleDispatchError(w, h.logger, err)
		return
	}
	h.jobs.update(jobID, status, h.clock.Now())
	h.persister.nudge()

	if status.State != runtime.WorkflowSucceeded {
		code := "generation_failed"
		if status.State == runtime.WorkflowCancelled {
			code = "cancelled"
		} else if status.OutOfMemory {
			code = "out_of_memory"
		}
		h.logger.Error("image generation did not succeed",
			slog.String("job_id", jobID), slog.String("workflow_id", h.imagesWorkflowID), slog.String("code", code))
		writeOpenAIError(w, http.StatusInternalServerError, "api_error", code, "image generation did not succeed")
		return
	}

	refs, err := h.sched.WorkflowArtifacts(r.Context(), candidate, run.ID)
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}
	images := selectImageArtifacts(refs)
	if len(images) == 0 {
		h.logger.Error("the images workflow produced no image artifacts",
			slog.String("job_id", jobID), slog.String("workflow_id", h.imagesWorkflowID))
		writeOpenAIError(w, http.StatusInternalServerError, "api_error", "no_image_produced",
			"the configured workflow did not produce an image")
		return
	}

	responseFormat := req.ResponseFormat
	if responseFormat == "" {
		responseFormat = "b64_json"
	}
	ids := h.jobs.recordArtifacts(jobID, images)
	data := make([]imagesDataJSON, 0, len(images))
	for i, ref := range images {
		item, err := h.renderImageData(r.Context(), candidate, ref, ids[i], responseFormat)
		if err != nil {
			handleDispatchError(w, h.logger, err)
			return
		}
		data = append(data, item)
	}
	if responseFormat == "url" {
		// url mode answers with GET /v1/artifacts/{artifact_id}, so the
		// bytes need to survive after this candidate's node disconnects —
		// nudge the same background persister submitRun already relies on
		// for that (STATUS.md's P04); downloadArtifact's live-pull fallback
		// covers the window before it catches up.
		//
		// url 模式用 GET /v1/artifacts/{artifact_id} 作答，因此这些字节需要
		// 在这一候选的节点断开之后依然可用——提醒与 submitRun 已经依赖的
		// 同一个后台持久化器去做这件事（STATUS.md 的 P04）；在它赶上之前的
		// 窗口，由 downloadArtifact 的实时拉取回退覆盖。
		h.persister.nudge()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(imagesResponse{Created: h.clock.Now().Unix(), Data: data})
}

// pollWorkflowToTerminal re-asks WorkflowStatus at imagesPollInterval until
// the run reaches a terminal state or ctx is done. It polls WorkflowStatus
// rather than subscribing to events: the ComfyUI adapter's own status query
// reads the backend's queue and history, the authoritative source, whereas a
// WebSocket subscription is documented as best-effort and can drop events on
// reconnect — the same reason jobStatus and the background jobSyncer already
// poll rather than trust events for state transitions.
//
// pollWorkflowToTerminal 以 imagesPollInterval 为间隔反复询问 WorkflowStatus，
// 直到该次运行到达终态或 ctx 结束。它轮询 WorkflowStatus 而不是订阅事件：
// ComfyUI 适配器自己的状态查询读的是后端的队列与历史，是权威源，而一次
// WebSocket 订阅按文档说明是尽力而为、重连时可能丢事件——jobStatus 与后台
// jobSyncer 已经因为同一个理由选择轮询而不是信任事件来判断状态转换。
func (h *handlers) pollWorkflowToTerminal(ctx context.Context, c scheduler.Candidate, runID string) (runtime.WorkflowStatus, error) {
	for {
		status, err := h.sched.WorkflowStatus(ctx, c, runID)
		if err != nil {
			return runtime.WorkflowStatus{}, err
		}
		switch status.State {
		case runtime.WorkflowSucceeded, runtime.WorkflowFailed, runtime.WorkflowCancelled:
			return status, nil
		}
		timer, stop := h.clock.NewTimer(imagesPollInterval)
		select {
		case <-ctx.Done():
			stop()
			return runtime.WorkflowStatus{}, ctx.Err()
		case <-timer:
		}
	}
}

// renderImageData renders one selected artifact as the OpenAI images shape.
// url mode never reads the artifact's bytes: it answers with this Gateway's
// own existing GET /v1/artifacts/{artifact_id} download path, which already
// knows how to stream them (preferring a persisted copy, falling back to a
// live node pull). b64_json mode reads the bytes here, bounded by
// MaxImageResponseBytes.
//
// renderImageData 把一个已选中的产物渲染成 OpenAI 的图像形状。url 模式从不
// 读取产物字节：它用本 Gateway 自己既有的 GET /v1/artifacts/{artifact_id}
// 下载路径作答，那条路径本就知道如何流式传输它们（优先读持久副本，回退到
// 实时节点拉取）。b64_json 模式在这里读取字节，受 MaxImageResponseBytes
// 限定。
func (h *handlers) renderImageData(ctx context.Context, c scheduler.Candidate, ref runtime.ArtifactRef, artifactID, responseFormat string) (imagesDataJSON, error) {
	if responseFormat == "url" {
		return imagesDataJSON{URL: "/v1/artifacts/" + artifactID}, nil
	}

	artifact, err := h.sched.OpenArtifact(ctx, c, ref)
	if err != nil {
		return imagesDataJSON{}, err
	}
	defer artifact.Body.Close()

	limited := io.LimitReader(artifact.Body, MaxImageResponseBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return imagesDataJSON{}, err
	}
	if len(buf) > MaxImageResponseBytes {
		return imagesDataJSON{}, &runtime.RuntimeError{
			Code:      runtime.ErrorResponseTooLarge,
			RuntimeID: c.RuntimeID,
			Operation: "images_generations",
			Message:   "generated image exceeds the response size limit",
		}
	}
	return imagesDataJSON{B64JSON: base64.StdEncoding.EncodeToString(buf)}, nil
}

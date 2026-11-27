package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"AIServeWeave/common/flowdef"
)

// Studio flow request bounds: a definition of up to flowdef.MaxDefinitionBytes
// with room for the name, and a run's input values.
//
// Studio 编排请求的上限：一份至多 flowdef.MaxDefinitionBytes 的定义加上名称的余量，以及
// 一次运行的输入值。
const (
	maxStudioFlowBodyBytes = flowdef.MaxDefinitionBytes + 64<<10
	maxStudioRunBodyBytes  = 2 << 20
	studioKeepAlive        = 15 * time.Second
	studioFlowIDPrefix     = "flw_"
)

// mountStudioFlows registers the flow and run endpoints.
//
// mountStudioFlows 注册编排与运行端点。
func (h *handlers) mountStudioFlows(mux *http.ServeMux, st StudioFlowStore, runner *studioRunner, logger *slog.Logger) {
	s := &studioFlows{h: h, store: st, runner: runner, logger: logger}
	mux.HandleFunc("GET /studio/v1/flows", s.list)
	mux.HandleFunc("POST /studio/v1/flows", s.create)
	mux.HandleFunc("GET /studio/v1/flows/{id}", s.get)
	mux.HandleFunc("PATCH /studio/v1/flows/{id}", s.update)
	mux.HandleFunc("DELETE /studio/v1/flows/{id}", s.remove)
	mux.HandleFunc("GET /studio/v1/flows/{id}/versions/{version}", s.version)
	mux.HandleFunc("GET /studio/v1/flows/{id}/runs", s.runs)
	mux.HandleFunc("POST /studio/v1/flows/{id}/runs", s.startRun)
	mux.HandleFunc("GET /studio/v1/runs/{id}", s.getRun)
	mux.HandleFunc("GET /studio/v1/runs/{id}/events", s.events)
	mux.HandleFunc("POST /studio/v1/runs/{id}/cancel", s.cancelRun)
	mux.HandleFunc("GET /studio/v1/runs/{id}/artifacts/{artifact_id}", s.artifact)
	mux.HandleFunc("GET /studio/v1/image-models", s.imageModels)
	mux.HandleFunc("GET /studio/v1/workflow-templates", s.workflowTemplates)
}

type studioFlows struct {
	h      *handlers
	store  StudioFlowStore
	runner *studioRunner
	logger *slog.Logger
}

type studioFlowJSON struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Version    int64           `json:"version"`
	Definition json.RawMessage `json:"definition,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

func renderFlow(f StudioFlow) studioFlowJSON {
	return studioFlowJSON{ID: f.ID, Name: f.Name, Version: f.Version, Definition: f.Definition, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt}
}

type studioRunStepJSON struct {
	NodeID     string            `json:"node_id"`
	NodeType   string            `json:"node_type"`
	Status     string            `json:"status"`
	ErrorCode  string            `json:"error_code,omitempty"`
	Model      string            `json:"model,omitempty"`
	Inputs     map[string]string `json:"inputs,omitempty"`
	Outputs    map[string]string `json:"outputs,omitempty"`
	Usage      *studioUsageJSON  `json:"usage,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at"`
}

func renderRunStep(s StudioRunStep) studioRunStepJSON {
	out := studioRunStepJSON{
		NodeID: s.NodeID, NodeType: s.NodeType, Status: s.Status, ErrorCode: s.ErrorCode, Model: s.Model,
		Inputs: s.Inputs, Outputs: s.Outputs, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt,
	}
	if s.PromptTokens > 0 || s.CompletionTokens > 0 {
		out.Usage = &studioUsageJSON{PromptTokens: s.PromptTokens, CompletionTokens: s.CompletionTokens}
	}
	return out
}

type studioRunJSON struct {
	ID          string              `json:"id"`
	FlowID      string              `json:"flow_id"`
	FlowVersion int64               `json:"flow_version"`
	Status      string              `json:"status"`
	ErrorCode   string              `json:"error_code,omitempty"`
	FailedNode  string              `json:"failed_node,omitempty"`
	Inputs      json.RawMessage     `json:"inputs,omitempty"`
	Outputs     map[string]string   `json:"outputs,omitempty"`
	Usage       studioUsageJSON     `json:"usage"`
	CreatedAt   time.Time           `json:"created_at"`
	FinishedAt  *time.Time          `json:"finished_at,omitempty"`
	Steps       []studioRunStepJSON `json:"steps,omitempty"`
}

func renderRun(r StudioRun) studioRunJSON {
	out := studioRunJSON{
		ID: r.ID, FlowID: r.FlowID, FlowVersion: r.FlowVersion, Status: r.Status, ErrorCode: r.ErrorCode, FailedNode: r.FailedNode,
		Inputs: r.Inputs, Outputs: r.Outputs, Usage: studioUsageJSON{PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens},
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}
	for _, s := range r.Steps {
		out.Steps = append(out.Steps, renderRunStep(s))
	}
	return out
}

// studioRunningJSON is a node whose model call is in flight, with the text it
// has produced so far.
//
// studioRunningJSON 是模型调用仍在进行中的节点，以及它迄今产出的文本。
type studioRunningJSON struct {
	NodeID string `json:"node_id"`
	Text   string `json:"text"`
}

type studioRunFinishedJSON struct {
	Status     string            `json:"status"`
	ErrorCode  string            `json:"error_code,omitempty"`
	FailedNode string            `json:"failed_node,omitempty"`
	Outputs    map[string]string `json:"outputs,omitempty"`
	Usage      studioUsageJSON   `json:"usage"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
}

func renderRunFinished(r StudioRun) studioRunFinishedJSON {
	return studioRunFinishedJSON{
		Status: r.Status, ErrorCode: r.ErrorCode, FailedNode: r.FailedNode, Outputs: r.Outputs,
		Usage: studioUsageJSON{PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens}, FinishedAt: r.FinishedAt,
	}
}

type studioIssuesJSON struct {
	Error     studioErrorBody `json:"error"`
	Issues    []flowdef.Issue `json:"issues"`
	Truncated bool            `json:"truncated,omitempty"`
}

// writeStudioIssues answers 400 with the problems found, each located on its
// node, for the editor or the run form to show.
//
// writeStudioIssues 以 400 返回发现的问题，每条都定位到所属节点，供编辑器或运行表单展示。
func writeStudioIssues(w http.ResponseWriter, code, message string, issues []flowdef.Issue, truncated bool) {
	writeStudioJSON(w, http.StatusBadRequest, studioIssuesJSON{Error: studioErrorBody{Code: code, Message: message}, Issues: issues, Truncated: truncated})
}

// storeError answers a StudioFlowStore or runner failure.
// storeError 回答 StudioFlowStore 或执行器的失败。
func (s *studioFlows) storeError(w http.ResponseWriter, err error) {
	var invalid *flowdef.ValidationError
	switch {
	case errors.As(err, &invalid):
		writeStudioIssues(w, "invalid_flow", "the flow is not valid", invalid.Issues, invalid.Truncated)
	case errors.Is(err, ErrStudioNotFound):
		writeStudioError(w, http.StatusNotFound, "not_found", "no such flow or run")
	case errors.Is(err, ErrStudioLimit):
		writeStudioError(w, http.StatusConflict, "limit_reached", "the flow or version limit is reached")
	case errors.Is(err, ErrStudioFlowChanged):
		writeStudioError(w, http.StatusConflict, "flow_changed", "the flow was saved elsewhere; reload it")
	case errors.Is(err, ErrStudioRunLimit):
		writeStudioError(w, http.StatusTooManyRequests, "run_limit", "too many runs are in progress; wait for one to finish")
	case errors.Is(err, ErrStudioInvalid):
		writeStudioError(w, http.StatusBadRequest, "invalid_request", "the request is not valid")
	case errors.Is(err, errStudioShutdown):
		writeStudioError(w, http.StatusServiceUnavailable, "unavailable", "this server is shutting down; retry")
	default:
		s.logger.Error("studio flow storage is unavailable", slog.Any("error", err))
		writeStudioError(w, http.StatusServiceUnavailable, "storage_unavailable", "flows cannot be reached right now")
	}
}

func pathID(w http.ResponseWriter, r *http.Request, prefix, what string) (string, bool) {
	id := r.PathValue("id")
	if !validStudioID(id, prefix) {
		writeStudioError(w, http.StatusNotFound, "not_found", "no such "+what)
		return "", false
	}
	return id, true
}

// pageParams reads cursor and limit, refusing values no list produces.
// pageParams 读取 cursor 与 limit，拒绝任何列表都不会产生的取值。
func pageParams(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	q := r.URL.Query()
	cursor := q.Get("cursor")
	limit, _ := strconv.Atoi(q.Get("limit"))
	if len(cursor) > maxStudioCursorBytes || limit < 0 {
		writeStudioError(w, http.StatusBadRequest, "invalid_request", "the cursor or limit is not valid")
		return "", 0, false
	}
	return cursor, limit, true
}

// list implements GET /studio/v1/flows?cursor=&limit=.
func (s *studioFlows) list(w http.ResponseWriter, r *http.Request) {
	cursor, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	page, err := s.store.ListFlows(r.Context(), studioOwnerOf(r), cursor, limit)
	if err != nil {
		s.storeError(w, err)
		return
	}
	out := struct {
		Data []studioFlowJSON `json:"data"`
		Next string           `json:"next,omitempty"`
	}{Data: make([]studioFlowJSON, len(page.Flows)), Next: page.Next}
	for i, f := range page.Flows {
		out.Data[i] = renderFlow(f)
	}
	writeStudioJSON(w, http.StatusOK, out)
}

// create implements POST /studio/v1/flows.
func (s *studioFlows) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string          `json:"name"`
		Definition json.RawMessage `json:"definition"`
	}
	if !decodeStudioBodyLimit(w, r, &req, maxStudioFlowBodyBytes) {
		return
	}
	f, err := s.store.CreateFlow(r.Context(), studioOwnerOf(r), req.Name, req.Definition)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeStudioJSON(w, http.StatusCreated, renderFlow(f))
}

// get implements GET /studio/v1/flows/{id}.
func (s *studioFlows) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, studioFlowIDPrefix, "flow")
	if !ok {
		return
	}
	f, err := s.store.GetFlow(r.Context(), studioOwnerOf(r), id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeStudioJSON(w, http.StatusOK, renderFlow(f))
}

// update implements PATCH /studio/v1/flows/{id}: a rename and/or a save.
func (s *studioFlows) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, studioFlowIDPrefix, "flow")
	if !ok {
		return
	}
	var req struct {
		Name        *string         `json:"name"`
		Definition  json.RawMessage `json:"definition"`
		BaseVersion int64           `json:"base_version"`
	}
	if !decodeStudioBodyLimit(w, r, &req, maxStudioFlowBodyBytes) {
		return
	}
	f, err := s.store.UpdateFlow(r.Context(), studioOwnerOf(r), id, StudioFlowPatch{Name: req.Name, Definition: req.Definition, BaseVersion: req.BaseVersion})
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeStudioJSON(w, http.StatusOK, renderFlow(f))
}

// remove implements DELETE /studio/v1/flows/{id}. A run of the flow still in
// progress here stops at its next write, which finds nothing.
func (s *studioFlows) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, studioFlowIDPrefix, "flow")
	if !ok {
		return
	}
	if err := s.store.DeleteFlow(r.Context(), studioOwnerOf(r), id); err != nil {
		s.storeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// version implements GET /studio/v1/flows/{id}/versions/{version}.
func (s *studioFlows) version(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, studioFlowIDPrefix, "flow")
	if !ok {
		return
	}
	n, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || n < 1 {
		writeStudioError(w, http.StatusNotFound, "not_found", "no such version")
		return
	}
	v, err := s.store.GetFlowVersion(r.Context(), studioOwnerOf(r), id, n)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeStudioJSON(w, http.StatusOK, struct {
		Version    int64           `json:"version"`
		Definition json.RawMessage `json:"definition"`
		CreatedAt  time.Time       `json:"created_at"`
	}{v.Version, v.Definition, v.CreatedAt})
}

// runs implements GET /studio/v1/flows/{id}/runs?cursor=&limit=.
func (s *studioFlows) runs(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, studioFlowIDPrefix, "flow")
	if !ok {
		return
	}
	cursor, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	page, err := s.store.ListRuns(r.Context(), studioOwnerOf(r), id, cursor, limit)
	if err != nil {
		s.storeError(w, err)
		return
	}
	out := struct {
		Data []studioRunJSON `json:"data"`
		Next string          `json:"next,omitempty"`
	}{Data: make([]studioRunJSON, len(page.Runs)), Next: page.Next}
	for i, run := range page.Runs {
		out.Data[i] = renderRun(run)
	}
	writeStudioJSON(w, http.StatusOK, out)
}

// startRun implements POST /studio/v1/flows/{id}/runs: run the latest version,
// or the one named, with the input values given. Bad input values and
// models that are not available are refused before anything is stored; the
// run then executes on this replica and the answer is 202 with the run.
//
// startRun 实现 POST /studio/v1/flows/{id}/runs：以给定的输入值运行最新版本或指定版本。
// 输入值有误或模型不可用时，在存储任何东西之前就被拒绝；之后运行在本副本上执行，应答为
// 202 并附上运行。
func (s *studioFlows) startRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, studioFlowIDPrefix, "flow")
	if !ok {
		return
	}
	var req struct {
		Inputs  map[string]json.RawMessage `json:"inputs"`
		Version int64                      `json:"version"`
	}
	if !decodeStudioBodyLimit(w, r, &req, maxStudioRunBodyBytes) {
		return
	}
	owner := studioOwnerOf(r)
	var definition json.RawMessage
	version := req.Version
	if version > 0 {
		v, err := s.store.GetFlowVersion(r.Context(), owner, id, version)
		if err != nil {
			s.storeError(w, err)
			return
		}
		definition = v.Definition
	} else {
		f, err := s.store.GetFlow(r.Context(), owner, id)
		if err != nil {
			s.storeError(w, err)
			return
		}
		definition, version = f.Definition, f.Version
	}
	def, err := flowdef.Decode(definition)
	if err == nil {
		err = flowdef.Validate(def)
	}
	if err != nil {
		s.storeError(w, err)
		return
	}
	if issues := s.unavailableModels(r.Context(), def); len(issues) > 0 {
		writeStudioIssues(w, "model_unavailable", "a model of this flow is not available", issues, false)
		return
	}
	if req.Inputs == nil {
		req.Inputs = map[string]json.RawMessage{}
	}
	inputs, _ := json.Marshal(req.Inputs)
	run, err := s.runner.start(r.Context(), owner, id, version, inputs, def, req.Inputs)
	if err != nil {
		var invalid *flowdef.ValidationError
		if errors.As(err, &invalid) {
			writeStudioIssues(w, "invalid_inputs", "the input values are not valid", invalid.Issues, invalid.Truncated)
			return
		}
		s.storeError(w, err)
		return
	}
	writeStudioJSON(w, http.StatusAccepted, renderRun(run))
}

// unavailableModels lists the llm nodes whose model this user may not call
// now, as issues on those nodes.
//
// unavailableModels 以节点上的问题列出模型当前不可供该用户调用的 llm 节点。
func (s *studioFlows) unavailableModels(ctx context.Context, def flowdef.Definition) []flowdef.Issue {
	offered := map[string]bool{}
	for _, m := range s.h.sched.Models(ctx) {
		offered[m.ID] = true
	}
	identity, _ := IdentityFrom(ctx)
	var issues []flowdef.Issue
	for _, n := range def.Nodes {
		switch n.Type {
		case flowdef.NodeLLM:
			if c, err := n.LLM(); err == nil && !offered[c.Model] {
				issues = append(issues, flowdef.Issue{Node: n.ID, Field: "config.model", Code: "model_unavailable", Message: "the model is not available"})
			}
		case flowdef.NodeImage:
			c, err := n.Image()
			switch {
			case err != nil:
			case c.Model != "" && len(s.h.sched.ImageCandidates(ctx, c.Model)) == 0:
				issues = append(issues, flowdef.Issue{Node: n.ID, Field: "config.model", Code: codeModelUnavailable, Message: "the image model is not available"})
			case c.Model == "" && !s.h.defaultImagesAvailable():
				issues = append(issues, flowdef.Issue{Node: n.ID, Field: "config.model", Code: codeImagesUnavailable, Message: "this deployment has no default image generation"})
			}
		case flowdef.NodeComfyUI:
			if c, err := n.ComfyUI(); err == nil && !s.h.templateVisible(c.Template, identity.TenantID) {
				issues = append(issues, flowdef.Issue{Node: n.ID, Field: "config.template", Code: codeTemplateUnavailable, Message: "the workflow template is not available"})
			}
		}
	}
	return issues
}

// getRun implements GET /studio/v1/runs/{id}. A run executing here is read
// from memory, so its owner sees the progress before it is stored.
//
// getRun 实现 GET /studio/v1/runs/{id}。在本副本执行的运行从内存读取，使其所有者在进度
// 存储之前就能看到。
func (s *studioFlows) getRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, studioRunIDPrefix, "run")
	if !ok {
		return
	}
	owner := studioOwnerOf(r)
	if active, ok := s.runner.lookup(owner, id); ok {
		run, _ := active.view()
		writeStudioJSON(w, http.StatusOK, renderRun(run))
		return
	}
	run, err := s.store.GetRun(r.Context(), owner, id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeStudioJSON(w, http.StatusOK, renderRun(run))
}

// events implements GET /studio/v1/runs/{id}/events. The stream opens with a
// run_snapshot frame — the run as it stands, its steps, and the text so far
// of the nodes still generating — then carries node_started, node_delta,
// node_finished, node_skipped and finally run_finished. A run that has ended
// gets its snapshot and run_finished at once. A run executing on another
// replica gets only its snapshot; the client polls GET /studio/v1/runs/{id}.
// A watcher that falls too far behind is cut off and reconnects to a fresh
// snapshot.
//
// events 实现 GET /studio/v1/runs/{id}/events。流以 run_snapshot 帧开头——运行的当前
// 状态、其步骤，以及仍在生成的节点迄今的文本——之后是 node_started、node_delta、
// node_finished、node_skipped，最后是 run_finished。已结束的运行立即得到快照与
// run_finished。在其他副本上执行的运行只得到快照，客户端改为轮询
// GET /studio/v1/runs/{id}。落后太多的观察者被断开，重连后从新的快照开始。
func (s *studioFlows) events(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, studioRunIDPrefix, "run")
	if !ok {
		return
	}
	owner := studioOwnerOf(r)
	type snapshot struct {
		Run     studioRunJSON       `json:"run"`
		Running []studioRunningJSON `json:"running"`
	}
	active, local := s.runner.lookup(owner, id)
	if !local {
		run, err := s.store.GetRun(r.Context(), owner, id)
		if err != nil {
			s.storeError(w, err)
			return
		}
		out := openStudioStream(w)
		out.emit("run_snapshot", snapshot{Run: renderRun(run), Running: []studioRunningJSON{}})
		if run.Status != "running" {
			out.emit("run_finished", renderRunFinished(run))
		}
		return
	}

	run, running, ch := active.subscribe()
	out := openStudioStream(w)
	out.emit("run_snapshot", snapshot{Run: renderRun(run), Running: running})
	if ch == nil {
		out.emit("run_finished", renderRunFinished(run))
		return
	}
	defer active.unsubscribe(ch)
	keepAlive := time.NewTicker(studioKeepAlive)
	defer keepAlive.Stop()
	for !out.gone {
		select {
		case frame, open := <-ch:
			if !open {
				return
			}
			out.emit(frame.event, frame.payload)
		case <-keepAlive.C:
			out.comment("keep-alive")
		case <-r.Context().Done():
			return
		}
	}
}

// openStudioStream starts an SSE response.
// openStudioStream 开始一个 SSE 响应。
func openStudioStream(w http.ResponseWriter) *studioFrames {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &studioFrames{w: w}
}

// cancelRun implements POST /studio/v1/runs/{id}/cancel. Only the replica
// executing a run can stop it; elsewhere the answer says why not.
//
// cancelRun 实现 POST /studio/v1/runs/{id}/cancel。只有执行该运行的副本能停止它；在其他
// 副本上，应答说明无法停止的原因。
func (s *studioFlows) cancelRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, studioRunIDPrefix, "run")
	if !ok {
		return
	}
	owner := studioOwnerOf(r)
	if active, ok := s.runner.lookup(owner, id); ok {
		active.cancel(errStudioRunCanceled)
		run, _ := active.view()
		writeStudioJSON(w, http.StatusAccepted, renderRun(run))
		return
	}
	run, err := s.store.GetRun(r.Context(), owner, id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if run.Status == "running" {
		writeStudioError(w, http.StatusConflict, "run_elsewhere", "the run executes on another server; retry shortly")
		return
	}
	writeStudioError(w, http.StatusConflict, "run_finished", "the run has already ended")
}

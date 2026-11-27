package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"AIServeWeave/common/flowdef"
	"AIServeWeave/common/idgen"
	"AIServeWeave/common/reqid"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/flow"
	"AIServeWeave/service/aiServeWeaveGateway/jobqueue"
	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// Studio flow nodes that produce artifacts: image_generation and
// comfyui_template.
//
// Both reach the same code the API-key front door runs, in process, so
// scheduling, the shared workflow queue, job records, artifact persistence and
// rate limits all apply unchanged. A ComfyUI image and a workflow template
// run produce ordinary job artifacts, downloadable by the tenant that owns
// them. An inference image model answers with the image's bytes and no
// artifact; those bytes are written to object storage under a key derived
// from the run, so any replica can serve them.
//
// 产出产物的 Studio 编排节点：image_generation 与 comfyui_template。
//
// 两者都在进程内走 API Key 前门所走的同一段代码，因此调度、共享工作流队列、job 记录、
// 产物持久化与限流都原样生效。ComfyUI 生图与工作流模板运行产出普通的 job 产物，由拥有
// 它们的租户下载。推理生图模型以图片字节作答、不产生产物；这些字节按由运行推导出的 key
// 写入对象存储，任一副本都能提供下载。

// Studio artifact handling. / Studio 产物处理参数。
const (
	// studioArtifactPrefix marks an artifact id minted here for an inference
	// image kept in object storage, as opposed to a job artifact id.
	// studioArtifactPrefix 标记在这里为存放在对象存储中的推理图片铸造的产物 id，以区别于
	// job 产物 id。
	studioArtifactPrefix = "sart_"
	// studioJobPollInterval is how often a comfyui_template node asks after
	// its job; studioJobPollFailures how many failed asks in a row fail it.
	// studioJobPollInterval 是 comfyui_template 节点询问其 job 的间隔；
	// studioJobPollFailures 是连续多少次询问失败后令节点失败。
	studioJobPollInterval = time.Second
	studioJobPollFailures = 5
	// studioJobCancelTimeout bounds withdrawing a job whose node was stopped.
	// studioJobCancelTimeout 限制撤回一个节点已被停止的 job 所用的时间。
	studioJobCancelTimeout = 10 * time.Second
	// maxInprocResponse bounds the JSON an in-process call may answer with;
	// every handler called this way answers with a small document.
	// maxInprocResponse 限制进程内调用可应答的 JSON 大小；以这种方式调用的每个
	// handler 都只回一个小文档。
	maxInprocResponse = 1 << 20
	// maxStudioArtifactIDBytes bounds an artifact id in a download path.
	// maxStudioArtifactIDBytes 限制下载路径中的产物 id 长度。
	maxStudioArtifactIDBytes = 128
)

// Failure codes of the artifact nodes, on top of the engine's own and those
// the chat dispatcher shares. Studio maps each to its own wording.
//
// 产物节点的失败码，在引擎自己的与对话派发器共用的之外。Studio 把每个码映射为自己的
// 文案。
const (
	codeModelUnavailable      = "model_unavailable"
	codeImagesUnavailable     = "image_generation_unavailable"
	codeTemplateUnavailable   = "template_unavailable"
	codeInvalidInput          = "invalid_input"
	codeNoCapableNode         = "no_capable_node"
	codeQueueTimeout          = "queue_timeout"
	codeGenerationFailed      = "generation_failed"
	codeOutOfMemory           = "out_of_memory"
	codeJobCancelled          = "job_cancelled"
	codeArtifactStorage       = "artifact_storage_unavailable"
	codeRateLimited           = "rate_limited"
	codeStudioArtifactMissing = "not_found"
)

var videoArtifactExtensions = map[string]bool{".mp4": true, ".webm": true, ".mov": true, ".mkv": true, ".avi": true}

// artifactKind classifies a produced file by its extension, the same
// convention selectImageArtifacts uses.
// artifactKind 按扩展名对产出的文件分类，与 selectImageArtifacts 的约定相同。
func artifactKind(filename string) string {
	ext := strings.ToLower(path.Ext(filename))
	switch {
	case imageArtifactExtensions[ext]:
		return flow.ArtifactImage
	case videoArtifactExtensions[ext]:
		return flow.ArtifactVideo
	}
	return flow.ArtifactFile
}

// inprocResponse is the http.ResponseWriter an in-process call writes to. It
// keeps at most maxInprocResponse bytes of body.
//
// inprocResponse 是进程内调用写入的 http.ResponseWriter，最多保留 maxInprocResponse
// 字节的响应体。
type inprocResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *inprocResponse) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *inprocResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *inprocResponse) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if room := maxInprocResponse - w.body.Len(); room < len(p) {
		w.body.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return w.body.Write(p)
}

// errorCode returns the code of an OpenAI-shaped error body, or "".
// errorCode 返回 OpenAI 形状错误体中的 code，或空字符串。
func (w *inprocResponse) errorCode() string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.body.Bytes(), &body)
	return body.Error.Code
}

// inproc calls handler as the route method+route would, with ctx's identity
// and pathValues set as the mux would set them, and body sent as JSON.
//
// inproc 像路由 method+route 那样调用 handler，带上 ctx 中的身份，像 mux 那样设置
// pathValues，并以 JSON 发送 body。
func inproc(ctx context.Context, handler http.HandlerFunc, method, route string, pathValues map[string]string, body any) *inprocResponse {
	if body == nil {
		return inprocBody(ctx, handler, method, route, pathValues, "", http.NoBody, 0)
	}
	encoded, _ := json.Marshal(body)
	return inprocBody(ctx, handler, method, route, pathValues, "application/json", bytes.NewReader(encoded), int64(len(encoded)))
}

// inprocBody is inproc with a body of the given type, of size bytes or -1.
// inprocBody 是带指定类型请求体（size 字节，未知为 -1）的 inproc。
func inprocBody(ctx context.Context, handler http.HandlerFunc, method, route string, pathValues map[string]string, contentType string, body io.Reader, size int64) *inprocResponse {
	req := (&http.Request{Method: method, URL: &url.URL{Path: route}, Header: http.Header{}}).WithContext(ctx)
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Body, req.ContentLength = io.NopCloser(body), size
	w := &inprocResponse{}
	handler(w, req)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w
}

// GenerateImage implements flow.Dispatcher. A named model is an inference
// image model and must be one this member may call; no model means the
// deployment's own image generation workflow.
//
// GenerateImage 实现 flow.Dispatcher。指定的模型是推理生图模型，且必须是该成员可以
// 调用的；不指定模型表示使用部署自己的生图工作流。
func (d studioFlowDispatcher) GenerateImage(ctx context.Context, call flow.ImageCall) (flow.MediaReply, error) {
	ctx = reqid.WithValue(ctx, reqid.New())
	if call.Model == "" {
		return d.workflowImage(ctx, call)
	}
	if len(d.h.sched.ImageCandidates(ctx, call.Model)) == 0 {
		return flow.MediaReply{}, &flow.Error{Code: codeModelUnavailable}
	}
	if call.Size != "" {
		// An inference image model takes a prompt only (inferenceUnsupported).
		// 推理生图模型只接受提示词（见 inferenceUnsupported）。
		return flow.MediaReply{}, &flow.Error{Code: codeInvalidInput}
	}
	if len(call.Prompt) > runtime.MaxImagePromptBytes || !utf8.ValidString(call.Prompt) {
		return flow.MediaReply{}, &flow.Error{Code: codeInvalidInput}
	}
	if d.h.storage == nil {
		return flow.MediaReply{}, &flow.Error{Code: codeArtifactStorage}
	}
	var (
		reply flow.MediaReply
		err   error
	)
	if qerr := d.h.withQuota(ctx, func(ctx context.Context) { reply, err = d.inferenceImage(ctx, call) }); qerr != nil {
		return flow.MediaReply{}, &flow.Error{Code: codeRateLimited}
	}
	return reply, err
}

// inferenceImage generates an image on an inference image model and writes
// it to object storage, streaming: the image is never held whole here.
//
// inferenceImage 在推理生图模型上生成图片并流式写入对象存储：图片从不在这里被完整持有。
func (d studioFlowDispatcher) inferenceImage(ctx context.Context, call flow.ImageCall) (flow.MediaReply, error) {
	identity, _ := IdentityFrom(ctx)
	meta, stream, _, err := d.h.sched.GenerateImage(ctx, runtime.ImageRequest{
		Model: call.Model, Prompt: call.Prompt, TenantID: identity.TenantID, KeyID: identity.KeyID,
	})
	if err != nil {
		return flow.MediaReply{}, dispatchFailure(ctx, err)
	}
	defer stream.Close()
	if meta.Size <= 0 || meta.Size > MaxImageResponseBytes {
		return flow.MediaReply{}, &flow.Error{Code: flow.CodeUpstream}
	}
	id := studioArtifactPrefix + idgen.New()
	key, ok := studioArtifactKey(identity.TenantID, d.session, id)
	if !ok {
		return flow.MediaReply{}, &flow.Error{Code: flow.CodeUpstream}
	}
	body := &imageStreamReader{stream: stream, left: meta.Size}
	if err := d.h.storage.Put(ctx, key, body, meta.Size); err != nil || body.left != 0 {
		_ = d.h.storage.Delete(context.WithoutCancel(ctx), key)
		if ctx.Err() != nil {
			return flow.MediaReply{}, ctx.Err()
		}
		d.h.logger.Warn("storing a studio image failed", slog.String("run_id", d.session), slog.Any("error", err))
		return flow.MediaReply{}, &flow.Error{Code: codeArtifactStorage}
	}
	return flow.MediaReply{Artifacts: []flow.Artifact{{ID: id, Filename: "image" + imageExtension(meta.ContentType), Kind: flow.ArtifactImage}}}, nil
}

// imageExtension is the image file extension for contentType, ".png" when
// it names no image type this package treats as one. mime lists several
// extensions per type in no useful order (".jfif" first for JPEG), so the
// first one that is an image extension here is taken.
//
// imageExtension 是 contentType 对应的图片扩展名；它不是本包视为图片的类型时为
// 「.png」。mime 对同一类型给出多个扩展名且顺序无用（JPEG 的第一个是「.jfif」），因此
// 取其中第一个属于这里的图片扩展名的。
func imageExtension(contentType string) string {
	exts, _ := mime.ExtensionsByType(contentType)
	for _, ext := range exts {
		if imageArtifactExtensions[ext] {
			return ext
		}
	}
	return ".png"
}

// imageStreamReader reads an image stream's byte chunks as an io.Reader,
// failing on a chunk past the announced size or a second metadata frame.
//
// imageStreamReader 以 io.Reader 的形式读取图片流的字节分块；分块超出声明的大小或
// 出现第二个元数据帧时失败。
type imageStreamReader struct {
	stream runtime.Stream[runtime.ImageEvent]
	buf    []byte
	left   int64
}

var errImageStreamMismatch = errors.New("httpapi: image stream does not match its metadata")

func (r *imageStreamReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		event, err := r.stream.Recv()
		if err != nil {
			return 0, err
		}
		if event.Meta != nil || int64(len(event.Data)) > r.left {
			return 0, errImageStreamMismatch
		}
		r.buf = event.Data
		r.left -= int64(len(event.Data))
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// studioArtifactKey is where an inference image of tenantID's run runID is
// stored. Every part must be a plain id, so no part can escape its prefix.
//
// studioArtifactKey 是 tenantID 的运行 runID 的推理图片的存储位置。每一段都必须是普通
// 的 id，任何一段都不能逃出它的前缀。
func studioArtifactKey(tenantID, runID, artifactID string) (string, bool) {
	for _, part := range []string{tenantID, runID, artifactID} {
		if part == "" || len(part) > maxStudioArtifactIDBytes || strings.ContainsFunc(part, func(r rune) bool {
			return !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
		}) {
			return "", false
		}
	}
	return studioStoragePrefix + "/" + tenantID + "/" + runID + "/" + artifactID, true
}

// workflowImage runs the deployment's image generation workflow through
// POST /v1/images/generations, asking for url answers so the images stay
// job artifacts instead of being read into this process.
//
// workflowImage 经 POST /v1/images/generations 运行部署自己的生图工作流，并要求以 url
// 作答，使图片保持为 job 产物，而不是被读进本进程。
func (d studioFlowDispatcher) workflowImage(ctx context.Context, call flow.ImageCall) (flow.MediaReply, error) {
	body := map[string]any{"prompt": call.Prompt, "response_format": "url"}
	if call.Size != "" {
		body["size"] = call.Size
	}
	var resp *inprocResponse
	if err := d.h.withQuota(ctx, func(ctx context.Context) {
		resp = inproc(ctx, d.h.imagesGenerations, http.MethodPost, "/v1/images/generations", nil, body)
	}); err != nil {
		return flow.MediaReply{}, &flow.Error{Code: codeRateLimited}
	}
	if ctx.Err() != nil {
		return flow.MediaReply{}, ctx.Err()
	}
	if resp.status != http.StatusOK {
		return flow.MediaReply{}, &flow.Error{Code: openAIFailure(resp.status, resp.errorCode())}
	}
	var out imagesResponse
	if err := json.Unmarshal(resp.body.Bytes(), &out); err != nil {
		return flow.MediaReply{}, &flow.Error{Code: flow.CodeUpstream}
	}
	identity, _ := IdentityFrom(ctx)
	var reply flow.MediaReply
	for _, item := range out.Data {
		id, ok := strings.CutPrefix(item.URL, "/v1/artifacts/")
		if !ok || id == "" {
			continue
		}
		filename := "image.png"
		if rec, ok := d.h.jobs.artifact(id, identity.TenantID); ok && rec.Ref.Filename != "" {
			filename = rec.Ref.Filename
		}
		reply.Artifacts = append(reply.Artifacts, flow.Artifact{ID: id, Filename: filename, Kind: flow.ArtifactImage})
	}
	return reply, nil
}

// RunTemplate implements flow.Dispatcher: it submits a workflow template run
// as POST /v1/workflows/{id}/runs would, follows the job to its end, and
// lists the artifacts it produced. A node stopped while its job is still
// going withdraws the job.
//
// RunTemplate 实现 flow.Dispatcher：它像 POST /v1/workflows/{id}/runs 那样提交一次工作流
// 模板运行，跟踪该 job 直到结束，并列出其产出的产物。job 仍在进行时节点被停止，则撤回该
// job。
func (d studioFlowDispatcher) RunTemplate(ctx context.Context, call flow.TemplateCall) (flow.MediaReply, error) {
	ctx = reqid.WithValue(ctx, reqid.New())
	identity, _ := IdentityFrom(ctx)
	if !d.h.templateVisible(call.Template, identity.TenantID) {
		return flow.MediaReply{}, &flow.Error{Code: codeTemplateUnavailable}
	}
	tpl, _ := d.h.workflows.Lookup(call.Template)
	inputs, code := templateInputs(tpl, call.Inputs, call.Files)
	if code != "" {
		return flow.MediaReply{}, &flow.Error{Code: code}
	}
	files, err := d.openFiles(ctx, identity.TenantID, call.Files)
	defer files.close()
	if err != nil {
		return flow.MediaReply{}, err
	}

	var resp *inprocResponse
	route, pathValues := "/v1/workflows/"+call.Template+"/runs", map[string]string{"workflow_id": call.Template}
	if err := d.h.withQuota(ctx, func(ctx context.Context) {
		if len(files) == 0 {
			resp = inproc(ctx, d.h.submitRun, http.MethodPost, route, pathValues, runRequest{Inputs: inputs})
			return
		}
		resp = submitWithFileParts(ctx, d.h.submitRun, route, pathValues, runRequest{Inputs: inputs}, files)
	}); err != nil {
		return flow.MediaReply{}, &flow.Error{Code: codeRateLimited}
	}
	if resp.status != http.StatusAccepted {
		if ctx.Err() != nil {
			return flow.MediaReply{}, ctx.Err()
		}
		return flow.MediaReply{}, &flow.Error{Code: openAIFailure(resp.status, resp.errorCode())}
	}
	var job jobJSON
	if err := json.Unmarshal(resp.body.Bytes(), &job); err != nil || job.JobID == "" {
		return flow.MediaReply{}, &flow.Error{Code: flow.CodeUpstream}
	}

	if err := d.awaitJob(ctx, job.JobID); err != nil {
		if ctx.Err() != nil {
			d.cancelJob(ctx, job.JobID)
			return flow.MediaReply{}, ctx.Err()
		}
		return flow.MediaReply{}, err
	}

	resp = inproc(ctx, d.h.listArtifacts, http.MethodGet, "/v1/jobs/"+job.JobID+"/artifacts", map[string]string{"job_id": job.JobID}, nil)
	if resp.status != http.StatusOK {
		return flow.MediaReply{}, &flow.Error{Code: openAIFailure(resp.status, resp.errorCode())}
	}
	var list artifactsResponse
	if err := json.Unmarshal(resp.body.Bytes(), &list); err != nil {
		return flow.MediaReply{}, &flow.Error{Code: flow.CodeUpstream}
	}
	reply := flow.MediaReply{Artifacts: []flow.Artifact{}}
	for _, a := range list.Data {
		// Only the run's results: "temp" and "input" are the backend's own
		// scratch and the files fed in.
		// 只取运行的结果：「temp」与「input」是后端自己的草稿与输入的文件。
		if a.Type != "" && a.Type != "output" {
			continue
		}
		reply.Artifacts = append(reply.Artifacts, flow.Artifact{ID: a.ArtifactID, Filename: a.Filename, Kind: artifactKind(a.Filename)})
	}
	return reply, nil
}

// templateInputs turns a node's rendered input values into the JSON values
// tpl declares, and checks that files go to its file inputs and only there.
// A value that renders blank is left out, so the template's default applies
// and a required input is reported by Bind.
//
// templateInputs 把节点渲染后的输入取值转换为 tpl 声明的 JSON 取值，并检查文件只交给
// 模板的文件输入。渲染为空白的取值不传，从而使用模板的默认值，必填输入由 Bind 报告。
func templateInputs(tpl *workflow.Template, values, files map[string]string) (map[string]json.RawMessage, string) {
	declared := make(map[string]workflow.Input, len(tpl.Inputs))
	for _, in := range tpl.Inputs {
		declared[in.Name] = in
	}
	for name := range files {
		if in, ok := declared[name]; !ok || in.Type != workflow.InputFile {
			return nil, codeInvalidInput
		}
	}
	out := make(map[string]json.RawMessage, len(values))
	for name, value := range values {
		in, ok := declared[name]
		if !ok {
			return nil, codeInvalidInput
		}
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		switch in.Type {
		case workflow.InputString:
			raw, _ := json.Marshal(value)
			out[name] = raw
		case workflow.InputInteger, workflow.InputNumber:
			f, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return nil, codeInvalidInput
			}
			out[name] = json.RawMessage(strconv.FormatFloat(f, 'f', -1, 64))
		case workflow.InputBoolean:
			b, err := strconv.ParseBool(strings.ToLower(text))
			if err != nil {
				return nil, codeInvalidInput
			}
			out[name] = json.RawMessage(strconv.FormatBool(b))
		default:
			// A file input takes a file of the run, never typed text.
			// 文件输入只接受运行中的文件，从不接受输入的文本。
			return nil, codeInvalidInput
		}
	}
	return out, ""
}

// awaitJob asks after jobID until it ends, tolerating a few failed asks in a
// row — a node reconnecting, a queue blinking — before giving up.
//
// awaitJob 持续询问 jobID 直到它结束；放弃之前容忍连续几次询问失败——节点重连、队列
// 短暂不可用。
func (d studioFlowDispatcher) awaitJob(ctx context.Context, jobID string) error {
	failures := 0
	for {
		resp := inproc(ctx, d.h.jobStatus, http.MethodGet, "/v1/jobs/"+jobID, map[string]string{"job_id": jobID}, nil)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var job jobJSON
		if resp.status == http.StatusOK && json.Unmarshal(resp.body.Bytes(), &job) == nil {
			failures = 0
			switch job.Status {
			case StatusSucceeded:
				return nil
			case StatusCancelled:
				return &flow.Error{Code: codeJobCancelled}
			case StatusFailed:
				switch job.ErrorCode {
				case jobqueue.CodeQueueTimeout:
					return &flow.Error{Code: codeQueueTimeout}
				case jobqueue.CodeNodeNotAllowed:
					return &flow.Error{Code: codeNoCapableNode}
				}
				return &flow.Error{Code: codeGenerationFailed}
			}
		} else if failures++; failures >= studioJobPollFailures || resp.status == http.StatusNotFound {
			return &flow.Error{Code: flow.CodeUpstream}
		}
		tick, stop := d.h.clock.NewTimer(studioJobPollInterval)
		select {
		case <-ctx.Done():
			stop()
			return ctx.Err()
		case <-tick:
		}
	}
}

// cancelJob withdraws a job whose node was stopped, best effort.
// cancelJob 尽力撤回一个节点已被停止的 job。
func (d studioFlowDispatcher) cancelJob(ctx context.Context, jobID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), studioJobCancelTimeout)
	defer cancel()
	resp := inproc(ctx, d.h.cancelJob, http.MethodPost, "/v1/jobs/"+jobID+"/cancel", map[string]string{"job_id": jobID}, nil)
	if resp.status >= 300 && resp.status != http.StatusConflict {
		d.h.logger.Warn("withdrawing a studio flow job failed", slog.String("run_id", d.session), slog.String("job_id", jobID), slog.Int("status", resp.status))
	}
}

// openAIFailure maps an in-process call's OpenAI-shaped error to a node
// failure code.
//
// openAIFailure 把进程内调用返回的 OpenAI 形状错误映射为节点失败码。
func openAIFailure(status int, code string) string {
	switch code {
	case "endpoint_not_configured":
		return codeImagesUnavailable
	case "workflow_not_found":
		return codeTemplateUnavailable
	case "workflow_backend_not_found", "no_capable_node":
		return codeNoCapableNode
	case "queue_timeout":
		return codeQueueTimeout
	case "invalid_input", "invalid_request", "unsupported_parameter", "invalid_json":
		return codeInvalidInput
	case "generation_failed", "out_of_memory", "no_image_produced":
		if code == "no_image_produced" {
			return flow.CodeNoImage
		}
		return code
	case "cancelled":
		return codeJobCancelled
	case "timeout":
		return flow.CodeNodeTimeout
	case "model_not_found", "model_not_allowed":
		return codeModelUnavailable
	}
	if status == http.StatusTooManyRequests {
		return codeRateLimited
	}
	return flow.CodeUpstream
}

// dispatchFailure maps a scheduler error to a node failure, or returns ctx's
// error when the node was stopped.
//
// dispatchFailure 把调度器错误映射为节点失败；节点被停止时返回 ctx 的错误。
func dispatchFailure(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, scheduler.ErrModelNotAllowed), errors.Is(err, scheduler.ErrNodeNotAllowed):
		return &flow.Error{Code: codeModelUnavailable}
	case errors.Is(err, scheduler.ErrNoCapableNode):
		return &flow.Error{Code: codeNoCapableNode}
	}
	return &flow.Error{Code: flow.CodeUpstream}
}

type studioImageModelsJSON struct {
	Data             []studioModelJSON `json:"data"`
	DefaultAvailable bool              `json:"default_available"`
}

// imageModels implements GET /studio/v1/image-models: the image models this
// member may call, and whether the deployment's own image generation, which
// an image_generation node without a model uses, is configured.
//
// imageModels 实现 GET /studio/v1/image-models：该成员可调用的图像模型，以及部署自己的
// 生图（不指定模型的 image_generation 节点使用它）是否已配置。
func (s *studioFlows) imageModels(w http.ResponseWriter, r *http.Request) {
	h := s.h
	out := studioImageModelsJSON{Data: []studioModelJSON{}}
	for _, m := range h.sched.Models(r.Context()) {
		if len(h.sched.ImageCandidates(r.Context(), m.ID)) == 0 {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		out.Data = append(out.Data, studioModelJSON{ID: m.ID, Name: name})
	}
	out.DefaultAvailable = h.defaultImagesAvailable()
	writeStudioJSON(w, http.StatusOK, out)
}

// defaultImagesAvailable reports whether the deployment's own image
// generation workflow, which an image_generation node without a model uses,
// is configured and registered.
//
// defaultImagesAvailable 报告部署自己的生图工作流（不指定模型的 image_generation 节点
// 使用它）是否已配置并注册。
func (h *handlers) defaultImagesAvailable() bool {
	if h.imagesWorkflowID == "" || h.workflows == nil {
		return false
	}
	_, ok := h.workflows.Lookup(h.imagesWorkflowID)
	return ok
}

// templateVisible reports whether tenantID may run workflow template id.
// templateVisible 报告 tenantID 是否可以运行工作流模板 id。
func (h *handlers) templateVisible(id, tenantID string) bool {
	if h.workflows == nil {
		return false
	}
	tpl, ok := h.workflows.Lookup(id)
	return ok && tpl.VisibleTo(tenantID)
}

type studioTemplateInputJSON struct {
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Required  bool            `json:"required"`
	Default   json.RawMessage `json:"default,omitempty"`
	MaxLength int             `json:"max_length,omitempty"`
	Min       *float64        `json:"min,omitempty"`
	Max       *float64        `json:"max,omitempty"`
}

type studioTemplateOutputJSON struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

type studioTemplateJSON struct {
	ID          string                     `json:"id"`
	Description string                     `json:"description,omitempty"`
	Inputs      []studioTemplateInputJSON  `json:"inputs"`
	Outputs     []studioTemplateOutputJSON `json:"outputs"`
}

// workflowTemplates implements GET /studio/v1/workflow-templates: the
// published workflow templates this member's tenant may run, with their
// inputs and outputs but never their graphs.
//
// workflowTemplates 实现 GET /studio/v1/workflow-templates：该成员所在租户可运行的已
// 发布工作流模板，带输入与输出声明，但绝不带图。
func (s *studioFlows) workflowTemplates(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	out := struct {
		Data []studioTemplateJSON `json:"data"`
	}{Data: []studioTemplateJSON{}}
	if s.h.workflows != nil {
		for _, id := range s.h.workflows.IDs() {
			tpl, ok := s.h.workflows.Lookup(id)
			if !ok || !tpl.VisibleTo(identity.TenantID) || tpl.Validate() != nil {
				continue
			}
			t := studioTemplateJSON{ID: tpl.ID, Description: tpl.Description,
				Inputs: make([]studioTemplateInputJSON, 0, len(tpl.Inputs)), Outputs: make([]studioTemplateOutputJSON, 0, len(tpl.Outputs))}
			for _, in := range tpl.Inputs {
				t.Inputs = append(t.Inputs, studioTemplateInputJSON{Name: in.Name, Type: string(in.Type), Required: in.Required,
					Default: in.Default, MaxLength: in.MaxLength, Min: in.Min, Max: in.Max})
			}
			for _, o := range tpl.Outputs {
				t.Outputs = append(t.Outputs, studioTemplateOutputJSON{Name: o.Name, Type: o.Type, Description: o.Description})
			}
			out.Data = append(out.Data, t)
		}
	}
	writeStudioJSON(w, http.StatusOK, out)
}

// artifact implements GET /studio/v1/runs/{id}/artifacts/{artifact_id}: one
// artifact a step of the member's own run produced. The run's steps are the
// claim: an artifact id that none of them names is not found, even one the
// tenant owns, because flows and their runs are private to their member.
//
// The body is the tenant's file, served from the Studio origin, so it is
// marked as a download that must not be sniffed or run as a document.
//
// artifact 实现 GET /studio/v1/runs/{id}/artifacts/{artifact_id}：成员自己的运行中某个
// 步骤产出的一个产物。运行的步骤就是凭据：没有任何步骤提到的产物 id 视为不存在，即便
// 租户拥有它，因为编排及其运行只属于其成员本人。
//
// 响应体是租户的文件，经 Studio 的来源提供，因此标记为下载，不得被嗅探或作为文档执行。
func (s *studioFlows) artifact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, studioRunIDPrefix, "run")
	if !ok {
		return
	}
	artifactID := r.PathValue("artifact_id")
	owner := studioOwnerOf(r)
	var run StudioRun
	if active, ok := s.runner.lookup(owner, id); ok {
		run, _ = active.view()
	} else if run, ok = s.storedRun(w, r, owner, id); !ok {
		return
	}
	filename, found := runArtifact(run, artifactID)
	if len(artifactID) > maxStudioArtifactIDBytes || !found {
		writeStudioError(w, http.StatusNotFound, codeStudioArtifactMissing, "no such artifact in this run")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "private, no-store")
	if !strings.HasPrefix(artifactID, studioArtifactPrefix) {
		s.h.serveJobArtifact(w, r, owner.TenantID, artifactID)
		return
	}
	s.serveStoredObject(w, r, owner.TenantID, id, artifactID, filename)
}

func (s *studioFlows) storedRun(w http.ResponseWriter, r *http.Request, owner StudioOwner, id string) (StudioRun, bool) {
	run, err := s.store.GetRun(r.Context(), owner, id)
	if err != nil {
		s.storeError(w, err)
		return StudioRun{}, false
	}
	return run, true
}

// serveStoredObject streams an object Studio stored for a run: an inference
// image, or a file the run was started with, named filename (empty for an
// image, which is then named by its type). Its type is sniffed from its
// first bytes rather than trusted from storage or the uploader, and only an
// image, audio or video type is ever declared.
//
// serveStoredObject 流式发送 Studio 为某次运行存放的对象：推理图片，或运行启动时收到的
// 名为 filename 的文件（图片为空，此时按类型命名）。其类型从开头的字节嗅探，而不是信任
// 存储或上传者给出的类型，并且只会声明图片、音频或视频类型。
func (s *studioFlows) serveStoredObject(w http.ResponseWriter, r *http.Request, tenantID, runID, artifactID, filename string) {
	key, ok := studioArtifactKey(tenantID, runID, artifactID)
	if !ok || s.h.storage == nil {
		writeStudioError(w, http.StatusNotFound, codeStudioArtifactMissing, "no such artifact in this run")
		return
	}
	body, info, err := s.h.storage.Open(r.Context(), key)
	if err != nil {
		if !errors.Is(err, objectstore.ErrNotFound) {
			s.logger.Warn("reading a studio image failed", slog.String("run_id", runID), slog.Any("error", err))
			writeStudioError(w, http.StatusServiceUnavailable, "storage_unavailable", "the artifact cannot be read right now")
			return
		}
		writeStudioError(w, http.StatusNotFound, codeStudioArtifactMissing, "no such artifact in this run")
		return
	}
	defer body.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(body, head)
	head = head[:n]
	contentType := sniffedMediaType(head)
	name := safeFilename(filename)
	if name == "" {
		name = "image" + imageExtension(contentType)
	}
	w.Header().Set("Content-Type", contentType)
	if info.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if _, err := io.Copy(w, io.MultiReader(bytes.NewReader(head), body)); err != nil {
		s.logger.Error("streaming a studio image failed", slog.String("run_id", runID), slog.Any("error", err))
	}
}

// sniffedMediaType is the type declared for a stored object whose first
// bytes are head: an image (never SVG, which is a document), audio or video
// type, or application/octet-stream.
//
// sniffedMediaType 是为开头字节为 head 的存储对象声明的类型：图片（绝不是 SVG，它是
// 文档）、音频或视频类型，否则为 application/octet-stream。
func sniffedMediaType(head []byte) string {
	t := http.DetectContentType(head)
	switch {
	case t == "image/svg+xml":
	case strings.HasPrefix(t, "image/"), strings.HasPrefix(t, "audio/"), strings.HasPrefix(t, "video/"):
		return t
	}
	return "application/octet-stream"
}

// runArtifact reports whether artifactID is a file run was started with or
// one a step of run produced, and returns its filename when known.
//
// runArtifact 报告 artifactID 是否是 run 启动时收到的文件或 run 的某个步骤产出的文件，
// 已知时返回其文件名。
func runArtifact(run StudioRun, artifactID string) (string, bool) {
	if artifactID == "" {
		return "", false
	}
	if f, ok := runInputFiles(run)[artifactID]; ok {
		return f.Filename, true
	}
	for _, step := range run.Steps {
		t := flowdef.NodeType(step.NodeType)
		if flowdef.IsArtifactField(t, flowdef.FieldImage) && step.Outputs[flowdef.FieldImage] == artifactID {
			return "", true
		}
		if !flowdef.IsArtifactField(t, flowdef.FieldArtifacts) {
			continue
		}
		var list []flow.Artifact
		if json.Unmarshal([]byte(step.Outputs[flowdef.FieldArtifacts]), &list) != nil {
			continue
		}
		for _, a := range list {
			if a.ID == artifactID {
				return a.Filename, true
			}
		}
	}
	return "", false
}

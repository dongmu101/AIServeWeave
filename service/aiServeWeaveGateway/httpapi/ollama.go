// ollama.go implements Gateway's second wire front door for Ollama's native
// inference API — POST /api/chat, POST /api/generate and POST
// /api/embeddings — translated at the boundary into the same canonical
// runtime.ChatRequest/EmbeddingRequest every other front door produces
// (STATUS.md's P2 "Ollama 原生 API" — see
// docs/superpowers/specs/2026-09-16-p2-api-compat-boundary-design.md §五).
// This is unrelated to how the Agent itself talks to a backend Ollama
// instance: common/runtime/ollama deliberately speaks the OpenAI-compatible
// protocol to that backend (see that package's doc comment), and nothing
// here changes that choice — this file only opens a second door on the
// Gateway-to-client side. Model-management endpoints (/api/create,
// /api/pull, /api/push, /api/delete, /api/copy, /api/show, /api/tags,
// /api/ps) answer a clear "not supported" rather than a 404 or a misleading
// success, because the Gateway never manages any node's local model files
// (design doc §五.3) — that is each node's own business with its own
// backend. Tool calls and vision ("images") inputs are refused by name,
// matching anthropic.go's "no silent drop" rule: this front door is the
// design doc's "纯推理端点" (pure inference endpoint) task (§九.2), scoped to
// plain chat/generate/embed only.
//
// ollama.go 实现 Gateway 第二套面向客户端的原生推理协议前门——POST
// /api/chat、POST /api/generate 与 POST /api/embeddings——在边界处转换成与
// 其他每个前门相同的 canonical runtime.ChatRequest/EmbeddingRequest
// （STATUS.md 的「Ollama 原生 API」——见设计文档§五）。这与 Agent 自己怎么连
// 后端 Ollama 实例无关：common/runtime/ollama 对那个后端刻意选择 OpenAI
// 兼容协议（见该包的包文档），本文件不改变这个选择——只在 Gateway 对客户端的
// 一侧再开一扇门。模型管理类端点（/api/create、/api/pull、/api/push、
// /api/delete、/api/copy、/api/show、/api/tags、/api/ps）明确答复「不支持」
// 而不是 404 或误导性的成功响应，因为 Gateway 从不管理任何节点上的本地模型
// 文件（设计文档§五.3）——那是每个节点与自己后端之间的事。工具调用与视觉
// （"images"）输入按名字拒绝，与 anthropic.go「不静默丢弃」的规则一致：本
// 前门对应设计文档的「纯推理端点」任务（§九.2），范围限定在纯
// chat/generate/embed。
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"AIServeWeave/common/runtime"
)

// -----------------------------------------------------------------------
// Shared error body and dispatch classification
// -----------------------------------------------------------------------

// ollamaErrorBody is the error shape Ollama's own API and its client
// libraries already know how to parse.
type ollamaErrorBody struct {
	Error string `json:"error"`
}

// writeOllamaError writes an Ollama-shaped error body. message is a fixed,
// generic string per error class, or a description of a request the caller
// itself built wrong — never upstream error text, matching
// writeOpenAIError's rule in errors.go.
func writeOllamaError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ollamaErrorBody{Error: message})
}

// handleOllamaDispatchError is handleDispatchError's Ollama-shaped
// counterpart, sharing dispatchErrorDetails's classification (errors.go) so
// every wire protocol judges the same dispatch failure the same way.
func handleOllamaDispatchError(w http.ResponseWriter, logger *slog.Logger, err error) {
	status, _, _, message := dispatchErrorDetails(logger, err)
	writeOllamaError(w, status, message)
}

// ollamaUnsupported answers a model-management endpoint with a clear
// "not supported" body instead of a bare 404 or a misleading success — see
// this file's package doc comment and design doc §五.3. Status 501: the
// Gateway has no code path for the operation at all, which is a statement
// about the server, not a malformed request (400) or a missing resource
// (404).
//
// ollamaUnsupported 用明确的「不支持」响应体回答一个模型管理端点，而不是
// 裸的 404 或误导性的成功——见本文件包注释与设计文档§五.3。状态码用
// 501：Gateway 对这个操作完全没有代码路径，这是关于服务器本身的陈述，不是
// 请求写错了（400）或资源不存在（404）。
func ollamaUnsupported(w http.ResponseWriter, r *http.Request) {
	writeOllamaError(w, http.StatusNotImplemented,
		"this gateway does not manage node-local models; model management endpoints are not supported (each node manages its own models independently)")
}

// writeOllamaNDJSON writes one newline-delimited JSON object — Ollama's own
// streaming framing, distinct from the SSE framing chat.go's writeSSE and
// anthropic.go's writeAnthropicSSE emit for the other two front doors.
func writeOllamaNDJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n"))
	return err
}

// ollamaDoneReason reports this Gateway's own already-classified finish
// reason (never raw backend text) as Ollama's done_reason, defaulting to
// "stop" for an empty reason. Unlike Anthropic's stop_reason, Ollama's
// done_reason is not a strict closed vocabulary in practice, so no mapping
// table is needed here.
func ollamaDoneReason(reason string) string {
	if reason == "" {
		return "stop"
	}
	return reason
}

// -----------------------------------------------------------------------
// Shared "options" bag
// -----------------------------------------------------------------------

// ollamaOptionsJSON is the subset of Ollama's free-form "options" bag this
// front door maps onto runtime.ChatRequest's sampling parameters. The bag
// also carries backend-process knobs (num_ctx, num_gpu, mirostat, …) that
// have no meaning on this Gateway's request-scoped, backend-agnostic
// dispatch model; those are silently ignored rather than rejected, the same
// treatment keep_alive gets below — this Gateway does not manage a single
// backend process's memory residency or context window sizing, so there is
// nothing to honor or refuse.
//
// ollamaOptionsJSON 是 Ollama 自由格式 "options" 包里、本前门会映射进
// runtime.ChatRequest 采样参数的子集。这个包还携带后端进程相关的旋钮
// （num_ctx、num_gpu、mirostat……），在这个按请求调度、与后端无关的模型下没有
// 对应语义；这些参数被静默忽略而非拒绝，与下面 keep_alive 的处理一致——本
// Gateway 不管理单个后端进程的内存驻留或上下文窗口大小，因此没有什么可供
// 遵从或拒绝。
type ollamaOptionsJSON struct {
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	NumPredict  *int      `json:"num_predict,omitempty"`
	Seed        *int64    `json:"seed,omitempty"`
	Stop        stopField `json:"stop,omitempty"`
}

// applyTo copies the sampling parameters this front door understands onto
// req. A nil receiver is a no-op, so callers never need to check for a
// missing "options" field themselves.
func (o *ollamaOptionsJSON) applyTo(req *runtime.ChatRequest) {
	if o == nil {
		return
	}
	req.Temperature = o.Temperature
	req.TopP = o.TopP
	req.MaxTokens = o.NumPredict
	req.Seed = o.Seed
	req.Stop = o.Stop
}

// -----------------------------------------------------------------------
// POST /api/chat
// -----------------------------------------------------------------------

type ollamaMessageJSON struct {
	Role    string          `json:"role"`
	Content string          `json:"content"`
	Images  json.RawMessage `json:"images,omitempty"`
}

type ollamaChatRequest struct {
	Model    string              `json:"model"`
	Messages []ollamaMessageJSON `json:"messages"`
	// Stream is a pointer because Ollama's own default is true when the
	// field is omitted entirely — the opposite of the OpenAI front door's
	// default — so "absent" and "explicitly false" must stay
	// distinguishable.
	//
	// Stream 用指针是因为 Ollama 自己的默认值是：字段整体缺失时视为
	// true——与 OpenAI 前门的默认值相反——因此「缺失」与「显式 false」必须
	// 保持可区分。
	Stream    *bool              `json:"stream,omitempty"`
	Options   *ollamaOptionsJSON `json:"options,omitempty"`
	Format    json.RawMessage    `json:"format,omitempty"`
	KeepAlive json.RawMessage    `json:"keep_alive,omitempty"`
	Tools     json.RawMessage    `json:"tools,omitempty"`
}

func (req ollamaChatRequest) wantsStream() bool {
	return req.Stream == nil || *req.Stream
}

// toRuntime converts req into the canonical chat request every front door
// produces. It rejects tools, format and per-message images by name rather
// than silently dropping them — see this file's package doc comment.
func (req ollamaChatRequest) toRuntime() (runtime.ChatRequest, error) {
	if len(req.Tools) > 0 {
		return runtime.ChatRequest{}, errors.New("tools is not supported by this Ollama-compatible endpoint")
	}
	if len(req.Format) > 0 {
		return runtime.ChatRequest{}, errors.New("format is not supported by this Ollama-compatible endpoint")
	}
	out := runtime.ChatRequest{Model: req.Model}
	out.Messages = make([]runtime.ChatMessage, len(req.Messages))
	for i, m := range req.Messages {
		if len(m.Images) > 0 {
			return runtime.ChatRequest{}, fmt.Errorf("messages[%d].images is not supported by this Ollama-compatible endpoint", i)
		}
		out.Messages[i] = runtime.ChatMessage{Role: m.Role, Content: m.Content}
	}
	req.Options.applyTo(&out)
	return out, nil
}

type ollamaChatResponse struct {
	Model     string            `json:"model"`
	CreatedAt string            `json:"created_at"`
	Message   ollamaMessageJSON `json:"message"`
	Done      bool              `json:"done"`
	// DoneReason, PromptEvalCount and EvalCount are omitted (not
	// zero-valued) until a terminal response actually has them: real Ollama
	// also includes total_duration/load_duration/prompt_eval_duration/
	// eval_duration timing fields this Gateway does not track — inventing a
	// number for them would misreport something as measured that was not,
	// so they are left out entirely rather than sent as a misleading zero.
	//
	// DoneReason、PromptEvalCount 与 EvalCount 只在一个终态响应真的带有它们
	// 时才输出（而非置零）：真实 Ollama 还包含
	// total_duration/load_duration/prompt_eval_duration/eval_duration 这类
	// 本 Gateway 不追踪的计时字段——为它们编造一个数字，等于把未测量的东西
	// 谎报成已测量，因此这里干脆整体不输出，而不是发送一个有误导性的零值。
	DoneReason      string `json:"done_reason,omitempty"`
	PromptEvalCount int    `json:"prompt_eval_count,omitempty"`
	EvalCount       int    `json:"eval_count,omitempty"`
}

// ollamaChat implements POST /api/chat.
//
// ollamaChat 实现 POST /api/chat。
func (h *handlers) ollamaChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req ollamaChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOllamaError(w, http.StatusBadRequest, "the request body is not valid JSON")
		return
	}
	if req.Model == "" || len(req.Messages) == 0 {
		writeOllamaError(w, http.StatusBadRequest, "model and a non-empty messages array are required")
		return
	}
	canonical, err := req.toRuntime()
	if err != nil {
		writeOllamaError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.wantsStream() {
		h.ollamaChatStream(w, r, canonical, start)
		return
	}
	h.ollamaChatOnce(w, r, canonical, start)
}

func (h *handlers) ollamaChatOnce(w http.ResponseWriter, r *http.Request, canonical runtime.ChatRequest, start time.Time) {
	resp, candidate, err := h.sched.Chat(r.Context(), canonical)
	if err != nil {
		handleOllamaDispatchError(w, h.logger, err)
		return
	}

	role := resp.Message.Role
	if role == "" {
		role = "assistant"
	}
	body := ollamaChatResponse{
		Model:           resp.Model,
		CreatedAt:       h.clock.Now().UTC().Format(time.RFC3339Nano),
		Message:         ollamaMessageJSON{Role: role, Content: resp.Message.Content},
		Done:            true,
		DoneReason:      ollamaDoneReason(resp.FinishReason),
		PromptEvalCount: resp.Usage.PromptTokens,
		EvalCount:       resp.Usage.CompletionTokens,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
	h.logTTFT(r, canonical.Model, candidate.NodeID, start, false)
	// Same reasoning as chatNonStream (chat.go): a non-streamed response's
	// first byte is its last, so only the total response time is recorded.
	h.recordUsage(r.Context(), resp.Usage, time.Since(start))
}

// ollamaChatStream serves POST /api/chat with stream true (Ollama's
// default), writing newline-delimited JSON chat responses ending in one
// with "done":true — Ollama's own streaming shape.
func (h *handlers) ollamaChatStream(w http.ResponseWriter, r *http.Request, canonical runtime.ChatRequest, start time.Time) {
	stream, candidate, err := h.sched.ChatStream(r.Context(), canonical)
	if err != nil {
		handleOllamaDispatchError(w, h.logger, err)
		return
	}
	defer stream.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOllamaError(w, http.StatusInternalServerError, "this connection does not support streaming")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	// See chat.go's chatStream for why no goroutine calls stream.Close() on
	// client disconnect: Recv already unblocks on its own once the request
	// context is done.
	loggedTTFT := false
	doneReason := ""
	// The last usage the backend reported wins, same reasoning as chat.go's
	// chatStream.
	var usage runtime.Usage
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			_ = writeOllamaNDJSON(w, ollamaChatResponse{
				Model:           canonical.Model,
				CreatedAt:       h.clock.Now().UTC().Format(time.RFC3339Nano),
				Message:         ollamaMessageJSON{Role: "assistant", Content: ""},
				Done:            true,
				DoneReason:      ollamaDoneReason(doneReason),
				PromptEvalCount: usage.PromptTokens,
				EvalCount:       usage.CompletionTokens,
			})
			flusher.Flush()
			h.recordUsage(r.Context(), usage, time.Since(start))
			return
		}
		if err != nil {
			// The stream already wrote at least one ndjson object; an
			// Ollama-shaped JSON error body still parses as one more
			// ndjson line, so the failure is reported that way instead of
			// a changed status code — same reasoning as chat.go's
			// chatStream reporting failures as an SSE event.
			h.logger.Error("ollama chat stream failed", slog.Any("error", err), slog.String("node_id", candidate.NodeID))
			_ = writeOllamaNDJSON(w, ollamaErrorBody{Error: "the stream ended with an error"})
			flusher.Flush()
			return
		}

		if ev.Usage != nil {
			usage = *ev.Usage
		}
		if ev.FinishReason != "" {
			doneReason = ev.FinishReason
		}
		if ev.Delta.Content == "" {
			continue
		}
		if err := writeOllamaNDJSON(w, ollamaChatResponse{
			Model:     canonical.Model,
			CreatedAt: h.clock.Now().UTC().Format(time.RFC3339Nano),
			Message:   ollamaMessageJSON{Role: "assistant", Content: ev.Delta.Content},
			Done:      false,
		}); err != nil {
			return
		}
		flusher.Flush()
		if !loggedTTFT {
			loggedTTFT = true
			h.logTTFT(r, canonical.Model, candidate.NodeID, start, true)
			h.metrics.TTFT(EndpointOllamaChat, time.Since(start))
		}
	}
}

// -----------------------------------------------------------------------
// POST /api/generate
// -----------------------------------------------------------------------

type ollamaGenerateRequest struct {
	Model     string             `json:"model"`
	Prompt    string             `json:"prompt"`
	System    string             `json:"system,omitempty"`
	Stream    *bool              `json:"stream,omitempty"`
	Options   *ollamaOptionsJSON `json:"options,omitempty"`
	Format    json.RawMessage    `json:"format,omitempty"`
	KeepAlive json.RawMessage    `json:"keep_alive,omitempty"`
	Images    json.RawMessage    `json:"images,omitempty"`
	Context   json.RawMessage    `json:"context,omitempty"`
	Raw       bool               `json:"raw,omitempty"`
	Template  string             `json:"template,omitempty"`
	Suffix    string             `json:"suffix,omitempty"`
}

func (req ollamaGenerateRequest) wantsStream() bool {
	return req.Stream == nil || *req.Stream
}

// toRuntime converts req into the canonical chat request every front door
// produces, reducing prompt/system to a two-message exchange. It rejects
// format, images, context, raw, template and suffix by name: none of them
// has a home in the canonical request, and this Gateway has no per-request
// continuation state to honor "context" against — see this file's package
// doc comment.
func (req ollamaGenerateRequest) toRuntime() (runtime.ChatRequest, error) {
	if len(req.Format) > 0 {
		return runtime.ChatRequest{}, errors.New("format is not supported by this Ollama-compatible endpoint")
	}
	if len(req.Images) > 0 {
		return runtime.ChatRequest{}, errors.New("images is not supported by this Ollama-compatible endpoint")
	}
	if len(req.Context) > 0 {
		return runtime.ChatRequest{}, errors.New("context is not supported by this Ollama-compatible endpoint")
	}
	if req.Raw {
		return runtime.ChatRequest{}, errors.New("raw is not supported by this Ollama-compatible endpoint")
	}
	if req.Template != "" {
		return runtime.ChatRequest{}, errors.New("template is not supported by this Ollama-compatible endpoint")
	}
	if req.Suffix != "" {
		return runtime.ChatRequest{}, errors.New("suffix is not supported by this Ollama-compatible endpoint")
	}

	out := runtime.ChatRequest{Model: req.Model}
	if req.System != "" {
		out.Messages = append(out.Messages, runtime.ChatMessage{Role: "system", Content: req.System})
	}
	out.Messages = append(out.Messages, runtime.ChatMessage{Role: "user", Content: req.Prompt})
	req.Options.applyTo(&out)
	return out, nil
}

type ollamaGenerateResponse struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	Response  string `json:"response"`
	Done      bool   `json:"done"`
	// See ollamaChatResponse's doc comment: omitted rather than
	// zero-valued until a terminal response actually has them.
	DoneReason      string `json:"done_reason,omitempty"`
	PromptEvalCount int    `json:"prompt_eval_count,omitempty"`
	EvalCount       int    `json:"eval_count,omitempty"`
}

// ollamaGenerate implements POST /api/generate.
//
// ollamaGenerate 实现 POST /api/generate。
func (h *handlers) ollamaGenerate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req ollamaGenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOllamaError(w, http.StatusBadRequest, "the request body is not valid JSON")
		return
	}
	if req.Model == "" || req.Prompt == "" {
		writeOllamaError(w, http.StatusBadRequest, "model and prompt are required")
		return
	}
	canonical, err := req.toRuntime()
	if err != nil {
		writeOllamaError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.wantsStream() {
		h.ollamaGenerateStream(w, r, canonical, start)
		return
	}
	h.ollamaGenerateOnce(w, r, canonical, start)
}

func (h *handlers) ollamaGenerateOnce(w http.ResponseWriter, r *http.Request, canonical runtime.ChatRequest, start time.Time) {
	resp, candidate, err := h.sched.Chat(r.Context(), canonical)
	if err != nil {
		handleOllamaDispatchError(w, h.logger, err)
		return
	}

	body := ollamaGenerateResponse{
		Model:           resp.Model,
		CreatedAt:       h.clock.Now().UTC().Format(time.RFC3339Nano),
		Response:        resp.Message.Content,
		Done:            true,
		DoneReason:      ollamaDoneReason(resp.FinishReason),
		PromptEvalCount: resp.Usage.PromptTokens,
		EvalCount:       resp.Usage.CompletionTokens,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
	h.logTTFT(r, canonical.Model, candidate.NodeID, start, false)
	h.recordUsage(r.Context(), resp.Usage, time.Since(start))
}

func (h *handlers) ollamaGenerateStream(w http.ResponseWriter, r *http.Request, canonical runtime.ChatRequest, start time.Time) {
	stream, candidate, err := h.sched.ChatStream(r.Context(), canonical)
	if err != nil {
		handleOllamaDispatchError(w, h.logger, err)
		return
	}
	defer stream.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOllamaError(w, http.StatusInternalServerError, "this connection does not support streaming")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	loggedTTFT := false
	doneReason := ""
	var usage runtime.Usage
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			_ = writeOllamaNDJSON(w, ollamaGenerateResponse{
				Model:           canonical.Model,
				CreatedAt:       h.clock.Now().UTC().Format(time.RFC3339Nano),
				Response:        "",
				Done:            true,
				DoneReason:      ollamaDoneReason(doneReason),
				PromptEvalCount: usage.PromptTokens,
				EvalCount:       usage.CompletionTokens,
			})
			flusher.Flush()
			h.recordUsage(r.Context(), usage, time.Since(start))
			return
		}
		if err != nil {
			h.logger.Error("ollama generate stream failed", slog.Any("error", err), slog.String("node_id", candidate.NodeID))
			_ = writeOllamaNDJSON(w, ollamaErrorBody{Error: "the stream ended with an error"})
			flusher.Flush()
			return
		}

		if ev.Usage != nil {
			usage = *ev.Usage
		}
		if ev.FinishReason != "" {
			doneReason = ev.FinishReason
		}
		if ev.Delta.Content == "" {
			continue
		}
		if err := writeOllamaNDJSON(w, ollamaGenerateResponse{
			Model:     canonical.Model,
			CreatedAt: h.clock.Now().UTC().Format(time.RFC3339Nano),
			Response:  ev.Delta.Content,
			Done:      false,
		}); err != nil {
			return
		}
		flusher.Flush()
		if !loggedTTFT {
			loggedTTFT = true
			h.logTTFT(r, canonical.Model, candidate.NodeID, start, true)
			h.metrics.TTFT(EndpointOllamaGenerate, time.Since(start))
		}
	}
}

// -----------------------------------------------------------------------
// POST /api/embeddings
// -----------------------------------------------------------------------

type ollamaEmbeddingsRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbeddingsResponse struct {
	Embedding []float32 `json:"embedding"`
}

// ollamaEmbeddings implements POST /api/embeddings — Ollama's legacy,
// single-prompt embedding endpoint (as opposed to the newer batch
// /api/embed, which this v1 front door does not implement).
//
// ollamaEmbeddings 实现 POST /api/embeddings——Ollama 的单条 prompt 旧版
// embedding 端点（相对于更新的批量 /api/embed，本 v1 前门不实现后者）。
func (h *handlers) ollamaEmbeddings(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req ollamaEmbeddingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOllamaError(w, http.StatusBadRequest, "the request body is not valid JSON")
		return
	}
	if req.Model == "" || req.Prompt == "" {
		writeOllamaError(w, http.StatusBadRequest, "model and prompt are required")
		return
	}

	resp, _, err := h.sched.Embed(r.Context(), runtime.EmbeddingRequest{Model: req.Model, Input: []string{req.Prompt}})
	if err != nil {
		handleOllamaDispatchError(w, h.logger, err)
		return
	}

	var vector []float32
	if len(resp.Data) > 0 {
		vector = resp.Data[0].Vector
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ollamaEmbeddingsResponse{Embedding: vector})
	// Same reasoning as embeddings.go's embeddings: prompt tokens only, no
	// output stream to have a rate.
	h.recordUsage(r.Context(), resp.Usage, time.Since(start))
}

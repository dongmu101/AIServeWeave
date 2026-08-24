package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"AIServeWeave/common/runtime"
)

// -----------------------------------------------------------------------
// Wire types
// -----------------------------------------------------------------------

type chatMessageJSON struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCallJSON `json:"tool_calls,omitempty"`
}

type toolCallJSON struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function functionCallJSON `json:"function"`
}

type functionCallJSON struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolJSON struct {
	Type     string          `json:"type"`
	Function functionDefJSON `json:"function"`
}

type functionDefJSON struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type responseFormatJSON struct {
	Type       string                `json:"type"`
	JSONSchema *jsonSchemaFormatJSON `json:"json_schema,omitempty"`
}

type jsonSchemaFormatJSON struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema"`
}

// stopField accepts OpenAI's "stop" as either a single string or an array
// of strings.
type stopField []string

func (f *stopField) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one != "" {
			*f = []string{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*f = many
	return nil
}

type chatCompletionRequest struct {
	Model          string              `json:"model"`
	Messages       []chatMessageJSON   `json:"messages"`
	Stream         bool                `json:"stream,omitempty"`
	Temperature    *float64            `json:"temperature,omitempty"`
	TopP           *float64            `json:"top_p,omitempty"`
	MaxTokens      *int                `json:"max_tokens,omitempty"`
	Stop           stopField           `json:"stop,omitempty"`
	Seed           *int64              `json:"seed,omitempty"`
	Tools          []toolJSON          `json:"tools,omitempty"`
	ToolChoice     json.RawMessage     `json:"tool_choice,omitempty"`
	ResponseFormat *responseFormatJSON `json:"response_format,omitempty"`
}

func (req chatCompletionRequest) toRuntime() runtime.ChatRequest {
	out := runtime.ChatRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Stop:        req.Stop,
		Seed:        req.Seed,
	}
	out.Messages = make([]runtime.ChatMessage, len(req.Messages))
	for i, m := range req.Messages {
		out.Messages[i] = runtime.ChatMessage{
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			ToolCalls:  toRuntimeToolCalls(m.ToolCalls),
		}
	}
	if len(req.Tools) > 0 {
		out.Tools = make([]runtime.Tool, len(req.Tools))
		for i, t := range req.Tools {
			out.Tools[i] = runtime.Tool{
				Type: t.Type,
				Function: runtime.FunctionDefinition{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					Parameters:  t.Function.Parameters,
				},
			}
		}
	}
	if choice, ok := decodeToolChoice(req.ToolChoice); ok {
		out.ToolChoice = choice
	}
	if req.ResponseFormat != nil {
		rf := &runtime.ResponseFormat{Type: req.ResponseFormat.Type}
		if req.ResponseFormat.JSONSchema != nil {
			rf.JSONSchema = &runtime.JSONSchemaFormat{
				Name:   req.ResponseFormat.JSONSchema.Name,
				Strict: req.ResponseFormat.JSONSchema.Strict,
				Schema: req.ResponseFormat.JSONSchema.Schema,
			}
		}
		out.ResponseFormat = rf
	}
	return out
}

// decodeToolChoice reduces OpenAI's tool_choice — a bare string ("auto",
// "none", "required") or an object naming one tool — to the plain string
// runtime.ChatRequest.ToolChoice carries: a quoted JSON string is unquoted,
// anything else passes through as compact JSON text for the backend adapter
// to interpret.
func decodeToolChoice(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	return string(raw), true
}

func toRuntimeToolCalls(calls []toolCallJSON) []runtime.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]runtime.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = runtime.ToolCall{
			ID:       c.ID,
			Type:     c.Type,
			Function: runtime.FunctionCall{Name: c.Function.Name, Arguments: c.Function.Arguments},
		}
	}
	return out
}

func fromRuntimeToolCalls(calls []runtime.ToolCall) []toolCallJSON {
	if len(calls) == 0 {
		return nil
	}
	out := make([]toolCallJSON, len(calls))
	for i, c := range calls {
		out[i] = toolCallJSON{
			ID:       c.ID,
			Type:     c.Type,
			Function: functionCallJSON{Name: c.Function.Name, Arguments: c.Function.Arguments},
		}
	}
	return out
}

type usageJSON struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens"`
}

type chatCompletionResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []chatChoiceJSON `json:"choices"`
	Usage   usageJSON        `json:"usage"`
}

type chatChoiceJSON struct {
	Index        int             `json:"index"`
	Message      chatMessageJSON `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type chatCompletionChunk struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Created int64                 `json:"created"`
	Model   string                `json:"model"`
	Choices []chatChunkChoiceJSON `json:"choices"`
	Usage   *usageJSON            `json:"usage,omitempty"`
}

type chatChunkChoiceJSON struct {
	Index        int                  `json:"index"`
	Delta        chatMessageDeltaJSON `json:"delta"`
	FinishReason *string              `json:"finish_reason"`
}

type chatMessageDeltaJSON struct {
	Role      string         `json:"role,omitempty"`
	Content   string         `json:"content,omitempty"`
	ToolCalls []toolCallJSON `json:"tool_calls,omitempty"`
}

// -----------------------------------------------------------------------
// Handler
// -----------------------------------------------------------------------

// chatCompletions implements POST /v1/chat/completions, dispatching to
// chatNonStream or chatStream depending on the "stream" field.
func (h *handlers) chatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "the request body is not valid JSON")
		return
	}
	if req.Model == "" || len(req.Messages) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "model and a non-empty messages array are required")
		return
	}

	if req.Stream {
		h.chatStream(w, r, req, start)
		return
	}
	h.chatNonStream(w, r, req, start)
}

func (h *handlers) chatNonStream(w http.ResponseWriter, r *http.Request, req chatCompletionRequest, start time.Time) {
	resp, candidate, err := h.sched.Chat(r.Context(), req.toRuntime())
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}

	body := chatCompletionResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: resp.CreatedAt.Unix(),
		Model:   resp.Model,
		Choices: []chatChoiceJSON{{
			Index: 0,
			Message: chatMessageJSON{
				Role:      resp.Message.Role,
				Content:   resp.Message.Content,
				ToolCalls: fromRuntimeToolCalls(resp.Message.ToolCalls),
			},
			FinishReason: resp.FinishReason,
		}},
		Usage: usageJSON{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
	h.logTTFT(r, req.Model, candidate.NodeID, start, false)
	// A non-streamed response's first byte is its last: TTFT and total
	// response time are the same number, and only the latter is recorded, so
	// the TTFT distribution stays a statement about streaming.
	//
	// 非流式响应的首字节就是它的末字节：TTFT 与总响应时间是同一个数字，因此只记录
	// 后者，好让 TTFT 分布始终是关于流式的陈述。
	h.recordUsage(r.Context(), resp.Usage, time.Since(start))
}

func (h *handlers) chatStream(w http.ResponseWriter, r *http.Request, req chatCompletionRequest, start time.Time) {
	stream, candidate, err := h.sched.ChatStream(r.Context(), req.toRuntime())
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}
	defer stream.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "api_error", "streaming_unsupported", "this connection does not support streaming")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// A client disconnect unblocks the Recv below on its own:
	// tunnelserver.Response.Recv selects on the request's context (call.go),
	// so cancellation returns from it immediately and the deferred Close then
	// carries the cancel across the tunnel to the Agent, per AGENTS.md's
	// "任何一跳都不得无界缓冲".
	//
	// There used to be a goroutine here that called Close on cancellation.
	// It bought nothing — Recv already returns on its own — and it broke
	// Response's stated single-consumer contract (see call.go's note on the
	// recording state), which the race detector caught as a real data race
	// between that Close and this Recv.
	//
	// 客户端断连会让下面的 Recv 自行解除阻塞：tunnelserver.Response.Recv 自己 select
	// 请求的 context（call.go），因此取消会让它立即返回，随后被 defer 的 Close 把这次
	// 取消经隧道送给 Agent，对应 AGENTS.md 的「任何一跳都不得无界缓冲」。
	//
	// 这里原本有一个在取消时调用 Close 的协程。它什么也没换来——Recv 本就会自行返回
	// ——却破坏了 Response 声明的单消费者契约（见 call.go 对记录状态的说明），竞态
	// 检测器把它抓成了那次 Close 与这里的 Recv 之间的一次真实数据竞争。

	loggedTTFT := false
	// The last usage the backend reported wins: OpenAI's streaming protocol
	// puts usage on a trailing chunk, and a backend that sends it more than
	// once is restating a running total rather than adding to it.
	//
	// 以后端最后一次上报的用量为准：OpenAI 的流式协议把用量放在末尾的 chunk 上，而
	// 多次发送用量的后端是在重述一个累计值，不是在做累加。
	var usage runtime.Usage
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
			h.recordUsage(r.Context(), usage, time.Since(start))
			return
		}
		if err != nil {
			// The stream already wrote at least the SSE header; an
			// OpenAI-shaped JSON error body would not parse as an SSE
			// event, so the failure is reported as an event instead of a
			// changed status code.
			h.logger.Error("chat stream failed", slog.Any("error", err), slog.String("node_id", candidate.NodeID))
			_ = writeSSE(w, chatCompletionChunk{
				Object:  "chat.completion.chunk",
				Choices: []chatChunkChoiceJSON{{Delta: chatMessageDeltaJSON{}, FinishReason: strPtr("error")}},
			})
			flusher.Flush()
			return
		}

		chunk := chatCompletionChunk{
			ID:     ev.ID,
			Object: "chat.completion.chunk",
			Model:  ev.Model,
			Choices: []chatChunkChoiceJSON{{
				Index: 0,
				Delta: chatMessageDeltaJSON{
					Role:      ev.Delta.Role,
					Content:   ev.Delta.Content,
					ToolCalls: toolCallDeltasToJSON(ev.Delta.ToolCalls),
				},
				FinishReason: finishReasonPtr(ev.FinishReason),
			}},
		}
		if ev.Usage != nil {
			usage = *ev.Usage
			chunk.Usage = &usageJSON{
				PromptTokens:     ev.Usage.PromptTokens,
				CompletionTokens: ev.Usage.CompletionTokens,
				TotalTokens:      ev.Usage.TotalTokens,
			}
		}
		if err := writeSSE(w, chunk); err != nil {
			return
		}
		flusher.Flush()
		if !loggedTTFT {
			loggedTTFT = true
			h.logTTFT(r, req.Model, candidate.NodeID, start, true)
			// Measured after Flush, not before: a chunk still sitting in a
			// buffer has not reached anyone, and the whole point of this
			// figure is what the client actually experienced.
			//
			// 在 Flush 之后而不是之前计量：还留在缓冲里的 chunk 并没有到达任何人，
			// 而这个数字的全部意义就在于客户端实际体验到了什么。
			h.metrics.TTFT(EndpointChatCompletions, time.Since(start))
		}
	}
}

func writeSSE(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n\n"))
	return err
}

func toolCallDeltasToJSON(deltas []runtime.ToolCallDelta) []toolCallJSON {
	if len(deltas) == 0 {
		return nil
	}
	out := make([]toolCallJSON, len(deltas))
	for i, d := range deltas {
		out[i] = toolCallJSON{
			ID:       d.ID,
			Type:     d.Type,
			Function: functionCallJSON{Name: d.Function.Name, Arguments: d.Function.Arguments},
		}
	}
	return out
}

func finishReasonPtr(reason string) *string {
	if reason == "" {
		return nil
	}
	return &reason
}

func strPtr(s string) *string { return &s }

// logTTFT records the time from this handler receiving the request to its
// first byte of response content — the front-door TTFT figure tunnel/README.md's
// phase 7 asks be measured against the tunnel-only baseline. It never logs
// message content.
func (h *handlers) logTTFT(r *http.Request, model, nodeID string, start time.Time, streamed bool) {
	h.logger.Info("chat ttft",
		slog.String("request_id", requestIDFrom(r.Context())),
		slog.String("model", model),
		slog.String("node_id", nodeID),
		slog.Bool("streamed", streamed),
		slog.Duration("ttft", time.Since(start)))
}

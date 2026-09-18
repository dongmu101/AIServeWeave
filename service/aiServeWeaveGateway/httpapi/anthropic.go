// anthropic.go implements a v1, text-only subset of Anthropic's POST
// /v1/messages, translated at the boundary into the same canonical chat
// request every other front door produces (STATUS.md's P2 "Anthropic
// Messages 前门" — see docs/superpowers/specs/2026-09-16-p2-api-compat-boundary-design.md
// §四). Tool calls and non-text content blocks are refused by name rather
// than silently dropped, matching responses.go's "unsupported 字段" pattern;
// full functional parity (structured content blocks, tool_use) needs a
// core-type change shared with the OpenAI front doors and is deliberately
// out of scope here (design doc §四.3).
//
// anthropic.go 实现 POST /v1/messages 的 v1、纯文本子集，在边界处转换成与其他
// 每个前门相同的 canonical 聊天请求（STATUS.md 的「Anthropic Messages 前门」
// ——见设计文档§四）。工具调用与非文本内容块按名字拒绝而非静默丢弃，与
// responses.go 的「unsupported 字段」模式一致；追求完整功能对等需要一次与
// OpenAI 前门共用的核心类型改动，本项刻意不做（设计文档§四.3）。
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
)

// -----------------------------------------------------------------------
// Request wire types
// -----------------------------------------------------------------------

// anthropicMessagesRequest is the subset of POST /v1/messages this Gateway
// serves. Tools and ToolChoice are present so toRuntime can refuse them by
// name instead of silently ignoring them.
type anthropicMessagesRequest struct {
	Model         string                 `json:"model"`
	Messages      []anthropicMessageJSON `json:"messages"`
	System        json.RawMessage        `json:"system,omitempty"`
	MaxTokens     int                    `json:"max_tokens"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`

	// Refused below by toRuntime. v1 has no core-type home for tool_use/
	// tool_result content blocks — see the design doc's §四.3 gap.
	//
	// 以下字段由 toRuntime 拒绝。v1 没有可以承载 tool_use/tool_result 内容块
	// 的核心类型——见设计文档§四.3 的缺口。
	Tools      json.RawMessage `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
}

type anthropicMessageJSON struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// anthropicContentBlockJSON is one element of a "content" array. "text" is
// the only block type v1 accepts on input; anthropicText rejects any other
// type by name. The same struct doubles as the output content block shape,
// where Type is always "text".
type anthropicContentBlockJSON struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// anthropicText reduces an Anthropic "content" field — a bare string or an
// array of content blocks — to the plain text runtime.ChatMessage.Content
// carries. A block whose type is not "text" (image, tool_use, tool_result,
// document, …) is rejected by name: v1 has nowhere to put it, and silently
// dropping it would answer a request the caller never actually sent.
//
// anthropicText 把 Anthropic 的 "content" 字段——一个裸字符串或一个内容块
// 数组——归约成 runtime.ChatMessage.Content 承载的纯文本。类型不是 "text"
// 的块（image、tool_use、tool_result、document……）按名字拒绝：v1 没有地方
// 安放它，静默丢弃等于在回答一个调用方从未真正发出的请求。
func anthropicText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var blocks []anthropicContentBlockJSON
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", errors.New("content must be a string or an array of content blocks")
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type != "text" {
			return "", fmt.Errorf("content block type %q is not supported by this v1 Anthropic Messages endpoint", b.Type)
		}
		sb.WriteString(b.Text)
	}
	return sb.String(), nil
}

// toRuntime converts req into the canonical chat request every front door
// produces. It rejects tools, tool_choice and any message role other than
// "user"/"assistant" by name; anthropicText rejects non-text content
// blocks the same way.
func (req anthropicMessagesRequest) toRuntime() (runtime.ChatRequest, error) {
	if len(req.Tools) > 0 {
		return runtime.ChatRequest{}, errors.New("tools is not supported by this v1 Anthropic Messages endpoint")
	}
	if len(req.ToolChoice) > 0 {
		return runtime.ChatRequest{}, errors.New("tool_choice is not supported by this v1 Anthropic Messages endpoint")
	}

	maxTokens := req.MaxTokens
	out := runtime.ChatRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   &maxTokens,
		Stop:        req.StopSequences,
	}

	system, err := anthropicText(req.System)
	if err != nil {
		return runtime.ChatRequest{}, fmt.Errorf("system: %w", err)
	}
	if system != "" {
		out.Messages = append(out.Messages, runtime.ChatMessage{Role: "system", Content: system})
	}

	for i, m := range req.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return runtime.ChatRequest{}, fmt.Errorf(`messages[%d].role must be "user" or "assistant"`, i)
		}
		text, err := anthropicText(m.Content)
		if err != nil {
			return runtime.ChatRequest{}, fmt.Errorf("messages[%d]: %w", i, err)
		}
		out.Messages = append(out.Messages, runtime.ChatMessage{Role: m.Role, Content: text})
	}
	return out, nil
}

// -----------------------------------------------------------------------
// Response wire types
// -----------------------------------------------------------------------

type anthropicUsageJSON struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicMessageResponse struct {
	ID           string                      `json:"id"`
	Type         string                      `json:"type"`
	Role         string                      `json:"role"`
	Model        string                      `json:"model"`
	Content      []anthropicContentBlockJSON `json:"content"`
	StopReason   string                      `json:"stop_reason"`
	StopSequence *string                     `json:"stop_sequence"`
	Usage        anthropicUsageJSON          `json:"usage"`
}

// anthropicStopReason maps this Gateway's backend-opaque finish reason
// (OpenAI-shaped: "stop", "length", "tool_calls", …) onto Anthropic's
// closed stop_reason vocabulary. An unrecognized or empty reason maps to
// "end_turn" — the value a client is least likely to branch on
// defensively, since it is what a normal completion reports. "tool_calls"
// is mapped even though v1 never sends tools (§ toRuntime): a backend is
// not obligated to honor the absence of a tools field, and this keeps that
// case from being misreported as a normal stop.
func anthropicStopReason(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}

// -----------------------------------------------------------------------
// Error body
// -----------------------------------------------------------------------

// anthropicErrorBody is the error shape an Anthropic-compatible client
// already knows how to parse.
type anthropicErrorBody struct {
	Type  string             `json:"type"`
	Error anthropicErrorJSON `json:"error"`
}

type anthropicErrorJSON struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// writeAnthropicError writes an Anthropic-shaped error body. message is
// either a fixed, generic string per error class, or — for a request the
// caller itself built wrong (missing field, unsupported parameter) — a
// description of that mistake; it is never upstream error text, matching
// writeOpenAIError's rule in errors.go.
func writeAnthropicError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(anthropicErrorBody{Type: "error", Error: anthropicErrorJSON{Type: errType, Message: message}})
}

// handleAnthropicDispatchError is handleDispatchError's Anthropic-shaped
// counterpart, sharing dispatchErrorDetails's classification (errors.go) so
// the two wire protocols never judge the same failure differently.
func handleAnthropicDispatchError(w http.ResponseWriter, logger *slog.Logger, err error) {
	status, errType, _, message := dispatchErrorDetails(logger, err)
	writeAnthropicError(w, status, errType, message)
}

// -----------------------------------------------------------------------
// Handler
// -----------------------------------------------------------------------

// anthropicMessages implements POST /v1/messages.
//
// anthropicMessages 实现 POST /v1/messages。
func (h *handlers) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req anthropicMessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "the request body is not valid JSON")
		return
	}
	if req.Model == "" || len(req.Messages) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "model and a non-empty messages array are required")
		return
	}
	if req.MaxTokens <= 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "max_tokens is required and must be greater than zero")
		return
	}
	canonical, err := req.toRuntime()
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	if req.Stream {
		h.anthropicMessagesStream(w, r, canonical, start)
		return
	}
	h.anthropicMessagesOnce(w, r, canonical, start)
}

func (h *handlers) anthropicMessagesOnce(w http.ResponseWriter, r *http.Request, canonical runtime.ChatRequest, start time.Time) {
	resp, candidate, err := h.sched.Chat(r.Context(), canonical)
	if err != nil {
		handleAnthropicDispatchError(w, h.logger, err)
		return
	}

	role := resp.Message.Role
	if role == "" {
		role = "assistant"
	}
	body := anthropicMessageResponse{
		ID:         resp.ID,
		Type:       "message",
		Role:       role,
		Model:      resp.Model,
		Content:    []anthropicContentBlockJSON{{Type: "text", Text: resp.Message.Content}},
		StopReason: anthropicStopReason(resp.FinishReason),
		Usage: anthropicUsageJSON{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
	h.logTTFT(r, canonical.Model, candidate.NodeID, start, false)
	// Same reasoning as chatNonStream (chat.go): a non-streamed response's
	// first byte is its last, so only the total response time is recorded.
	h.recordUsage(r.Context(), resp.Usage, time.Since(start), UsageEndpointAnthropicMessages, canonical.Model)
}

// -----------------------------------------------------------------------
// Streaming event wire types
// -----------------------------------------------------------------------

type anthropicMessageStartEvent struct {
	Type    string                   `json:"type"`
	Message anthropicMessageResponse `json:"message"`
}

type anthropicContentBlockStartEvent struct {
	Type         string                    `json:"type"`
	Index        int                       `json:"index"`
	ContentBlock anthropicContentBlockJSON `json:"content_block"`
}

type anthropicTextDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicContentBlockDeltaEvent struct {
	Type  string             `json:"type"`
	Index int                `json:"index"`
	Delta anthropicTextDelta `json:"delta"`
}

type anthropicContentBlockStopEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type anthropicMessageDeltaBody struct {
	StopReason   string  `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
}

type anthropicMessageDeltaEvent struct {
	Type  string                    `json:"type"`
	Delta anthropicMessageDeltaBody `json:"delta"`
	Usage anthropicUsageJSON        `json:"usage"`
}

type anthropicMessageStopEvent struct {
	Type string `json:"type"`
}

type anthropicStreamErrorEvent struct {
	Type  string             `json:"type"`
	Error anthropicErrorJSON `json:"error"`
}

// writeAnthropicSSE writes one named SSE event ("event: <name>\ndata:
// <json>\n\n"), Anthropic's streaming framing — distinct from the bare
// "data: …" chat.go's writeSSE emits for the OpenAI front door.
func writeAnthropicSSE(w io.Writer, event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("event: " + event + "\n")); err != nil {
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

// anthropicMessagesStream serves POST /v1/messages with stream:true,
// translating runtime.ChatEvent values into Anthropic's
// message_start/content_block_start/content_block_delta/content_block_stop/
// message_delta/message_stop sequence. message_start and content_block_start
// are written unconditionally before the first Recv — not lazily on first
// content — both because Anthropic's own wire protocol always opens them
// before any delta, and because deferring them to first content would skip
// them entirely for a response with no content (stop_reason alone).
//
// anthropicMessagesStream 服务带 stream:true 的 POST /v1/messages，把
// runtime.ChatEvent 转换成 Anthropic 的
// message_start/content_block_start/content_block_delta/content_block_stop/
// message_delta/message_stop 序列。message_start 与 content_block_start 在第
// 一次 Recv 之前就无条件写出——而不是等到第一段内容才写——原因有二：Anthropic
// 自己的协议本就在任何 delta 之前打开它们；且如果推迟到第一段内容，一个没有
// 内容、只有 stop_reason 的响应会完全跳过它们。
func (h *handlers) anthropicMessagesStream(w http.ResponseWriter, r *http.Request, canonical runtime.ChatRequest, start time.Time) {
	stream, candidate, err := h.sched.ChatStream(r.Context(), canonical)
	if err != nil {
		handleAnthropicDispatchError(w, h.logger, err)
		return
	}
	defer stream.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "this connection does not support streaming")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// See chat.go's chatStream for why no goroutine calls stream.Close() on
	// client disconnect: Recv already unblocks on its own once the request
	// context is done, and a second Close caller here would race with it.
	messageID := "msg_" + newRequestID()
	_ = writeAnthropicSSE(w, "message_start", anthropicMessageStartEvent{
		Type: "message_start",
		Message: anthropicMessageResponse{
			ID:      messageID,
			Type:    "message",
			Role:    "assistant",
			Model:   canonical.Model,
			Content: []anthropicContentBlockJSON{},
			Usage:   anthropicUsageJSON{},
		},
	})
	_ = writeAnthropicSSE(w, "content_block_start", anthropicContentBlockStartEvent{
		Type:         "content_block_start",
		Index:        0,
		ContentBlock: anthropicContentBlockJSON{Type: "text", Text: ""},
	})
	flusher.Flush()

	loggedTTFT := false
	stopReason := ""
	// The last usage the backend reported wins, same reasoning as chat.go's
	// chatStream: OpenAI-shaped backends restate a running total rather than
	// adding to it.
	var usage runtime.Usage
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			_ = writeAnthropicSSE(w, "content_block_stop", anthropicContentBlockStopEvent{Type: "content_block_stop", Index: 0})
			if stopReason == "" {
				stopReason = anthropicStopReason("")
			}
			_ = writeAnthropicSSE(w, "message_delta", anthropicMessageDeltaEvent{
				Type:  "message_delta",
				Delta: anthropicMessageDeltaBody{StopReason: stopReason},
				Usage: anthropicUsageJSON{OutputTokens: usage.CompletionTokens},
			})
			_ = writeAnthropicSSE(w, "message_stop", anthropicMessageStopEvent{Type: "message_stop"})
			flusher.Flush()
			h.recordUsage(r.Context(), usage, time.Since(start), UsageEndpointAnthropicMessages, canonical.Model)
			return
		}
		if err != nil {
			// The stream already wrote at least the SSE header; an
			// Anthropic-shaped JSON error body would not parse as an SSE
			// event, so the failure is reported as an event instead of a
			// changed status code — same reasoning as chat.go's chatStream.
			h.logger.Error("anthropic messages stream failed", slog.Any("error", err), slog.String("node_id", candidate.NodeID))
			_ = writeAnthropicSSE(w, "error", anthropicStreamErrorEvent{
				Type:  "error",
				Error: anthropicErrorJSON{Type: "api_error", Message: "the stream ended with an error"},
			})
			flusher.Flush()
			return
		}

		if ev.Usage != nil {
			usage = *ev.Usage
		}
		if ev.FinishReason != "" {
			stopReason = anthropicStopReason(ev.FinishReason)
		}
		if ev.Delta.Content == "" {
			continue
		}
		if err := writeAnthropicSSE(w, "content_block_delta", anthropicContentBlockDeltaEvent{
			Type:  "content_block_delta",
			Index: 0,
			Delta: anthropicTextDelta{Type: "text_delta", Text: ev.Delta.Content},
		}); err != nil {
			return
		}
		flusher.Flush()
		if !loggedTTFT {
			loggedTTFT = true
			h.logTTFT(r, canonical.Model, candidate.NodeID, start, true)
			h.metrics.TTFT(EndpointAnthropicMessages, time.Since(start))
		}
	}
}

// anthropic.go implements Anthropic's POST /v1/messages, translated at the
// boundary into the same canonical chat request every other front door
// produces (STATUS.md's P2 "Anthropic Messages 前门" — see
// docs/superpowers/specs/2026-09-16-p2-api-compat-boundary-design.md §四).
// Text, image, tool_use and tool_result content blocks are all supported,
// reusing runtime.ChatMessage's existing ToolCalls/ToolCallID fields (the
// same ones the OpenAI front door in chat.go already populates) rather than
// any Anthropic-specific core type — a "tool_use" block becomes a
// runtime.ToolCall on an assistant message, a "tool_result" block becomes
// its own synthetic "tool"-role message, matching every other front door's
// shape. Any other content block type (document, thinking, …) is still
// refused by name rather than silently dropped, matching responses.go's
// "unsupported 字段" pattern.
//
// anthropic.go 实现 POST /v1/messages，在边界处转换成与其他每个前门相同的
// canonical 聊天请求（STATUS.md 的「Anthropic Messages 前门」——见设计文档
// §四）。文本、图片、tool_use 与 tool_result 内容块均已支持，复用
// runtime.ChatMessage 既有的 ToolCalls/ToolCallID 字段（与 chat.go 里 OpenAI
// 前门填的是同一对字段）而非任何 Anthropic 专属核心类型——一个 "tool_use" 块
// 变成 assistant 消息上的一次 runtime.ToolCall，一个 "tool_result" 块变成一条
// 独立的 "tool" 角色合成消息，与其他每个前门的形状一致。其他任何内容块类型
// （document、thinking……）仍按名字拒绝而非静默丢弃，与 responses.go 的
// 「unsupported 字段」模式一致。
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
// serves.
type anthropicMessagesRequest struct {
	Model         string                 `json:"model"`
	Messages      []anthropicMessageJSON `json:"messages"`
	System        json.RawMessage        `json:"system,omitempty"`
	MaxTokens     int                    `json:"max_tokens"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
	Tools         []anthropicToolJSON    `json:"tools,omitempty"`
	ToolChoice    json.RawMessage        `json:"tool_choice,omitempty"`
}

// anthropicToolJSON is one element of "tools": Anthropic names a tool
// directly (no OpenAI-style {"type":"function","function":{...}} wrapper),
// so it maps straight onto runtime.FunctionDefinition.
type anthropicToolJSON struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// toRuntimeTools converts Anthropic's flat tool list into runtime.Tool,
// always tagging Type "function" — Anthropic has only the one tool shape,
// unlike OpenAI's wire format which names it explicitly.
func toRuntimeTools(tools []anthropicToolJSON) []runtime.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]runtime.Tool, len(tools))
	for i, t := range tools {
		out[i] = runtime.Tool{
			Type: "function",
			Function: runtime.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}
	return out
}

// anthropicToolChoiceJSON is the "tool_choice" object shape.
// DisableParallelToolUse has no analogous per-request knob today (runtime.
// ChatRequest.ToolChoice is a single string) and is silently ignored by
// anthropicToolChoice — the same treatment ollama.go gives "keep_alive" for
// a parameter this Gateway has nowhere to apply.
type anthropicToolChoiceJSON struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

// anthropicToolChoice reduces Anthropic's tool_choice object to the plain
// string runtime.ChatRequest.ToolChoice carries, mirroring chat.go's
// decodeToolChoice for the OpenAI front door: "auto"/"none" pass through
// unchanged, "any" (Anthropic's "call some tool" mode) maps onto "required"
// — the closest existing vocabulary entry — and "tool" (name one specific
// tool) is re-encoded as the OpenAI named-tool-choice object shape every
// backend adapter already expects on that string field.
func anthropicToolChoice(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var c anthropicToolChoiceJSON
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", errors.New(`"tool_choice" must be an object`)
	}
	switch c.Type {
	case "auto":
		return "auto", nil
	case "none":
		return "none", nil
	case "any":
		return "required", nil
	case "tool":
		if c.Name == "" {
			return "", errors.New(`tool_choice of type "tool" requires "name"`)
		}
		encoded, err := json.Marshal(struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}{Type: "function", Function: struct {
			Name string `json:"name"`
		}{Name: c.Name}})
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	default:
		return "", fmt.Errorf("tool_choice type %q is not supported", c.Type)
	}
}

type anthropicMessageJSON struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// anthropicContentBlockJSON is one element of a "content" array: "text",
// "image", "document", "tool_use" and "tool_result" are the block types
// accepted on input; anthropicMessageToRuntime rejects any other type by
// name. The same struct doubles as the output content block shape, where
// Type is "text" or "tool_use" — a response is never constructed with an
// image, document or tool_result block.
type anthropicContentBlockJSON struct {
	Type   string                    `json:"type"`
	Text   string                    `json:"text,omitempty"`
	Source *anthropicImageSourceJSON `json:"source,omitempty"`
	// Title is a "document" block's optional caption, carried through as
	// runtime.ContentFile.Filename; images carry no equivalent field.
	//
	// Title 是 "document" 块的可选说明文字，透传为 runtime.ContentFile.Filename；
	// 图片没有对应的字段。
	Title string `json:"title,omitempty"`

	// ID, Name and Input are set on a "tool_use" block (both directions):
	// input carries the assistant's arguments as a JSON object, mirroring
	// runtime.FunctionCall.Arguments's string form via json.RawMessage.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// ToolUseID and Content are set on an input-only "tool_result" block:
	// ToolUseID names the tool_use this result answers (becomes
	// runtime.ChatMessage.ToolCallID), Content is that result's own content
	// — a string or an array of content blocks, deferred here as raw JSON
	// and reduced by anthropicToolResultContent.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

// anthropicImageSourceJSON is an "image" or "document" content block's
// source. Anthropic defines two shapes for both block types: inline base64
// bytes ("base64", with media_type and data) and a fetchable URL ("url").
// Both translate to a single URL string — see toURL.
type anthropicImageSourceJSON struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// toURL reduces an Anthropic image or document source to the single URL
// string runtime.ContentImageURL.URL / runtime.ContentFile.URL carries: a
// base64 source becomes a data: URI (the same inline form OpenAI's own
// image_url.url accepts), a url source passes through unchanged. blockType
// names the calling content block ("image" or "document") for its error
// messages.
func (s *anthropicImageSourceJSON) toURL(blockType string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("an %q content block requires \"source\"", blockType)
	}
	switch s.Type {
	case "base64":
		if s.MediaType == "" || s.Data == "" {
			return "", errors.New(`source of type "base64" requires "media_type" and "data"`)
		}
		return "data:" + s.MediaType + ";base64," + s.Data, nil
	case "url":
		if s.URL == "" {
			return "", errors.New(`source of type "url" requires "url"`)
		}
		return s.URL, nil
	default:
		return "", fmt.Errorf("source type %q is not supported", s.Type)
	}
}

// anthropicText reduces an Anthropic "content" field — a bare string or an
// array of content blocks — to the plain text runtime.ChatMessage.Content
// carries. Any non-"text" block is rejected by name. It is used only for
// "system", which Anthropic defines as text-only — never images — so it
// keeps its own narrower contract rather than reusing
// anthropicMessageContent's image handling for a field that can never carry
// one.
//
// anthropicText 把 Anthropic 的 "content" 字段——一个裸字符串或一个内容块
// 数组——归约成 runtime.ChatMessage.Content 承载的纯文本。任何非 "text" 的块
// 都按名字拒绝。它只用于 "system"——Anthropic 把它定义为纯文本、绝不含图片
// ——因此保留自己更窄的契约，而不是为一个永远不会携带图片的字段复用
// anthropicMessageContent 的图片处理逻辑。
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
			return "", fmt.Errorf("content block type %q is not supported for \"system\"", b.Type)
		}
		sb.WriteString(b.Text)
	}
	return sb.String(), nil
}

// anthropicToolResultContent reduces a "tool_result" block's own "content"
// field — a bare string or an array of content blocks — to plain text, the
// same text runtime.ChatMessage.Content carries for a "tool"-role message
// on every other front door. Anthropic allows image blocks inside a
// tool_result too, but no other front door's tool-role message carries
// ContentParts today, so that combination is rejected by name here rather
// than silently accepted and then dropped somewhere downstream.
//
// anthropicToolResultContent 把一个 "tool_result" 块自己的 "content" 字段
// ——裸字符串或内容块数组——归约成纯文本，与其他每个前门的 "tool" 角色消息所
// 承载的 runtime.ChatMessage.Content 是同一种文本。Anthropic 也允许
// tool_result 内嵌图片块，但目前没有任何前门的 tool 角色消息携带
// ContentParts，因此这种组合在此按名字拒绝，而不是悄悄接受、之后又在下游
// 被丢弃。
func anthropicToolResultContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var blocks []anthropicContentBlockJSON
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", errors.New(`"content" of a tool_result block must be a string or an array of content blocks`)
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type != "text" {
			return "", fmt.Errorf("tool_result content block type %q is not supported", b.Type)
		}
		sb.WriteString(b.Text)
	}
	return sb.String(), nil
}

// anthropicContentRun converts one uninterrupted run of content blocks
// (never containing a "tool_result" — anthropicMessageToRuntime splits on
// those before calling this) into a single canonical message's
// Content/ContentParts/ToolCalls. A run with no "image" or "document" block
// collapses its "text" blocks into one concatenated string, matching
// anthropicText's plain-text behavior; a run containing at least one
// "image" or "document" instead renders every text/image/document block as
// its own ContentPart, in order — the same two-mode split
// anthropicMessageContent used before tool blocks existed. "tool_use"
// blocks accumulate into ToolCalls regardless of mode, since Anthropic
// freely mixes a text block and one or more tool_use blocks in the same
// assistant turn, exactly like an OpenAI assistant message carrying both
// Content and ToolCalls.
func anthropicContentRun(blocks []anthropicContentBlockJSON) (text string, parts []runtime.ContentPart, calls []runtime.ToolCall, err error) {
	hasMultimodal := false
	for _, b := range blocks {
		if b.Type == "image" || b.Type == "document" {
			hasMultimodal = true
			break
		}
	}
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if hasMultimodal {
				parts = append(parts, runtime.ContentPart{Type: "text", Text: b.Text})
			} else {
				sb.WriteString(b.Text)
			}
		case "image":
			url, err := b.Source.toURL("image")
			if err != nil {
				return "", nil, nil, err
			}
			parts = append(parts, runtime.ContentPart{Type: "image_url", ImageURL: &runtime.ContentImageURL{URL: url}})
		case "document":
			url, err := b.Source.toURL("document")
			if err != nil {
				return "", nil, nil, err
			}
			parts = append(parts, runtime.ContentPart{Type: "file", File: &runtime.ContentFile{URL: url, Filename: b.Title}})
		case "tool_use":
			if b.ID == "" || b.Name == "" {
				return "", nil, nil, errors.New(`a "tool_use" content block requires "id" and "name"`)
			}
			args := string(b.Input)
			if args == "" {
				args = "{}"
			}
			calls = append(calls, runtime.ToolCall{
				ID:       b.ID,
				Type:     "function",
				Function: runtime.FunctionCall{Name: b.Name, Arguments: args},
			})
		default:
			return "", nil, nil, fmt.Errorf("content block type %q is not supported by this Anthropic Messages endpoint", b.Type)
		}
	}
	if !hasMultimodal {
		text = sb.String()
	}
	return text, parts, calls, nil
}

// anthropicMessageToRuntime converts one Anthropic input message (its role
// and raw "content" field) into one or more canonical chat messages. A bare
// string, or an array with no "tool_result" block, produces exactly one
// message with that role — the overwhelmingly common case, unchanged from
// before tool support existed. Each "tool_result" block instead produces its
// own synthetic "tool"-role message (ToolCallID + result text): Anthropic
// embeds a tool result as one content block inside a user turn, but every
// other front door's canonical shape wants it as an independent message, so
// this function flushes whatever text/image/tool_use run preceded a
// tool_result as its own message first, preserving the original block
// order across the resulting message slice.
//
// anthropicMessageToRuntime 把一条 Anthropic 输入消息（角色 + 原始 "content"
// 字段）转换成一条或多条 canonical 聊天消息。裸字符串，或不含 "tool_result"
// 块的数组，都只产生一条该角色的消息——这是工具支持存在之前就有的绝大多数
// 情形，未受影响。每个 "tool_result" 块则各自产生一条独立的 "tool" 角色合成
// 消息（ToolCallID + 结果文本）：Anthropic 把工具结果嵌在一个 user 回合的内容
// 块里，但其他每个前门的 canonical 形状都希望它是一条独立消息，因此本函数在
// 遇到 tool_result 之前，先把此前那段文本/图片/tool_use 的连续片段整理成它
// 自己的一条消息，从而在产生的消息切片里保留原始块顺序。
func anthropicMessageToRuntime(role string, raw json.RawMessage) ([]runtime.ChatMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil
		}
		return []runtime.ChatMessage{{Role: role, Content: s}}, nil
	}
	var blocks []anthropicContentBlockJSON
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, errors.New("content must be a string or an array of content blocks")
	}

	var out []runtime.ChatMessage
	var run []anthropicContentBlockJSON
	flush := func() error {
		if len(run) == 0 {
			return nil
		}
		text, parts, calls, err := anthropicContentRun(run)
		if err != nil {
			return err
		}
		if text != "" || len(parts) > 0 || len(calls) > 0 {
			out = append(out, runtime.ChatMessage{Role: role, Content: text, ContentParts: parts, ToolCalls: calls})
		}
		run = nil
		return nil
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			run = append(run, b)
			continue
		}
		if err := flush(); err != nil {
			return nil, err
		}
		if b.ToolUseID == "" {
			return nil, errors.New(`a "tool_result" content block requires "tool_use_id"`)
		}
		resultText, err := anthropicToolResultContent(b.Content)
		if err != nil {
			return nil, err
		}
		out = append(out, runtime.ChatMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: resultText})
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// toRuntime converts req into the canonical chat request every front door
// produces. It rejects any message role other than "user"/"assistant" by
// name; anthropicMessageToRuntime rejects unsupported content block types
// the same way.
func (req anthropicMessagesRequest) toRuntime() (runtime.ChatRequest, error) {
	choice, err := anthropicToolChoice(req.ToolChoice)
	if err != nil {
		return runtime.ChatRequest{}, err
	}

	maxTokens := req.MaxTokens
	out := runtime.ChatRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   &maxTokens,
		Stop:        req.StopSequences,
		Tools:       toRuntimeTools(req.Tools),
		ToolChoice:  choice,
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
		msgs, err := anthropicMessageToRuntime(m.Role, m.Content)
		if err != nil {
			return runtime.ChatRequest{}, fmt.Errorf("messages[%d]: %w", i, err)
		}
		out.Messages = append(out.Messages, msgs...)
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

// anthropicResponseContentBlocks renders a Chat response message's text and
// tool calls as Anthropic content blocks: a leading "text" block only when
// there is text to carry (Anthropic never emits an empty one when a
// response is tool_use-only), then one "tool_use" block per
// runtime.ToolCall, in order. A response with neither — an edge case no
// real backend should produce — still renders one empty text block so
// Content is never an empty array.
func anthropicResponseContentBlocks(msg runtime.ChatMessage) []anthropicContentBlockJSON {
	var blocks []anthropicContentBlockJSON
	if msg.Content != "" {
		blocks = append(blocks, anthropicContentBlockJSON{Type: "text", Text: msg.Content})
	}
	for _, c := range msg.ToolCalls {
		input := json.RawMessage(c.Function.Arguments)
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, anthropicContentBlockJSON{
			Type:  "tool_use",
			ID:    c.ID,
			Name:  c.Function.Name,
			Input: input,
		})
	}
	if len(blocks) == 0 {
		blocks = []anthropicContentBlockJSON{{Type: "text", Text: ""}}
	}
	return blocks
}

// anthropicStopReason maps this Gateway's backend-opaque finish reason
// (OpenAI-shaped: "stop", "length", "tool_calls", …) onto Anthropic's
// closed stop_reason vocabulary. An unrecognized or empty reason maps to
// "end_turn" — the value a client is least likely to branch on
// defensively, since it is what a normal completion reports.
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
		Content:    anthropicResponseContentBlocks(resp.Message),
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

// anthropicInputJSONDelta is a tool_use content block's streaming delta:
// PartialJSON is one fragment of the arguments object's JSON text, to be
// concatenated (never separately parsed) across every delta an SDK sees for
// that block's index — the streaming equivalent of runtime.ToolCallDelta's
// own FunctionCallDelta.Arguments fragment.
type anthropicInputJSONDelta struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
}

// anthropicContentBlockDeltaEvent's Delta is anthropicTextDelta for the text
// block (index 0) or anthropicInputJSONDelta for a tool_use block — the two
// delta shapes Anthropic's own wire protocol defines.
type anthropicContentBlockDeltaEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta any    `json:"delta"`
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

	// Tool-call streaming state. Anthropic's content blocks are strictly
	// sequential — one open at a time, each started, deltared and stopped
	// before the next starts — while runtime.ToolCallDelta.Index (mirroring
	// OpenAI's own streaming shape) can only be trusted to group deltas
	// belonging to the same call, not to promise they arrive contiguously.
	// In practice every OpenAI-compatible backend this Gateway talks to
	// streams one call to completion before starting the next, so this loop
	// assumes that ordering rather than buffering to reorder around it —
	// consistent with the rest of this codebase never buffering a stream to
	// reshape it. textClosed tracks whether the leading text block (index 0,
	// already started above) has been stopped; toolBlockIndex maps an
	// incoming ToolCallDelta.Index to the Anthropic content block index
	// assigned to it; openToolIndex names whichever tool_use block is
	// currently open, so it can be stopped before the next one starts or at
	// EOF.
	textClosed := false
	toolBlockIndex := map[int]int{}
	nextBlockIndex := 1
	openToolIndex := -1
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if openToolIndex >= 0 {
				_ = writeAnthropicSSE(w, "content_block_stop", anthropicContentBlockStopEvent{Type: "content_block_stop", Index: openToolIndex})
			} else if !textClosed {
				_ = writeAnthropicSSE(w, "content_block_stop", anthropicContentBlockStopEvent{Type: "content_block_stop", Index: 0})
			}
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

		for _, td := range ev.Delta.ToolCalls {
			if !textClosed {
				_ = writeAnthropicSSE(w, "content_block_stop", anthropicContentBlockStopEvent{Type: "content_block_stop", Index: 0})
				textClosed = true
			}
			idx, seen := toolBlockIndex[td.Index]
			if !seen {
				if openToolIndex >= 0 {
					_ = writeAnthropicSSE(w, "content_block_stop", anthropicContentBlockStopEvent{Type: "content_block_stop", Index: openToolIndex})
				}
				idx = nextBlockIndex
				nextBlockIndex++
				toolBlockIndex[td.Index] = idx
				openToolIndex = idx
				input := json.RawMessage("{}")
				_ = writeAnthropicSSE(w, "content_block_start", anthropicContentBlockStartEvent{
					Type:  "content_block_start",
					Index: idx,
					ContentBlock: anthropicContentBlockJSON{
						Type: "tool_use", ID: td.ID, Name: td.Function.Name, Input: input,
					},
				})
			}
			if td.Function.Arguments != "" {
				if err := writeAnthropicSSE(w, "content_block_delta", anthropicContentBlockDeltaEvent{
					Type:  "content_block_delta",
					Index: idx,
					Delta: anthropicInputJSONDelta{Type: "input_json_delta", PartialJSON: td.Function.Arguments},
				}); err != nil {
					return
				}
			}
			flusher.Flush()
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

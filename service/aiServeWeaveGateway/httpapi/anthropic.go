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
// shape. A response's reasoning is rendered as leading "thinking" (and
// "redacted_thinking") blocks, signed when the backend signed them. Signed
// blocks a client echoes back on an assistant turn are kept for the backend
// that signed them to replay; unsigned ones are dropped. The "thinking"
// request parameter becomes runtime.ChatRequest.Reasoning. Any other content
// block type is still refused by name rather than silently dropped, matching
// responses.go's "unsupported 字段" pattern.
//
// anthropic.go 实现 POST /v1/messages，在边界处转换成与其他每个前门相同的
// canonical 聊天请求（STATUS.md 的「Anthropic Messages 前门」——见设计文档
// §四）。文本、图片、tool_use 与 tool_result 内容块均已支持，复用
// runtime.ChatMessage 既有的 ToolCalls/ToolCallID 字段（与 chat.go 里 OpenAI
// 前门填的是同一对字段）而非任何 Anthropic 专属核心类型——一个 "tool_use" 块
// 变成 assistant 消息上的一次 runtime.ToolCall，一个 "tool_result" 块变成一条
// 独立的 "tool" 角色合成消息，与其他每个前门的形状一致。响应里的思考渲染为打头的
// "thinking"（及 "redacted_thinking"）块，后端签了名时带上签名。客户端在 assistant
// 回合里回传的签名块被保留，交给签名的后端回放；没有签名的被丢弃。请求参数
// "thinking" 转为 runtime.ChatRequest.Reasoning。其他任何内容块类型仍按名字拒绝而非
// 静默丢弃，与 responses.go 的「unsupported 字段」模式一致。
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
	Metadata      json.RawMessage        `json:"metadata,omitempty"`
	MaxTokens     int                    `json:"max_tokens"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
	Tools         []anthropicToolJSON    `json:"tools,omitempty"`
	ToolChoice    json.RawMessage        `json:"tool_choice,omitempty"`
	Thinking      json.RawMessage        `json:"thinking,omitempty"`
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
// accepted on input, plus "thinking" and "redacted_thinking" on an assistant
// turn (see anthropicMessageToRuntime); any other type is rejected by name.
// The same struct doubles as the output content block shape, where Type is
// "thinking", "redacted_thinking", "text" or "tool_use" — a response is
// never constructed with an image, document or tool_result block.
type anthropicContentBlockJSON struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// Thinking and Signature are set on a "thinking" block (both
	// directions). They are pointers because the block requires both fields
	// even when empty. The signature is the backend's own when an Anthropic
	// backend produced the block, and empty otherwise; Data is a
	// "redacted_thinking" block's encrypted payload.
	//
	// Thinking 与 Signature 设置在 "thinking" 块上（两个方向）。用指针是因为这个块即使
	// 为空也要求两个字段都在。块由 Anthropic 后端产出时签名是后端自己的，否则为空；
	// Data 是 "redacted_thinking" 块的加密载荷。
	Thinking  *string `json:"thinking,omitempty"`
	Signature *string `json:"signature,omitempty"`
	Data      string  `json:"data,omitempty"`

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
	var reasoning []runtime.ReasoningBlock
	flush := func() error {
		if len(run) == 0 && len(reasoning) == 0 {
			return nil
		}
		text, parts, calls, err := anthropicContentRun(run)
		if err != nil {
			return err
		}
		if text != "" || len(parts) > 0 || len(calls) > 0 || len(reasoning) > 0 {
			out = append(out, runtime.ChatMessage{Role: role, Content: text, ContentParts: parts, ToolCalls: calls, ReasoningBlocks: reasoning})
		}
		run, reasoning = nil, nil
		return nil
	}
	for _, b := range blocks {
		if role == "assistant" && (b.Type == "thinking" || b.Type == "redacted_thinking") {
			// A signed block is kept whole for the Anthropic backend that
			// signed it, which must get it back within a tool loop; a forged
			// signature is that backend's to reject. An unsigned block is
			// the reasoning this front door rendered for some other backend,
			// and a prior turn's reasoning is not replayed, so it is dropped.
			//
			// 签过名的块被完整保留，交给签名的 Anthropic 后端——它在工具循环中必须收回这些
			// 块；伪造的签名由该后端拒绝。没有签名的块是本前门为其他后端渲染的思考，而上一轮
			// 的思考不回放，因此丢弃。
			if block, ok := anthropicSignedBlock(b); ok {
				reasoning = append(reasoning, block)
			}
			continue
		}
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

// anthropicSignedBlock returns the reasoning block an echoed "thinking" or
// "redacted_thinking" block carries, and whether it is signed and so worth
// replaying.
//
// anthropicSignedBlock 返回回传的 "thinking" 或 "redacted_thinking" 块所携带的思考块，
// 以及它是否签了名、因而值得回放。
func anthropicSignedBlock(b anthropicContentBlockJSON) (runtime.ReasoningBlock, bool) {
	if b.Type == "redacted_thinking" {
		return runtime.ReasoningBlock{Type: runtime.ReasoningBlockRedacted, Data: b.Data}, b.Data != ""
	}
	if b.Signature == nil || *b.Signature == "" {
		return runtime.ReasoningBlock{}, false
	}
	block := runtime.ReasoningBlock{Type: runtime.ReasoningBlockThinking, Signature: *b.Signature}
	if b.Thinking != nil {
		block.Thinking = *b.Thinking
	}
	return block, true
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

	reasoning, err := reasoningFromAnthropic(req.Thinking)
	if err != nil {
		return runtime.ChatRequest{}, err
	}

	maxTokens := req.MaxTokens
	out := runtime.ChatRequest{
		Reasoning:   reasoning,
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

// anthropicResponseContentBlocks renders a Chat response message's
// reasoning, text and tool calls as Anthropic content blocks: leading
// reasoning blocks when there is reasoning — the backend's signed blocks
// as they were when it kept them, otherwise one unsigned "thinking" block —
// a "text" block only when there
// is text to carry (Anthropic never emits an empty one when a response is
// tool_use-only), then one "tool_use" block per runtime.ToolCall, in order.
// A response with neither text nor tool calls — an edge case no real backend
// should produce — still renders one empty text block, so a client always
// finds an answer block.
//
// anthropicResponseContentBlocks 把 Chat 响应消息的思考、文本与工具调用渲染成
// Anthropic 内容块：有思考时打头是思考块——后端保留了签名块时原样输出，否则输出一个
// 不带签名的 "thinking" 块——有文本时才有 "text" 块
// （只含 tool_use 的响应里 Anthropic 从不发空 text 块），随后按顺序每个
// runtime.ToolCall 一个 "tool_use" 块。既无文本也无工具调用的响应——真实后端不该
// 产生的边界情形——依旧渲染一个空 text 块，让客户端总能找到回答块。
func anthropicResponseContentBlocks(msg runtime.ChatMessage) []anthropicContentBlockJSON {
	var blocks []anthropicContentBlockJSON
	switch {
	case len(msg.ReasoningBlocks) > 0:
		for _, r := range msg.ReasoningBlocks {
			if r.Type == runtime.ReasoningBlockRedacted {
				blocks = append(blocks, anthropicContentBlockJSON{Type: "redacted_thinking", Data: r.Data})
				continue
			}
			block := anthropicThinkingBlock(r.Thinking)
			*block.Signature = r.Signature
			blocks = append(blocks, block)
		}
	case msg.ReasoningContent != "":
		blocks = append(blocks, anthropicThinkingBlock(msg.ReasoningContent))
	}
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
	if msg.Content == "" && len(msg.ToolCalls) == 0 {
		blocks = append(blocks, anthropicContentBlockJSON{Type: "text", Text: ""})
	}
	return blocks
}

// anthropicThinkingBlock is a "thinking" content block carrying text and an
// empty signature, for a caller to fill when the backend signed the block.
//
// anthropicThinkingBlock 是一个携带 text、签名为空的 "thinking" 内容块；后端签了名时
// 由调用方填入。
func anthropicThinkingBlock(text string) anthropicContentBlockJSON {
	signature := ""
	return anthropicContentBlockJSON{Type: "thinking", Thinking: &text, Signature: &signature}
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
	var raw json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	if err := decoder.Decode(&raw); err != nil || decoder.Decode(new(any)) != io.EOF || json.Unmarshal(raw, &req) != nil {
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
	if h.useNativeMessages(req) {
		h.nativeMessages(w, r, req, raw, start)
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
	writeOutputLimitWarning(w, candidate, "max_tokens")

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

// anthropicThinkingDelta is a thinking content block's streaming delta.
// anthropicThinkingDelta 是 thinking 内容块的流式增量。
type anthropicThinkingDelta struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

// anthropicSignatureDelta carries a thinking block's signature, sent last
// before the block stops.
// anthropicSignatureDelta 携带 thinking 块的签名，在块结束前最后发送。
type anthropicSignatureDelta struct {
	Type      string `json:"type"`
	Signature string `json:"signature"`
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

// anthropicContentBlockDeltaEvent's Delta is anthropicThinkingDelta for a
// thinking block, anthropicTextDelta for a text block, or
// anthropicInputJSONDelta for a tool_use block — delta shapes Anthropic's own
// wire protocol defines.
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
// message_delta/message_stop sequence. message_start is written before the
// first Recv, as Anthropic's own protocol always opens with it; content blocks
// are opened as their content arrives, by anthropicBlocks, because a thinking
// block has to come before the text block and only the backend knows whether
// there will be one.
//
// anthropicMessagesStream 服务带 stream:true 的 POST /v1/messages，把
// runtime.ChatEvent 转换成 Anthropic 的
// message_start/content_block_start/content_block_delta/content_block_stop/
// message_delta/message_stop 序列。message_start 在第一次 Recv 之前写出，与
// Anthropic 自己的协议总以它开场一致；内容块则由 anthropicBlocks 随内容到达再打开，
// 因为 thinking 块必须排在 text 块之前，而会不会有 thinking 块只有后端知道。
func (h *handlers) anthropicMessagesStream(w http.ResponseWriter, r *http.Request, canonical runtime.ChatRequest, start time.Time) {
	stream, candidate, err := h.sched.ChatStream(r.Context(), canonical)
	if err != nil {
		handleAnthropicDispatchError(w, h.logger, err)
		return
	}
	defer stream.Close()
	writeOutputLimitWarning(w, candidate, "max_tokens")

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
	flusher.Flush()

	loggedTTFT := false
	stopReason := ""
	// The last usage the backend reported wins, same reasoning as chat.go's
	// chatStream: OpenAI-shaped backends restate a running total rather than
	// adding to it.
	var usage runtime.Usage
	blocks := newAnthropicBlocks(w)
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			blocks.finish()
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

		// Within one event the order is thinking, text, tool calls: the order
		// a model produces them in, so a backend that packs several into one
		// event does not get its text split around a tool call.
		//
		// 同一事件内按 thinking、文本、工具调用的顺序处理：这正是模型产出它们的顺序，
		// 因此把几样打包进同一事件的后端不会让文本被工具调用拆开。
		if ev.Delta.ReasoningContent != "" {
			if err := blocks.thinking(ev.Delta.ReasoningContent); err != nil {
				return
			}
		}
		if ev.Delta.ReasoningBlock != nil {
			if err := blocks.signed(*ev.Delta.ReasoningBlock); err != nil {
				return
			}
		}
		if ev.Delta.Content != "" {
			if err := blocks.text(ev.Delta.Content); err != nil {
				return
			}
		}
		for _, td := range ev.Delta.ToolCalls {
			if err := blocks.toolCall(td); err != nil {
				return
			}
		}
		flusher.Flush()
		if !loggedTTFT && (ev.Delta.ReasoningContent != "" || ev.Delta.Content != "") {
			loggedTTFT = true
			h.logTTFT(r, canonical.Model, candidate.NodeID, start, true)
			h.metrics.TTFT(EndpointAnthropicMessages, time.Since(start))
		}
	}
}

// anthropicBlocks sequences Anthropic content blocks over one stream.
// Anthropic's blocks are strictly sequential — one open at a time, each
// started, deltaed and stopped before the next starts — so a change of kind
// stops the open block and starts a new one at the next index.
//
// A text block always precedes the first tool_use block and is always
// present by the end, even if empty: that is the shape this front door has
// produced since before thinking existed, and a client that indexes blocks
// by position keeps working.
//
// runtime.ToolCallDelta.Index (mirroring OpenAI's own streaming shape) can
// only be trusted to group deltas belonging to the same call, not to promise
// they arrive contiguously. In practice every OpenAI-compatible backend this
// Gateway talks to streams one call to completion before starting the next,
// so this assumes that ordering rather than buffering to reorder around it —
// consistent with the rest of this codebase never buffering a stream to
// reshape it.
//
// anthropicBlocks 在一条流上给 Anthropic 内容块排序。Anthropic 的块严格顺序——同一
// 时间只开一个，每个都先 start、delta、stop，下一个才开始——因此种类一变，就停掉
// 打开的块，在下一个下标开新块。
//
// 第一个 tool_use 块之前总有一个 text 块，流结束时也必定至少有一个 text 块（哪怕为
// 空）：这是本前门在 thinking 出现之前就一直产出的形状，按位置取块的客户端照常工作。
//
// runtime.ToolCallDelta.Index（对应 OpenAI 自己的流式形状）只能用来把属于同一调用的
// delta 归到一起，不能保证它们连续到达。实际上本 Gateway 对接的每个 OpenAI 兼容后端
// 都是一个调用发完再发下一个，因此这里假定这种顺序，而不是缓冲重排——与本仓库从不为
// 改形而缓冲流的做法一致。
type anthropicBlocks struct {
	w           io.Writer
	next        int         // index the next block gets / 下一个块的下标
	open        int         // index of the open block, -1 when none / 打开的块的下标，没有时为 -1
	openType    string      // "thinking", "text" or "tool_use" / 打开的块的类型
	textStarted bool        // whether a text block has been started / 是否已开过 text 块
	tools       map[int]int // ToolCallDelta.Index -> block index / 工具调用下标 -> 块下标
}

func newAnthropicBlocks(w io.Writer) *anthropicBlocks {
	return &anthropicBlocks{w: w, open: -1, tools: map[int]int{}}
}

// start stops the open block, if any, and starts block at the next index.
//
// start 停掉打开的块（如果有），在下一个下标开始 block。
func (b *anthropicBlocks) start(block anthropicContentBlockJSON) {
	b.stop()
	b.open, b.openType = b.next, block.Type
	b.next++
	if block.Type == "text" {
		b.textStarted = true
	}
	_ = writeAnthropicSSE(b.w, "content_block_start", anthropicContentBlockStartEvent{
		Type: "content_block_start", Index: b.open, ContentBlock: block,
	})
}

// stop stops the open block, if any.
//
// stop 停掉打开的块（如果有）。
func (b *anthropicBlocks) stop() {
	if b.open < 0 {
		return
	}
	_ = writeAnthropicSSE(b.w, "content_block_stop", anthropicContentBlockStopEvent{Type: "content_block_stop", Index: b.open})
	b.open, b.openType = -1, ""
}

func (b *anthropicBlocks) delta(index int, delta any) error {
	return writeAnthropicSSE(b.w, "content_block_delta", anthropicContentBlockDeltaEvent{
		Type: "content_block_delta", Index: index, Delta: delta,
	})
}

// thinking writes a reasoning fragment, opening a thinking block if one is
// not already open.
//
// thinking 写出一段思考片段；没有打开的 thinking 块时先打开一个。
func (b *anthropicBlocks) thinking(text string) error {
	if b.openType != "thinking" {
		b.start(anthropicThinkingBlock(""))
	}
	return b.delta(b.open, anthropicThinkingDelta{Type: "thinking_delta", Thinking: text})
}

// signed closes a signed reasoning block: a thinking block's signature goes
// into the open thinking block (one is opened if the signature arrived with
// no text), which then stops, so later reasoning starts a new block; a
// redacted block is written whole.
//
// signed 结束一个签过名的思考块：thinking 块的签名写入打开的 thinking 块（签名到达时
// 没有文本就先开一个），随后该块结束，之后的思考会开新块；redacted 块整块写出。
func (b *anthropicBlocks) signed(r runtime.ReasoningBlock) error {
	if r.Type == runtime.ReasoningBlockRedacted {
		b.start(anthropicContentBlockJSON{Type: "redacted_thinking", Data: r.Data})
		b.stop()
		return nil
	}
	if b.openType != "thinking" {
		b.start(anthropicThinkingBlock(""))
	}
	err := b.delta(b.open, anthropicSignatureDelta{Type: "signature_delta", Signature: r.Signature})
	b.stop()
	return err
}

// text writes a text fragment, opening a text block if one is not already
// open.
//
// text 写出一段文本片段；没有打开的 text 块时先打开一个。
func (b *anthropicBlocks) text(text string) error {
	if b.openType != "text" {
		b.start(anthropicContentBlockJSON{Type: "text", Text: ""})
	}
	return b.delta(b.open, anthropicTextDelta{Type: "text_delta", Text: text})
}

// toolCall writes one tool-call delta, starting a tool_use block the first
// time its index is seen.
//
// toolCall 写出一个工具调用 delta；某个下标第一次出现时开始一个 tool_use 块。
func (b *anthropicBlocks) toolCall(td runtime.ToolCallDelta) error {
	if !b.textStarted {
		b.start(anthropicContentBlockJSON{Type: "text", Text: ""})
	}
	idx, seen := b.tools[td.Index]
	if !seen {
		b.start(anthropicContentBlockJSON{
			Type: "tool_use", ID: td.ID, Name: td.Function.Name, Input: json.RawMessage("{}"),
		})
		idx = b.open
		b.tools[td.Index] = idx
	}
	if td.Function.Arguments == "" {
		return nil
	}
	return b.delta(idx, anthropicInputJSONDelta{Type: "input_json_delta", PartialJSON: td.Function.Arguments})
}

// finish makes sure a text block exists and stops the open block.
//
// finish 确保存在一个 text 块，并停掉打开的块。
func (b *anthropicBlocks) finish() {
	if !b.textStarted {
		b.start(anthropicContentBlockJSON{Type: "text", Text: ""})
	}
	b.stop()
}

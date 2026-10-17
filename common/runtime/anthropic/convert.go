package anthropic

import (
	"encoding/json"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
)

// defaultMaxTokens is sent when the caller set no output limit, because
// Messages requires max_tokens.
// defaultMaxTokens 在调用方未设置输出上限时发送，因为 Messages 要求必须提供
// max_tokens。
const defaultMaxTokens = 4096

// modeledFields are the request fields this package writes; ChatRequest.Extra
// may add other top-level fields but never replace one of these.
// modeledFields 是本包写入的请求字段；ChatRequest.Extra 可以追加其他顶层字段，
// 但绝不能替换其中任何一个。
var modeledFields = map[string]bool{"model": true, "max_tokens": true, "system": true, "messages": true, "temperature": true,
	"top_p": true, "stop_sequences": true, "tools": true, "tool_choice": true, "stream": true}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    *imageSource    `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type messagesRequest struct {
	Model         string      `json:"model"`
	MaxTokens     int         `json:"max_tokens"`
	System        string      `json:"system,omitempty"`
	Messages      []message   `json:"messages"`
	Temperature   *float64    `json:"temperature,omitempty"`
	TopP          *float64    `json:"top_p,omitempty"`
	StopSequences []string    `json:"stop_sequences,omitempty"`
	Tools         []tool      `json:"tools,omitempty"`
	ToolChoice    *toolChoice `json:"tool_choice,omitempty"`
	Stream        bool        `json:"stream,omitempty"`
}

// errRequest marks a ChatRequest that cannot be expressed as Messages.
// errRequest 标记无法表达为 Messages 的 ChatRequest。
type errRequest string

func (e errRequest) Error() string { return "anthropic: " + string(e) }

// buildRequest converts a protocol-neutral chat request into a Messages
// document: system and developer text become the top-level system prompt,
// tool results become user tool_result blocks, and consecutive messages of
// the same role are merged because Messages requires alternation.
//
// buildRequest 把协议无关的聊天请求转换为 Messages 文档：system 与 developer
// 文本成为顶层 system 提示词，工具结果成为 user 角色的 tool_result 块，相邻的
// 同角色消息被合并，因为 Messages 要求角色交替。
func buildRequest(req runtime.ChatRequest, stream bool) (map[string]json.RawMessage, error) {
	if req.Seed != nil {
		return nil, errRequest("seed is not supported by Anthropic Messages")
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type != "text" {
		return nil, errRequest("structured output is not supported by Anthropic Messages")
	}
	out := messagesRequest{Model: req.Model, MaxTokens: defaultMaxTokens, Temperature: req.Temperature, TopP: req.TopP, StopSequences: req.Stop, Stream: stream}
	if req.MaxTokens != nil {
		if *req.MaxTokens <= 0 {
			return nil, errRequest("max_tokens must be positive")
		}
		out.MaxTokens = *req.MaxTokens
	}
	var system []string
	for _, m := range req.Messages {
		blocks, err := contentBlocks(m)
		if err != nil {
			return nil, err
		}
		role := m.Role
		switch m.Role {
		case "system", "developer":
			for _, b := range blocks {
				if b.Type != "text" {
					return nil, errRequest("system messages may contain text only")
				}
				system = append(system, b.Text)
			}
			continue
		case "user":
		case "assistant":
			for _, call := range m.ToolCalls {
				input := json.RawMessage(call.Function.Arguments)
				if strings.TrimSpace(call.Function.Arguments) == "" {
					input = json.RawMessage(`{}`)
				}
				var obj map[string]any
				if json.Unmarshal(input, &obj) != nil || obj == nil {
					return nil, errRequest("tool call arguments must be a JSON object")
				}
				blocks = append(blocks, block{Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: input})
			}
		case "tool":
			if m.ToolCallID == "" {
				return nil, errRequest("tool message requires tool_call_id")
			}
			var text strings.Builder
			for _, b := range blocks {
				if b.Type != "text" {
					return nil, errRequest("tool results may contain text only")
				}
				text.WriteString(b.Text)
			}
			role, blocks = "user", []block{{Type: "tool_result", ToolUseID: m.ToolCallID, Content: text.String()}}
		default:
			return nil, errRequest("unsupported message role")
		}
		if len(blocks) == 0 {
			continue
		}
		if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == role {
			out.Messages[n-1].Content = append(out.Messages[n-1].Content, blocks...)
		} else {
			out.Messages = append(out.Messages, message{Role: role, Content: blocks})
		}
	}
	if len(out.Messages) == 0 {
		return nil, errRequest("at least one user or assistant message is required")
	}
	out.System = strings.Join(system, "\n\n")
	for _, t := range req.Tools {
		schema := t.Function.Parameters
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, tool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: schema})
	}
	switch req.ToolChoice {
	case "":
	case "auto", "none":
		out.ToolChoice = &toolChoice{Type: req.ToolChoice}
	case "required":
		out.ToolChoice = &toolChoice{Type: "any"}
	default:
		name, err := namedTool(req.ToolChoice)
		if err != nil {
			return nil, err
		}
		out.ToolChoice = &toolChoice{Type: "tool", Name: name}
	}
	return mergeExtra(out, req.Extra)
}

// namedTool reads the tool name from a named-tool choice, which the Gateway
// front doors pass as the OpenAI object {"type":"function","function":{"name":...}}.
// namedTool 从指名工具的选择中读取工具名；Gateway 各前门以 OpenAI 对象
// {"type":"function","function":{"name":...}} 的形式传递它。
func namedTool(choice string) (string, error) {
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal([]byte(choice), &obj) != nil || obj.Type != "function" || obj.Function.Name == "" {
		return "", errRequest("tool_choice must be auto, none, required or a named function")
	}
	return obj.Function.Name, nil
}

// contentBlocks turns a message's text and content parts into blocks.
// Empty text is dropped because Messages rejects empty text blocks.
// contentBlocks 把消息的文本与内容片段转换为块。空文本被丢弃，因为 Messages
// 拒绝空文本块。
func contentBlocks(m runtime.ChatMessage) ([]block, error) {
	var blocks []block
	if m.Content != "" {
		blocks = append(blocks, block{Type: "text", Text: m.Content})
	}
	for _, part := range m.ContentParts {
		switch part.Type {
		case "text":
			if part.Text != "" {
				blocks = append(blocks, block{Type: "text", Text: part.Text})
			}
		case "image_url":
			if part.ImageURL == nil {
				return nil, errRequest("image_url part requires a URL")
			}
			src, err := imageFromURL(part.ImageURL.URL)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block{Type: "image", Source: src})
		default:
			return nil, errRequest("content part type is not supported")
		}
	}
	return blocks, nil
}

// imageFromURL accepts a base64 data URL or an http(s) URL.
// imageFromURL 接受 base64 data URL 或 http(s) URL。
func imageFromURL(raw string) (*imageSource, error) {
	if rest, ok := strings.CutPrefix(raw, "data:"); ok {
		meta, data, found := strings.Cut(rest, ",")
		mediaType, isBase64 := strings.CutSuffix(meta, ";base64")
		if !found || !isBase64 || data == "" {
			return nil, errRequest("image data URL must be base64-encoded")
		}
		switch mediaType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return nil, errRequest("image media type must be png, jpeg, gif or webp")
		}
		return &imageSource{Type: "base64", MediaType: mediaType, Data: data}, nil
	}
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
		return &imageSource{Type: "url", URL: raw}, nil
	}
	return nil, errRequest("image URL must be a data URL or http(s) URL")
}

func mergeExtra(req messagesRequest, extra map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, errRequest("encode request")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, errRequest("encode request")
	}
	for key, value := range extra {
		if modeledFields[key] {
			return nil, errRequest("an extra field collides with a modeled field")
		}
		body[key] = value
	}
	return body, nil
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// prompt counts every input token, cached or not, as OpenAI's prompt_tokens does.
// prompt 统计全部输入 token（无论是否命中缓存），与 OpenAI 的 prompt_tokens 一致。
func (u usage) prompt() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

type responseBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type messagesResponse struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Content    []responseBlock `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      usage           `json:"usage"`
}

// finishReason maps a Messages stop_reason onto the OpenAI vocabulary.
// finishReason 把 Messages 的 stop_reason 映射到 OpenAI 的取值。
func finishReason(stop string) string {
	switch stop {
	case "":
		return ""
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	}
	return "stop"
}

// toChatResponse keeps text and tool_use blocks; thinking blocks are not
// part of the OpenAI message and are dropped.
// toChatResponse 保留文本与 tool_use 块；thinking 块不属于 OpenAI 消息，被丢弃。
func toChatResponse(resp messagesResponse, createdAt time.Time) runtime.ChatResponse {
	out := runtime.ChatResponse{CreatedAt: createdAt}
	out.ID, out.Model, out.FinishReason = resp.ID, resp.Model, finishReason(resp.StopReason)
	out.Message.Role = "assistant"
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			out.Message.Content += b.Text
		case "tool_use":
			args := string(b.Input)
			if args == "" {
				args = "{}"
			}
			out.Message.ToolCalls = append(out.Message.ToolCalls, runtime.ToolCall{ID: b.ID, Type: "function", Function: runtime.FunctionCall{Name: b.Name, Arguments: args}})
		}
	}
	out.Usage = runtime.Usage{PromptTokens: resp.Usage.prompt(), CompletionTokens: resp.Usage.OutputTokens, TotalTokens: resp.Usage.prompt() + resp.Usage.OutputTokens}
	return out
}

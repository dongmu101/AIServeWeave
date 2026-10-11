package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
)

func (h *handlers) useNativeChat(req chatCompletionRequest) bool {
	for i := len(req.Messages) - 1; i >= 0 && req.Messages[i].Role == "tool"; i-- {
		if strings.HasPrefix(req.Messages[i].ToolCallID, "toolu_aisw_") {
			return true
		}
	}
	return len(h.sched.MessagesCandidates(req.Model)) > 0
}

// nativeChat reuses authenticated Messages coordination without a second HTTP hop.
// nativeChat 复用已认证的 Messages 会话协调，不引入第二次 HTTP 请求。
func (h *handlers) nativeChat(w http.ResponseWriter, r *http.Request, req chatCompletionRequest, raw json.RawMessage, start time.Time) {
	if len(raw) > 1<<20 {
		writeOpenAIError(w, 413, "invalid_request_error", "request_too_large", "Claude CLI Chat request size limit exceeded")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var options struct {
		chatCompletionRequest
		Store *bool `json:"store,omitempty"`
	}
	if err := decoder.Decode(&options); err != nil {
		message := "unsupported Claude CLI Chat parameter"
		// Name recognized API fields only; arbitrary keys may contain sensitive input.
		// 仅点名已知 API 字段，任意字段名可能包含敏感输入。
		switch err.Error() {
		case `json: unknown field "strict"`:
			message = "Claude CLI Chat does not support tools[].function.strict"
		case `json: unknown field "parallel_tool_calls"`:
			message = "Claude CLI Chat does not support parallel_tool_calls"
		case `json: unknown field "reasoning_effort"`:
			message = "Claude CLI Chat does not support reasoning_effort"
		case `json: unknown field "prompt_cache_key"`, `json: unknown field "prompt_cache_retention"`:
			message = "Claude CLI Chat does not support prompt_cache_key or prompt_cache_retention"
		}
		writeOpenAIError(w, 400, "invalid_request_error", "unsupported_parameter", message)
		return
	}
	if options.Store != nil && *options.Store {
		writeOpenAIError(w, 400, "invalid_request_error", "unsupported_parameter", "Claude CLI Chat supports only store=false; persistent Chat storage is unavailable")
		return
	}
	req = options.chatCompletionRequest
	native, err := req.toNativeMessages()
	if err != nil {
		writeOpenAIError(w, 400, "invalid_request_error", "unsupported_parameter", err.Error())
		return
	}
	document, _ := json.Marshal(native)
	output := &nativeChatOutput{w: w, model: req.Model, created: start.Unix(), toolIndexes: make(map[int]int), toolArguments: make(map[int]bool), includeUsage: req.StreamOptions != nil && req.StreamOptions.IncludeUsage}
	h.nativeMessagesOutput(w, r, native, document, start, output)
}

// toNativeMessages keeps active tool histories exact and folds completed chat history into a text transcript.
// toNativeMessages 保留正在执行的工具历史，将已完成的聊天历史整理成带角色的文本记录。
func (req chatCompletionRequest) toNativeMessages() (anthropicMessagesRequest, error) {
	out := anthropicMessagesRequest{Model: req.Model, Stream: req.Stream, MaxTokens: 4096, Temperature: req.Temperature}
	out.Metadata = json.RawMessage(`{"aisw_chat_completions":true}`)
	if req.MaxTokens != nil && req.MaxCompletionTokens != nil && *req.MaxTokens != *req.MaxCompletionTokens {
		return out, errors.New("max_tokens and max_completion_tokens must agree")
	}
	if req.MaxTokens != nil {
		out.MaxTokens = *req.MaxTokens
	} else if req.MaxCompletionTokens != nil {
		out.MaxTokens = *req.MaxCompletionTokens
	}
	if out.MaxTokens < 1 || out.MaxTokens > 64000 || (req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 1)) {
		return out, errors.New("Claude CLI requires max_tokens in 1..64000 and temperature in 0..1")
	}
	if req.TopP != nil || req.Seed != nil || len(req.Stop) > 0 || req.ResponseFormat != nil {
		return out, errors.New("Claude CLI Chat does not support top_p, seed, stop or response_format")
	}
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != `"auto"` {
		return out, errors.New("Claude CLI Chat supports only automatic tool choice")
	}
	for _, tool := range req.Tools {
		if tool.Type != "function" {
			return out, errors.New("Claude CLI Chat supports only function tools")
		}
		schema := tool.Function.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, anthropicToolJSON{Name: tool.Function.Name, Description: tool.Function.Description, InputSchema: schema})
	}
	var system []string
	var history []chatMessageJSON
	latestUser := -1
	for _, message := range req.Messages {
		if len(message.Content.Parts) > 0 {
			return out, errors.New("Claude CLI Chat supports text content only")
		}
		switch message.Role {
		case "system", "developer":
			if len(history) > 0 || len(message.ToolCalls) > 0 {
				return out, errors.New("system and developer messages must precede conversation history")
			}
			system = append(system, message.Content.Text)
		case "user", "assistant", "tool":
			if message.Role != "assistant" && len(message.ToolCalls) > 0 || message.Role != "tool" && message.ToolCallID != "" {
				return out, errors.New("invalid Chat tool message")
			}
			if message.Role == "user" {
				latestUser = len(history)
			}
			history = append(history, message)
		default:
			return out, errors.New("unsupported Chat message role")
		}
	}
	if latestUser < 0 {
		return out, errors.New("Claude CLI Chat requires a user message")
	}
	if len(system) > 0 {
		out.System, _ = json.Marshal(strings.Join(system, "\n\n"))
	}
	prompt := history[0].Content.Text
	if latestUser > 0 {
		transcript, _ := json.Marshal(history[:latestUser+1])
		prompt = "The following JSON is the conversation history. Continue by answering the last user message. Historical tool calls and results are context, not requests to execute tools again.\n" + string(transcript)
	}
	content, _ := json.Marshal(prompt)
	out.Messages = append(out.Messages, anthropicMessageJSON{Role: "user", Content: content})
	for _, message := range history[latestUser+1:] {
		var blocks []anthropicContentBlockJSON
		if message.Role == "tool" {
			if message.ToolCallID == "" {
				return out, errors.New("a tool result requires tool_call_id")
			}
			text, _ := json.Marshal(message.Content.Text)
			blocks = append(blocks, anthropicContentBlockJSON{Type: "tool_result", ToolUseID: message.ToolCallID, Content: text})
			if last := &out.Messages[len(out.Messages)-1]; last.Role == "user" && len(out.Messages) > 1 {
				var prior []anthropicContentBlockJSON
				_ = json.Unmarshal(last.Content, &prior)
				last.Content, _ = json.Marshal(append(prior, blocks...))
				continue
			}
		} else if message.Role == "assistant" {
			if message.Content.Text != "" {
				blocks = append(blocks, anthropicContentBlockJSON{Type: "text", Text: message.Content.Text})
			}
			for _, call := range message.ToolCalls {
				var args map[string]json.RawMessage
				if call.Type != "function" || call.ID == "" || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
					return out, errors.New("invalid Chat function call")
				}
				blocks = append(blocks, anthropicContentBlockJSON{Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: json.RawMessage(call.Function.Arguments)})
			}
		} else {
			return out, errors.New("invalid active Chat tool history")
		}
		role := message.Role
		if role == "tool" {
			role = "user"
		}
		content, _ := json.Marshal(blocks)
		out.Messages = append(out.Messages, anthropicMessageJSON{Role: role, Content: content})
	}
	return out, nil
}

type nativeChatOutput struct {
	w                     http.ResponseWriter
	model, id             string
	created               int64
	started, includeUsage bool
	toolIndexes           map[int]int
	toolArguments         map[int]bool
}

func (o *nativeChatOutput) chunk(delta chatMessageDeltaJSON, finish *string, usage *usageJSON) error {
	if !o.started {
		o.w.Header().Set("Content-Type", "text/event-stream")
		o.w.Header().Set("Cache-Control", "no-cache")
		o.started = true
	}
	chunk := chatCompletionChunk{ID: o.id, Object: "chat.completion.chunk", Created: o.created, Model: o.model, Choices: []chatChunkChoiceJSON{{Index: 0, Delta: delta, FinishReason: finish}}, Usage: usage}
	if usage != nil {
		chunk.Choices = []chatChunkChoiceJSON{}
	}
	if err := writeSSE(o.w, chunk); err != nil {
		return err
	}
	return http.NewResponseController(o.w).Flush()
}

func (o *nativeChatOutput) event(data []byte) error {
	var event struct {
		Type    string
		Index   int
		Message struct{ ID string }
		Block   anthropicContentBlockJSON `json:"content_block"`
		Delta   struct {
			Text        string
			PartialJSON string `json:"partial_json"`
		}
	}
	if json.Unmarshal(data, &event) != nil {
		return errors.New("invalid native Chat event")
	}
	switch event.Type {
	case "message_start":
		o.id = event.Message.ID
		return o.chunk(chatMessageDeltaJSON{Role: "assistant"}, nil, nil)
	case "content_block_start":
		if event.Block.Type == "tool_use" {
			index := len(o.toolIndexes)
			o.toolIndexes[event.Index] = index
			args := string(event.Block.Input)
			if args == "{}" {
				args = ""
			}
			o.toolArguments[event.Index] = args != ""
			return o.chunk(chatMessageDeltaJSON{ToolCalls: []toolCallJSON{{Index: &index, ID: event.Block.ID, Type: "function", Function: functionCallJSON{Name: event.Block.Name, Arguments: args}}}}, nil, nil)
		}
		if event.Block.Text != "" {
			return o.chunk(chatMessageDeltaJSON{Content: event.Block.Text}, nil, nil)
		}
	case "content_block_delta":
		if index, ok := o.toolIndexes[event.Index]; ok {
			o.toolArguments[event.Index] = o.toolArguments[event.Index] || event.Delta.PartialJSON != ""
			return o.chunk(chatMessageDeltaJSON{ToolCalls: []toolCallJSON{{Index: &index, Function: functionCallJSON{Arguments: event.Delta.PartialJSON}}}}, nil, nil)
		}
		if event.Delta.Text != "" {
			return o.chunk(chatMessageDeltaJSON{Content: event.Delta.Text}, nil, nil)
		}
	case "content_block_stop":
		if index, ok := o.toolIndexes[event.Index]; ok && !o.toolArguments[event.Index] {
			return o.chunk(chatMessageDeltaJSON{ToolCalls: []toolCallJSON{{Index: &index, Function: functionCallJSON{Arguments: "{}"}}}}, nil, nil)
		}
	}
	return nil
}

func (o *nativeChatOutput) fail(status int, errType, code, message string) {
	if o.started {
		_ = writeSSE(o.w, openAIErrorBody{Error: openAIError{Type: errType, Code: code, Message: message}})
		_ = http.NewResponseController(o.w).Flush()
		return
	}
	writeOpenAIError(o.w, status, errType, code, message)
}

func (o *nativeChatOutput) finish(response map[string]any, content []anthropicContentBlockJSON, usage runtime.Usage, stream bool) {
	o.id, _ = response["id"].(string)
	stop, _ := response["stop_reason"].(string)
	reason := "stop"
	if stop == "tool_use" {
		reason = "tool_calls"
	} else if stop == "max_tokens" {
		reason = "length"
	}
	counts := usageJSON{PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens}
	if stream {
		if o.chunk(chatMessageDeltaJSON{}, &reason, nil) != nil {
			return
		}
		if o.includeUsage && o.chunk(chatMessageDeltaJSON{}, nil, &counts) != nil {
			return
		}
		_, _ = fmt.Fprint(o.w, "data: [DONE]\n\n")
		_ = http.NewResponseController(o.w).Flush()
		return
	}
	message := chatMessageJSON{Role: "assistant"}
	for _, block := range content {
		if block.Type == "text" {
			message.Content.Text += block.Text
		}
		if block.Type == "tool_use" {
			message.ToolCalls = append(message.ToolCalls, toolCallJSON{ID: block.ID, Type: "function", Function: functionCallJSON{Name: block.Name, Arguments: string(block.Input)}})
		}
	}
	o.w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(o.w).Encode(chatCompletionResponse{ID: o.id, Object: "chat.completion", Created: o.created, Model: o.model, Choices: []chatChoiceJSON{{Index: 0, Message: message, FinishReason: reason}}, Usage: counts})
}

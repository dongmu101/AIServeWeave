package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
)

// useNativeResponses routes Claude CLI models and their tool continuations to native Messages.
// useNativeResponses 把 Claude CLI 模型及其工具续接路由到原生 Messages。
func (h *handlers) useNativeResponses(req responsesRequest) bool {
	var items []responsesInputItem
	_ = json.Unmarshal(req.Input, &items)
	for i := len(items) - 1; i >= 0 && strings.HasSuffix(items[i].Type, "_call_output"); i-- {
		if strings.HasPrefix(items[i].CallID, "toolu_aisw_") {
			return true
		}
	}
	return len(h.sched.MessagesCandidates(req.Model)) > 0
}

// nativeResponses reuses the Chat compatibility layer, so Responses continuations share its CLI sessions.
// nativeResponses 复用 Chat 兼容层，使 Responses 续接与之共用同一套 CLI 会话校验。
func (h *handlers) nativeResponses(w http.ResponseWriter, r *http.Request, req responsesRequest, raw json.RawMessage, start time.Time) {
	if len(raw) > 1<<20 {
		writeOpenAIError(w, 413, "invalid_request_error", "request_too_large", "Claude CLI Responses request size limit exceeded")
		return
	}
	canonical, toolset, err := req.toRuntime()
	if err != nil {
		writeOpenAIError(w, 400, "invalid_request_error", "invalid_request", err.Error())
		return
	}
	chat, err := req.toNativeChat(canonical)
	var native anthropicMessagesRequest
	if err == nil {
		native, err = chat.toNativeMessages()
	}
	if err != nil {
		writeOpenAIError(w, 400, "invalid_request_error", "unsupported_parameter", err.Error())
		return
	}
	if len(toolset.ignored) > 0 {
		w.Header().Set(ignoredToolsHeader, strings.Join(toolset.ignored, ","))
	}
	document, _ := json.Marshal(native)
	output := &nativeResponsesOutput{w: w, obj: req.render(newResponseID(), req.Model, h.clock.Now()), toolset: toolset, calls: make(map[int]int)}
	h.nativeMessagesOutput(w, r, native, document, start, output)
}

// toNativeChat refuses in Responses terms what Claude CLI cannot honour, then rebuilds the Chat form.
// toNativeChat 先用 Responses 的字段名拒绝 Claude CLI 无法兑现的语义，再重建为 Chat 形式。
func (req responsesRequest) toNativeChat(canonical runtime.ChatRequest) (chatCompletionRequest, error) {
	out := chatCompletionRequest{Model: req.Model, Stream: req.Stream, Temperature: req.Temperature, MaxTokens: req.MaxOutputTokens, ReasoningEffort: nativeEffort(canonical.Reasoning)}
	switch {
	case req.Store != nil && *req.Store:
		return out, errors.New("Claude CLI Responses supports only store=false; persistent Responses storage is unavailable")
	case req.PreviousResponseID != "":
		return out, errors.New("Claude CLI Responses does not support previous_response_id; send the full input instead")
	case req.MaxOutputTokens != nil && (*req.MaxOutputTokens < 1 || *req.MaxOutputTokens > 64000), req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 1):
		return out, errors.New("Claude CLI requires max_output_tokens in 1..64000 and temperature in 0..1")
	case req.TopP != nil:
		return out, errors.New("Claude CLI Responses does not support top_p")
	case canonical.ResponseFormat != nil && canonical.ResponseFormat.Type != "text":
		return out, errors.New("Claude CLI Responses does not support text.format")
	case canonical.ToolChoice != "" && canonical.ToolChoice != "auto":
		return out, errors.New("Claude CLI Responses supports only automatic tool choice")
	}
	tools := append([]responsesTool(nil), req.Tools...)
	var items []responsesInputItem
	if json.Unmarshal(req.Input, &items) == nil {
		for _, item := range items {
			tools = append(tools, item.Tools...)
		}
	}
	if strictTools(tools) {
		return out, errors.New("Claude CLI Responses supports only strict=false tools; strict schema enforcement is unavailable")
	}
	for _, tool := range canonical.Tools {
		out.Tools = append(out.Tools, toolJSON{Type: "function", Function: functionDefJSON{Name: tool.Function.Name, Description: tool.Function.Description, Parameters: tool.Function.Parameters}})
	}
	hasUser := false
	for _, message := range canonical.Messages {
		if len(message.ContentParts) > 0 {
			return out, errors.New("Claude CLI Responses supports text content only")
		}
		switch message.Role {
		case "system", "assistant", "tool":
		case "user":
			hasUser = true
		default:
			return out, errors.New("unsupported Responses input role")
		}
		var calls []toolCallJSON
		for _, call := range message.ToolCalls {
			var args map[string]json.RawMessage
			if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
				return out, errors.New("Claude CLI Responses requires tool call arguments to be a JSON object")
			}
			calls = append(calls, toolCallJSON{ID: call.ID, Type: "function", Function: functionCallJSON{Name: call.Function.Name, Arguments: call.Function.Arguments}})
		}
		// Responses replays one Claude turn as separate text and call items; merge them back.
		// Responses 把同一个 Claude 回合拆成独立的文本项和调用项回放，这里合并回一条。
		if n := len(out.Messages); message.Role == "assistant" && n > 0 && out.Messages[n-1].Role == "assistant" {
			out.Messages[n-1].Content.Text += message.Content
			out.Messages[n-1].ToolCalls = append(out.Messages[n-1].ToolCalls, calls...)
			continue
		}
		out.Messages = append(out.Messages, chatMessageJSON{Role: message.Role, Content: chatContentJSON{Text: message.Content}, ToolCallID: message.ToolCallID, ToolCalls: calls})
	}
	if !hasUser {
		return out, errors.New("Claude CLI Responses requires a user message")
	}
	return out, nil
}

// nativeEffort restates a Responses reasoning hint as the Chat
// reasoning_effort toNativeMessages reads: "none" to turn thinking off, the
// requested level, or "medium" when thinking was asked for without a level
// (runtime.ReasoningConfig.Budget's default too). Nil stays "".
//
// nativeEffort 把 Responses 的思考提示改写为 toNativeMessages 读取的 Chat
// reasoning_effort：关闭思考为 "none"，否则为请求的档位；要求思考却没给档位时为
// "medium"（与 runtime.ReasoningConfig.Budget 的默认值一致）。nil 保持为 ""。
func nativeEffort(reasoning *runtime.ReasoningConfig) string {
	switch {
	case reasoning == nil:
		return ""
	case !reasoning.Enabled:
		return "none"
	case runtime.ValidReasoningEffort(reasoning.EffortLevel()):
		return reasoning.EffortLevel()
	}
	return runtime.ReasoningEffortMedium
}

// strictTools reports whether any declared tool, including namespace members, asks for strict schemas.
// strictTools 报告是否有声明的工具（含 namespace 成员）要求严格 schema。
func strictTools(tools []responsesTool) bool {
	for _, tool := range tools {
		if tool.Strict != nil && *tool.Strict || strictTools(tool.Tools) {
			return true
		}
	}
	return false
}

type nativeResponsesOutput struct {
	w       http.ResponseWriter
	obj     responseObject
	toolset responsesToolset
	em      *responsesEmitter
	items   *responseItems
	calls   map[int]int // native block index → tool delta index / 原生块索引 → 工具增量索引
	started bool
}

func (o *nativeResponsesOutput) streaming() bool { return o.started }

func (o *nativeResponsesOutput) endpoints() (string, string) {
	return UsageEndpointResponses, EndpointResponses
}

func (o *nativeResponsesOutput) begin() error {
	if o.started {
		return nil
	}
	flusher, ok := o.w.(http.Flusher)
	if !ok {
		writeOpenAIError(o.w, http.StatusInternalServerError, "api_error", "streaming_unsupported", "this connection does not support streaming")
		return errors.New("streaming unsupported")
	}
	writeSSEHeader(o.w)
	o.w.WriteHeader(http.StatusOK)
	o.started = true
	o.em = &responsesEmitter{w: o.w, flusher: flusher}
	o.items = newResponseItems(o.em, o.toolset)
	obj := o.obj
	obj.Status = "in_progress"
	o.em.event("response.created", map[string]any{"response": obj})
	o.em.event("response.in_progress", map[string]any{"response": obj})
	return nil
}

// event renders one native event as Responses items: thinking as a
// reasoning item, text as a message item, tool_use as a call item.
// Signatures and redacted thinking have no Responses form and are skipped.
//
// event 把一条原生事件渲染为 Responses 输出项：思考为 reasoning 项，文本为 message
// 项，tool_use 为调用项。签名与 redacted thinking 在 Responses 中没有对应形式，跳过。
func (o *nativeResponsesOutput) event(data []byte) error {
	var event struct {
		Type  string
		Index int
		Block anthropicContentBlockJSON `json:"content_block"`
		Delta struct {
			Type, Text, Thinking string
			PartialJSON          string `json:"partial_json"`
		}
	}
	if json.Unmarshal(data, &event) != nil {
		return errors.New("invalid native Responses event")
	}
	if err := o.begin(); err != nil {
		return err
	}
	switch event.Type {
	case "content_block_start":
		if event.Block.Type == "tool_use" {
			args := string(event.Block.Input)
			if args == "{}" {
				args = ""
			}
			index := len(o.calls)
			o.calls[event.Index] = index
			o.items.toolCall(runtime.ToolCallDelta{Index: index, ID: event.Block.ID, Type: "function", Function: runtime.FunctionCallDelta{Name: event.Block.Name, Arguments: args}})
		} else if event.Block.Type == "text" {
			o.items.text(event.Block.Text)
		} else if event.Block.Type == "thinking" && event.Block.Thinking != nil && *event.Block.Thinking != "" {
			o.items.reasoning(*event.Block.Thinking)
		}
	case "content_block_delta":
		if index, ok := o.calls[event.Index]; ok {
			if event.Delta.Type == "input_json_delta" {
				o.items.toolCall(runtime.ToolCallDelta{Index: index, Function: runtime.FunctionCallDelta{Arguments: event.Delta.PartialJSON}})
			}
		} else if event.Delta.Type == "text_delta" {
			o.items.text(event.Delta.Text)
		} else if event.Delta.Type == "thinking_delta" && event.Delta.Thinking != "" {
			o.items.reasoning(event.Delta.Thinking)
		}
	}
	return nil
}

func (o *nativeResponsesOutput) fail(status int, errType, code, message string) {
	if o.started {
		obj := o.obj
		obj.Status = "failed"
		obj.Error = &responsesError{Code: code, Message: message}
		o.em.event("response.failed", map[string]any{"response": obj})
		return
	}
	writeOpenAIError(o.w, status, errType, code, message)
}

func (o *nativeResponsesOutput) finish(response map[string]any, content []anthropicContentBlockJSON, usage runtime.Usage, stream bool) {
	stop, _ := response["stop_reason"].(string)
	reason := "stop"
	if stop == "tool_use" {
		reason = "tool_calls"
	} else if stop == "max_tokens" {
		reason = "length"
	}
	obj := o.obj
	obj.Status, obj.IncompleteDetails = statusFor(reason)
	obj.Usage = usageFor(usage)
	if stream {
		if o.begin() != nil {
			return
		}
		obj.Output = o.items.finish()
		// A truncated answer must not read as a finished agent turn.
		// 被截断的答案不能被当成一个完成的 agent 回合。
		terminal := "response.completed"
		if obj.Status == "incomplete" {
			terminal = "response.incomplete"
		}
		o.em.event(terminal, map[string]any{"response": obj})
		return
	}
	message := runtime.ChatMessage{Role: "assistant"}
	for _, block := range content {
		if block.Type == "thinking" && block.Thinking != nil {
			message.ReasoningContent += *block.Thinking
		}
		if block.Type == "text" {
			message.Content += block.Text
		}
		if block.Type == "tool_use" {
			message.ToolCalls = append(message.ToolCalls, runtime.ToolCall{ID: block.ID, Type: "function", Function: runtime.FunctionCall{Name: block.Name, Arguments: string(block.Input)}})
		}
	}
	obj.Output = outputItemsFor(message, o.toolset)
	o.w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(o.w).Encode(obj)
}

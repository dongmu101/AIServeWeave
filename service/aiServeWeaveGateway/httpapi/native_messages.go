package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/messagesession"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

func nativeResultIDs(req anthropicMessagesRequest) []string {
	var ids []string
	if len(req.Messages) == 0 {
		return ids
	}
	var blocks []struct {
		Type string `json:"type"`
		ID   string `json:"tool_use_id"`
	}
	_ = json.Unmarshal(req.Messages[len(req.Messages)-1].Content, &blocks)
	for _, block := range blocks {
		if block.Type == "tool_result" {
			ids = append(ids, block.ID)
		}
	}
	return ids
}

func (h *handlers) useNativeMessages(req anthropicMessagesRequest) bool {
	for _, id := range nativeResultIDs(req) {
		if strings.HasPrefix(id, "toolu_aisw_") {
			return true
		}
	}
	return len(h.sched.MessagesCandidates(req.Model)) > 0
}

// nativeMessages preserves native block order and authenticates every
// continuation. A client "thinking" object is read as a reasoning hint and
// rewritten by nativeThinking into the one form the Claude CLI bridge
// accepts, so adaptive thinking or a budget that does not fit max_tokens is
// adjusted or dropped rather than refused; only a malformed object is a 400.
//
// nativeMessages 保留原生块顺序，并对每次续接重新认证。客户端的 "thinking" 对象被当作
// 思考提示读取，并由 nativeThinking 改写为 Claude CLI 桥接唯一接受的形式，因此
// adaptive thinking 或放不进 max_tokens 的预算会被调整或丢弃，而不是拒绝；只有格式
// 错误的对象才返回 400。
func (h *handlers) nativeMessages(w http.ResponseWriter, r *http.Request, req anthropicMessagesRequest, raw json.RawMessage, start time.Time) {
	if len(req.Thinking) > 0 {
		reasoning, err := reasoningFromAnthropic(req.Thinking)
		if err != nil {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		req.Thinking = nativeThinking(reasoning, req.MaxTokens, req.Temperature)
		var document map[string]json.RawMessage
		if json.Unmarshal(raw, &document) != nil {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "the request body is not valid JSON")
			return
		}
		if req.Thinking != nil {
			document["thinking"] = req.Thinking
		} else {
			delete(document, "thinking")
		}
		raw, _ = json.Marshal(document)
	}
	h.nativeMessagesOutput(w, r, req, raw, start, nil)
}

// nativeThinking maps a reasoning hint onto the Messages "thinking" object
// the Claude CLI bridge accepts: {"type":"enabled","budget_tokens":N} with
// 1024 <= N < maxTokens, or nil for the bridge's default of no thinking. The
// budget is reasoning.Budget() clamped below maxTokens; a hint that turns
// thinking off, comes with a temperature (the Messages API refuses thinking
// with a modified temperature) or cannot fit 1024 tokens below maxTokens is
// dropped.
//
// nativeThinking 把思考提示映射为 Claude CLI 桥接接受的 Messages "thinking" 对象：
// {"type":"enabled","budget_tokens":N}，其中 1024 <= N < maxTokens；或返回 nil，即
// 桥接默认的不思考。预算取 reasoning.Budget()，并收敛到 maxTokens 以下；关闭思考的
// 提示、带 temperature 的提示（Messages API 不接受修改了 temperature 的思考），以及
// maxTokens 以下放不下 1024 token 的提示都被丢弃。
func nativeThinking(reasoning *runtime.ReasoningConfig, maxTokens int, temperature *float64) json.RawMessage {
	if reasoning == nil || !reasoning.Enabled || temperature != nil {
		return nil
	}
	budget := min(reasoning.Budget(), maxTokens-1)
	if budget < 1024 {
		return nil
	}
	return json.RawMessage(fmt.Sprintf(`{"type":"enabled","budget_tokens":%d}`, budget))
}

// nativeOutput renders the native Messages stream in another public protocol.
// nativeOutput 以另一种公开协议渲染原生 Messages 流。
type nativeOutput interface {
	fail(status int, errType, code, message string)
	event(data []byte) error
	finish(response map[string]any, content []anthropicContentBlockJSON, usage runtime.Usage, stream bool)
	// streaming reports whether response headers are already sent.
	// streaming 报告响应头是否已经发出。
	streaming() bool
	// endpoints names the usage ledger and metrics endpoints of the protocol.
	// endpoints 给出该协议在用量账本与指标中的端点名。
	endpoints() (usage, metric string)
}

// nativeMessagesOutput shares ownership, affinity and limits between public protocols.
// nativeMessagesOutput 在不同公开协议之间共用归属校验、节点绑定和容量限制。
func (h *handlers) nativeMessagesOutput(w http.ResponseWriter, r *http.Request, req anthropicMessagesRequest, raw json.RawMessage, start time.Time, out nativeOutput) {
	started := false
	fail := func(status int, errType, code, message string) {
		if out != nil {
			out.fail(status, errType, code, message)
		} else if started {
			_ = writeAnthropicSSE(w, "error", map[string]any{"type": "error", "error": map[string]string{"type": errType, "message": message}})
			_ = http.NewResponseController(w).Flush()
		} else {
			writeAnthropicError(w, status, errType, message)
		}
	}
	ids := nativeResultIDs(req)
	continuationUnavailable := func() {
		fail(409, "invalid_request_error", "continuation_unavailable", "Messages continuation unavailable")
	}
	dispatchError := func(err error) {
		// A continuation whose node no longer holds the CLI cannot resume
		// anywhere, so it reads as an expired lease, not a node outage.
		// 节点已不再持有 CLI 的续接在哪里都无法恢复，因此按租约失效报告，而不是节点故障。
		if len(ids) > 0 && continuationLost(err) {
			continuationUnavailable()
			return
		}
		status, errType, code, message := dispatchErrorDetails(h.logger, err)
		setRetryAfter(w, err)
		fail(status, errType, code, message)
	}
	identity, ok := IdentityFrom(r.Context())
	if !ok || identity.TenantID == "" || identity.KeyID == "" {
		fail(401, "authentication_error", "authentication_error", "native Messages requires authenticated API keys")
		return
	}
	if h.messagesSessions == nil {
		fail(503, "api_error", "session_unavailable", "native Messages requires shared Redis session coordination")
		return
	}
	if len(raw) > 1<<20 {
		fail(413, "invalid_request_error", "request_too_large", "native Messages request size limit exceeded")
		return
	}
	native := runtime.MessagesRequest{Model: req.Model, TenantID: identity.TenantID, KeyID: identity.KeyID, JSON: raw}
	var record messagesession.Record
	var finish func(context.Context, bool) error
	var stream runtime.Stream[runtime.MessagesEvent]
	var err error
	committed := false
	if len(ids) > 0 {
		record, finish, err = h.messagesSessions.Claim(r.Context(), ids, identity.TenantID, identity.KeyID, req.Model)
		if err != nil {
			continuationUnavailable()
			return
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			_ = finish(ctx, committed)
		}()
		stream, err = h.sched.Messages(r.Context(), record.Candidate, native)
	} else {
		var candidate scheduler.Candidate
		stream, candidate, err = h.sched.MessagesStart(r.Context(), native)
		// A model the caller may not use falls through to dispatchError's
		// not-found, like any other front door; only a missing native node is
		// an outage.
		//
		// 调用方不能使用的模型与其他前门一样落到 dispatchError 的 not-found；只有缺少
		// 原生节点才算故障。
		if errors.Is(err, scheduler.ErrNoCapableNode) && !errors.Is(err, scheduler.ErrModelNotAllowed) {
			fail(503, "api_error", "node_unavailable", "native Messages node unavailable")
			return
		}
		record = messagesession.Record{TenantID: identity.TenantID, KeyID: identity.KeyID, Model: req.Model, Candidate: candidate}
	}
	if err != nil {
		committed = !messagesNotApplied(err)
		dispatchError(err)
		return
	}
	defer stream.Close()
	response := make(map[string]any)
	var content []anthropicContentBlockJSON
	var calls []string
	var usage runtime.Usage
	stopped := false
	total := 0
	protocolError := func(message string) {
		fail(502, "api_error", "upstream_protocol", message)
	}
	for {
		item, err := stream.Recv()
		if err == io.EOF {
			if !stopped {
				err = errors.New("Messages stream ended before message_stop")
			} else {
				endpoint := UsageEndpointAnthropicMessages
				if out != nil {
					out.finish(response, content, usage, req.Stream)
					endpoint, _ = out.endpoints()
				} else if !req.Stream {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(response)
				}
				h.recordUsage(r.Context(), usage, time.Since(start), endpoint, req.Model)
				return
			}
		}
		if err != nil {
			if !started {
				committed = committed || !messagesNotApplied(err)
				dispatchError(err)
			} else {
				protocolError("native Messages stream failed")
			}
			return
		}
		committed = true
		total += len(item.JSON)
		if total > 8<<20 {
			protocolError("native Messages response size limit exceeded")
			return
		}
		var event struct {
			Type    string                    `json:"type"`
			Index   int                       `json:"index"`
			Message map[string]any            `json:"message"`
			Block   anthropicContentBlockJSON `json:"content_block"`
			Delta   struct {
				Type, Text, Thinking, Signature string
				PartialJSON                     string `json:"partial_json"`
				StopReason                      string `json:"stop_reason"`
			} `json:"delta"`
			Usage map[string]any `json:"usage"`
		}
		if json.Unmarshal(item.JSON, &event) != nil || stopped {
			protocolError("invalid native Messages event")
			return
		}
		data := item.JSON
		switch event.Type {
		case "message_start":
			if event.Message == nil || len(response) != 0 {
				protocolError("invalid native Messages event")
				return
			}
			response = event.Message
			response["model"] = req.Model
			var obj map[string]any
			_ = json.Unmarshal(data, &obj)
			obj["message"] = response
			data, _ = json.Marshal(obj)
			if value, ok := response["usage"].(map[string]any); ok {
				if n, ok := value["input_tokens"].(float64); ok {
					usage.PromptTokens = int(n)
				}
			}
		case "content_block_start":
			if event.Index != len(content) {
				protocolError("invalid native Messages block index")
				return
			}
			content = append(content, event.Block)
			if event.Block.Type == "tool_use" {
				calls = append(calls, event.Block.ID)
			}
		case "content_block_delta":
			if event.Index < 0 || event.Index >= len(content) {
				protocolError("invalid native Messages block index")
				return
			}
			switch event.Delta.Type {
			case "text_delta":
				content[event.Index].Text += event.Delta.Text
			case "thinking_delta":
				content[event.Index].Thinking = appendText(content[event.Index].Thinking, event.Delta.Thinking)
			case "signature_delta":
				content[event.Index].Signature = appendText(content[event.Index].Signature, event.Delta.Signature)
			}
			if event.Delta.Type == "input_json_delta" {
				if string(content[event.Index].Input) == "{}" {
					content[event.Index].Input = nil
				}
				content[event.Index].Input = append(content[event.Index].Input, event.Delta.PartialJSON...)
			}
		case "content_block_stop":
		case "message_delta":
			response["stop_reason"] = event.Delta.StopReason
			prior, _ := response["usage"].(map[string]any)
			if prior == nil {
				protocolError("invalid native Messages usage")
				return
			}
			for k, v := range event.Usage {
				prior[k] = v
			}
			response["usage"] = prior
			if n, ok := event.Usage["output_tokens"].(float64); ok {
				usage.CompletionTokens = int(n)
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		case "message_stop":
			if len(calls) > 0 {
				record.IDs = calls
				if h.messagesSessions.Publish(r.Context(), record) != nil {
					fail(503, "api_error", "session_unavailable", "Messages continuation storage unavailable")
					return
				}
			}
			stopped = true
			response["content"] = content
		case "error":
			protocolError("native Messages stream failed")
			return
		default:
			protocolError("unsupported native Messages event")
			return
		}
		if req.Stream {
			if out != nil {
				if err := out.event(data); err != nil {
					return
				}
				if !started && out.streaming() {
					_, metric := out.endpoints()
					h.logTTFT(r, req.Model, record.Candidate.NodeID, start, true)
					h.metrics.TTFT(metric, time.Since(start))
				}
				started = out.streaming()
				continue
			}
			if !started {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				started = true
				h.logTTFT(r, req.Model, record.Candidate.NodeID, start, true)
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data); err != nil {
				return
			}
			if http.NewResponseController(w).Flush() != nil {
				return
			}
		}
	}
}

// appendText returns a pointer to *field + s, treating a nil field as empty.
// appendText 返回指向 *field + s 的指针，nil 视为空串。
func appendText(field *string, s string) *string {
	text := s
	if field != nil {
		text = *field + s
	}
	return &text
}

func messagesNotApplied(err error) bool {
	var failure *runtime.RuntimeError
	return errors.As(err, &failure) && (failure.Code == runtime.ErrorInvalidConfig || failure.Code == runtime.ErrorCapability || failure.Code == runtime.ErrorBackpressure)
}

// continuationLost reports whether a continuation's node answered that it no
// longer holds the CLI session — the Agent restarted, the session expired, or
// the runtime is shutting down. The lease is then consumed, never restored.
//
// continuationLost 判断续接所在节点是否答复已不再持有该 CLI 会话——Agent 重启、会话
// 过期或运行时正在关闭。此时租约被消费，不会恢复。
func continuationLost(err error) bool {
	var failure *runtime.RuntimeError
	return errors.As(err, &failure) && failure.Code == runtime.ErrorClosed
}

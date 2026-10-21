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

// nativeMessages preserves native block order and authenticates every continuation.
// nativeMessages 保留原生块顺序，并对每次续接重新认证。
func (h *handlers) nativeMessages(w http.ResponseWriter, r *http.Request, req anthropicMessagesRequest, raw json.RawMessage, start time.Time) {
	h.nativeMessagesOutput(w, r, req, raw, start, nil)
}

// nativeMessagesOutput shares ownership, affinity and limits between public protocols.
// nativeMessagesOutput 在不同公开协议之间共用归属校验、节点绑定和容量限制。
func (h *handlers) nativeMessagesOutput(w http.ResponseWriter, r *http.Request, req anthropicMessagesRequest, raw json.RawMessage, start time.Time, chat *nativeChatOutput) {
	started := false
	fail := func(status int, errType, code, message string) {
		if chat != nil {
			chat.fail(status, errType, code, message)
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
		if errors.Is(err, scheduler.ErrNoCapableNode) {
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
				if chat != nil {
					chat.finish(response, content, usage, req.Stream)
					endpoint = UsageEndpointChat
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
				Type, Text  string
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
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
			if event.Delta.Type == "text_delta" {
				content[event.Index].Text += event.Delta.Text
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
			if chat != nil {
				if err := chat.event(data); err != nil {
					return
				}
				if !started && chat.started {
					h.logTTFT(r, req.Model, record.Candidate.NodeID, start, true)
					h.metrics.TTFT(EndpointChatCompletions, time.Since(start))
				}
				started = chat.started
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

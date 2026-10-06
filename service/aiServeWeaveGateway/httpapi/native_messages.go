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
	identity, ok := IdentityFrom(r.Context())
	if !ok || identity.TenantID == "" || identity.KeyID == "" {
		writeAnthropicError(w, 401, "authentication_error", "native Messages requires authenticated API keys")
		return
	}
	if h.messagesSessions == nil {
		writeAnthropicError(w, 503, "api_error", "native Messages requires shared Redis session coordination")
		return
	}
	if len(raw) > 1<<20 {
		writeAnthropicError(w, 413, "invalid_request_error", "native Messages request size limit exceeded")
		return
	}
	ids := nativeResultIDs(req)
	candidates := h.sched.MessagesCandidates(req.Model)
	var record messagesession.Record
	var finish func(context.Context, bool) error
	if len(ids) > 0 {
		var err error
		record, finish, err = h.messagesSessions.Claim(r.Context(), ids, identity.TenantID, identity.KeyID, req.Model)
		if err != nil {
			writeAnthropicError(w, 409, "invalid_request_error", "Messages continuation unavailable")
			return
		}
	} else {
		if len(candidates) == 0 {
			writeAnthropicError(w, 503, "api_error", "native Messages node unavailable")
			return
		}
		record = messagesession.Record{TenantID: identity.TenantID, KeyID: identity.KeyID, Model: req.Model, Candidate: candidates[0]}
	}
	committed := false
	if finish != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			_ = finish(ctx, committed)
		}()
	}
	stream, err := h.sched.Messages(r.Context(), record.Candidate, runtime.MessagesRequest{Model: req.Model, TenantID: identity.TenantID, KeyID: identity.KeyID, JSON: raw})
	if err != nil {
		committed = !messagesNotApplied(err)
		handleAnthropicDispatchError(w, h.logger, err)
		return
	}
	defer stream.Close()
	response := make(map[string]any)
	var content []anthropicContentBlockJSON
	var calls []string
	var usage runtime.Usage
	started, stopped := false, false
	total := 0
	fail := func(status int, message string) {
		if started {
			_ = writeAnthropicSSE(w, "error", map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": message}})
		} else {
			writeAnthropicError(w, status, "api_error", message)
		}
	}
	for {
		item, err := stream.Recv()
		if err == io.EOF {
			if !stopped {
				err = errors.New("Messages stream ended before message_stop")
			} else {
				if !req.Stream {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(response)
				}
				h.recordUsage(r.Context(), usage, time.Since(start), UsageEndpointAnthropicMessages, req.Model)
				return
			}
		}
		if err != nil {
			if !started {
				committed = committed || !messagesNotApplied(err)
				handleAnthropicDispatchError(w, h.logger, err)
			} else {
				_ = writeAnthropicSSE(w, "error", map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "native Messages stream failed"}})
			}
			return
		}
		committed = true
		total += len(item.JSON)
		if total > 8<<20 {
			fail(502, "native Messages response size limit exceeded")
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
			fail(502, "invalid native Messages event")
			return
		}
		data := item.JSON
		switch event.Type {
		case "message_start":
			if event.Message == nil || len(response) != 0 {
				fail(502, "invalid native Messages event")
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
				fail(502, "invalid native Messages block index")
				return
			}
			content = append(content, event.Block)
			if event.Block.Type == "tool_use" {
				calls = append(calls, event.Block.ID)
			}
		case "content_block_delta":
			if event.Index < 0 || event.Index >= len(content) {
				fail(502, "invalid native Messages block index")
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
				fail(502, "invalid native Messages usage")
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
					if started {
						_ = writeAnthropicSSE(w, "error", map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "Messages continuation storage unavailable"}})
					} else {
						writeAnthropicError(w, 503, "api_error", "Messages continuation storage unavailable")
					}
					return
				}
			}
			stopped = true
			response["content"] = content
		case "error":
			fail(502, "native Messages stream failed")
			return
		default:
			fail(502, "unsupported native Messages event")
			return
		}
		if req.Stream {
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

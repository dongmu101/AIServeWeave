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
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/messagesession"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// Messages size limits. A request may be as large as the Messages API itself
// accepts, and every hop to the Agent shares that limit. A relayed response
// stays bounded, far above any model output, because a node may be run by a
// third party and the Gateway must not buffer whatever it streams.
//
// Messages 大小上限。请求可以达到 Messages API 自身接受的大小，通往 Agent 的每一跳共用
// 该上限。转发的响应仍然有界（远高于任何模型输出），因为节点可能由第三方运行，Gateway
// 不能缓冲它流过来的任意数据。
const (
	// maxMessagesRequestBytes bounds a Messages, Chat or Responses body bound
	// for the Claude CLI bridge.
	// maxMessagesRequestBytes 限制发往 Claude CLI 桥接的 Messages、Chat 或 Responses 请求体。
	maxMessagesRequestBytes = tunnelwire.MaxMessagesJSONBytes
	// maxNativeResponseBytes bounds the events of one native response.
	// maxNativeResponseBytes 限制一次原生响应的事件总量。
	maxNativeResponseBytes = 64 << 20
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

// nativePassthrough reports whether an unguarded request is sent unchanged
// (runtime.MessagesRequest.Native) rather than through the compatibility
// bridge. Every node serving the model must not be required to accept it:
// MessagesStart only offers the request to nodes that do, so it is enough
// that one exists. A round answering a bridge tool call stays on the bridge,
// because only the CLI that made that call can take its results; the next
// question in the same conversation is native.
//
// nativePassthrough 判断不带 safeguards 的请求是否原样发送（runtime.MessagesRequest.Native），
// 而不经过兼容桥接。并不要求服务该模型的每个节点都接受：MessagesStart 只把请求交给接受的
// 节点，所以存在一个即可。答复桥接工具调用的回合仍走桥接，因为只有发起调用的 CLI 能接收
// 其结果；同一对话的下一个提问则走原生。
func (h *handlers) nativePassthrough(ctx context.Context, req anthropicMessagesRequest) bool {
	for _, id := range nativeResultIDs(req) {
		if strings.HasPrefix(id, "toolu_aisw_") {
			return false
		}
	}
	return len(h.sched.MessagesNativeCandidates(ctx, req.Model)) > 0
}

func (h *handlers) useNativeMessages(ctx context.Context, req anthropicMessagesRequest) bool {
	if len(req.Safeguards) > 0 {
		return true
	}
	for _, id := range nativeResultIDs(req) {
		if strings.HasPrefix(id, "toolu_aisw_") {
			return true
		}
	}
	return len(h.sched.MessagesCandidates(ctx, req.Model)) > 0
}

// nativeMessages forwards the document unchanged when nativePassthrough
// allows, exactly as for a safeguarded one: the original thinking, cache
// markers, context_management and tools reach the upstream, and every round
// is stateless. Otherwise it falls back to the compatibility bridge below.
//
// nativeMessages 在 nativePassthrough 允许时原样转发文档，与带 safeguards 的请求相同：
// 原始思考、缓存标记、context_management 与工具都交给上游，每一回合都无状态。否则回退
// 到下面的兼容桥接。
//
// On the bridge, nativeMessages preserves native block order and authenticates every
// continuation. A client "thinking" object is read as a reasoning hint and
// rewritten by nativeThinking into the one form the Claude CLI bridge
// accepts, so adaptive thinking or a budget that does not fit max_tokens is
// adjusted or dropped rather than refused; only a malformed object is a 400.
// Adaptive thinking becomes a fixed budget chosen from output_config.effort
// (medium when absent): the model no longer picks its depth per request,
// which is the part of the native semantics this bridge cannot keep.
//
// nativeMessages 保留原生块顺序，并对每次续接重新认证。客户端的 "thinking" 对象被当作
// 思考提示读取，并由 nativeThinking 改写为 Claude CLI 桥接唯一接受的形式，因此
// adaptive thinking 或放不进 max_tokens 的预算会被调整或丢弃，而不是拒绝；只有格式
// 错误的对象才返回 400。adaptive 会变成按 output_config.effort 选出的固定预算（未给时
// 取 medium）：模型不再按每个请求自行决定思考深度，这是本桥接无法保留的那部分原生语义。
//
// The rest of the body is reshaped by nativeBridgeShape, so the requests
// Claude Code itself sends reach the bridge's strict decoder in a form it
// accepts.
//
// 请求体其余部分由 nativeBridgeShape 整形，使 Claude Code 自己发出的请求以桥接严格
// 解码器能接受的形式到达。
//
// Safeguards and their protocol selectors reach the CLI unchanged in meaning;
// only real upstream verdicts are returned, associated with the public tool IDs.
// Safeguards 及其协议选择完整传给 CLI；只返回真实上游判定，并关联公开工具 ID。
func (h *handlers) nativeMessages(w http.ResponseWriter, r *http.Request, req anthropicMessagesRequest, raw json.RawMessage, start time.Time) {
	var document map[string]json.RawMessage
	if json.Unmarshal(raw, &document) != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "the request body is not valid JSON")
		return
	}
	if err := runtime.ValidateMessagesSafeguards(document["safeguards"]); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if len(req.Safeguards) > 0 {
		h.nativeMessagesOutput(w, r, req, raw, start, nil, false)
		return
	}
	if h.nativePassthrough(r.Context(), req) {
		h.nativeMessagesOutput(w, r, req, raw, start, nil, true)
		return
	}
	if len(req.Thinking) > 0 {
		reasoning, err := reasoningFromAnthropic(req.Thinking, anthropicOutputEffort(req.OutputConfig))
		if err != nil {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		req.Thinking = nativeThinking(reasoning, req.MaxTokens, req.Temperature)
		if req.Thinking != nil {
			document["thinking"] = req.Thinking
		} else {
			delete(document, "thinking")
		}
	}
	if err := nativeBridgeShape(document, req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	raw, _ = json.Marshal(document)
	h.nativeMessagesOutput(w, r, req, raw, start, nil, false)
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
	// keepAlive sends the response headers if needed and one heartbeat that
	// carries no model output, keeping an idle stream past proxy timeouts.
	// keepAlive 按需发出响应头和一次不含模型输出的心跳，使空闲的流不被代理超时切断。
	keepAlive() error
	// endpoints names the usage ledger and metrics endpoints of the protocol.
	// endpoints 给出该协议在用量账本与指标中的端点名。
	endpoints() (usage, metric string)
}

// nativeMessagesOutput shares ownership, affinity and limits between public
// protocols. passthrough marks an unguarded document sent unchanged; it and a
// safeguarded one are both stateless and keep the upstream's own blocks.
//
// nativeMessagesOutput 在不同公开协议之间共用归属校验、节点绑定和容量限制。passthrough
// 表示原样发送的不带 safeguards 的文档；它与带 safeguards 的文档都无状态，并保留上游自己的内容块。
func (h *handlers) nativeMessagesOutput(w http.ResponseWriter, r *http.Request, req anthropicMessagesRequest, raw json.RawMessage, start time.Time, out nativeOutput, passthrough bool) {
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
	// headersApplied is set once the upstream headers had their one chance
	// to go out; nothing can be added after the first byte of a response.
	// headersApplied 在上游响应头获得唯一一次发送机会后置位；响应的首个字节之后无法再追加响应头。
	headersApplied := false
	applyUpstreamHeader := func(header http.Header) {
		sent := started
		if out != nil {
			sent = out.streaming()
		}
		if headersApplied || sent {
			return
		}
		headersApplied = true
		for name, values := range runtime.SanitizeUpstreamHeader(header) {
			if _, own := w.Header()[name]; !own {
				w.Header()[name] = values
			}
		}
	}
	// upstreamFail answers with the upstream's own status, headers and error
	// document when the Agent captured them. A capability rejection keeps its
	// fixed recovery message, which clients match on.
	// upstreamFail 在 Agent 截获到上游应答时，以上游自己的状态码、响应头与错误文档应答。
	// 能力拒绝保留客户端据以匹配的固定恢复文案。
	upstreamFail := func(err error) bool {
		upstream := runtime.UpstreamOf(err)
		if upstream == nil {
			return false
		}
		applyUpstreamHeader(upstream.Header)
		if !runtime.ValidUpstreamErrorBody(upstream.Body) || runtime.MessagesRejectionMessage(err) != "" {
			return false
		}
		errType, message := runtime.UpstreamErrorDetail(upstream.Body)
		h.logger.Warn("native Messages upstream error passed through", "status", upstream.StatusCode, "error_type", errType)
		if w.Header().Get("Retry-After") == "" {
			setRetryAfter(w, err)
		}
		switch {
		case out != nil:
			out.fail(upstream.StatusCode, errType, errType, message)
		case started:
			_ = writeAnthropicSSE(w, "error", upstream.Body)
			_ = http.NewResponseController(w).Flush()
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(upstream.StatusCode)
			_, _ = w.Write(upstream.Body)
		}
		return true
	}
	ids := nativeResultIDs(req)
	unchanged := len(req.Safeguards) > 0 || passthrough
	if unchanged {
		ids = nil
	} // no live CLI consumes these results; this is a stateless native request / 无存活 CLI 接收结果，按无状态原生请求处理
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
		if upstreamFail(err) {
			return
		}
		status, errType, code, message := dispatchErrorDetails(h.logger, err)
		if w.Header().Get("Retry-After") == "" {
			setRetryAfter(w, err)
		}
		fail(status, errType, code, message)
	}
	identity, ok := IdentityFrom(r.Context())
	if !ok || identity.TenantID == "" || identity.KeyID == "" {
		fail(401, "authentication_error", "authentication_error", "native Messages requires authenticated API keys")
		return
	}
	if h.messagesSessions == nil && !unchanged {
		fail(503, "api_error", "session_unavailable", "native Messages requires shared Redis session coordination")
		return
	}
	if len(raw) > maxMessagesRequestBytes {
		fail(413, "invalid_request_error", "request_too_large", "native Messages request size limit exceeded")
		return
	}
	native := runtime.MessagesRequest{Model: req.Model, TenantID: identity.TenantID, KeyID: identity.KeyID, JSON: raw,
		APIVersion: r.Header.Get("Anthropic-Version"), BetaFeatures: strings.Join(r.Header.Values("Anthropic-Beta"), ","), Native: passthrough}
	if err := native.ValidateProtocol(); err != nil {
		fail(400, "invalid_request_error", "invalid_request_error", err.Error())
		return
	}
	// received reports whether a model event reached the client; started only
	// whether response headers went out, which a heartbeat may do first.
	// received 表示是否已有模型事件送达客户端；started 只表示响应头是否已发出，心跳可能先发出响应头。
	received := false
	var ping func() error
	if req.Stream {
		ping = func() error {
			if out != nil {
				err := out.keepAlive()
				started = out.streaming()
				return err
			}
			if !started {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				started = true
			}
			if err := writeAnthropicSSE(w, "ping", map[string]string{"type": "ping"}); err != nil {
				return err
			}
			return http.NewResponseController(w).Flush()
		}
	}
	type dispatched struct {
		stream    runtime.Stream[runtime.MessagesEvent]
		candidate scheduler.Candidate
		err       error
	}
	var record messagesession.Record
	var finish func(context.Context, bool) error
	var stream runtime.Stream[runtime.MessagesEvent]
	var err error
	committed := false
	resume := false
	if len(ids) > 0 {
		record, finish, err = h.messagesSessions.Claim(r.Context(), ids, identity.TenantID, identity.KeyID, req.Model)
		switch {
		case errors.Is(err, messagesession.ErrExpired):
			// Nobody continued this round in time, so its CLI is gone; the
			// round resumes in a new session that reads the results from the
			// transcript. Consumed or contested rounds still answer 409 below.
			// 这一回合没有在时限内被续接，其 CLI 已不存在；在新会话中从文本记录读取结果
			// 恢复该回合。已消费或被争用的回合仍在下面返回 409。
			resume = true
		case err != nil:
			continuationUnavailable()
			return
		default:
			defer func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
				defer cancel()
				_ = finish(ctx, committed)
			}()
			d := awaitAlive(h.clock, ping, func() dispatched {
				stream, err := h.sched.Messages(r.Context(), record.Candidate, native)
				return dispatched{stream: stream, err: err}
			})
			stream, err = d.stream, d.err
		}
	}
	if len(ids) == 0 || resume {
		d := awaitAlive(h.clock, ping, func() dispatched {
			stream, candidate, err := h.sched.MessagesStart(r.Context(), native)
			return dispatched{stream, candidate, err}
		})
		stream, err = d.stream, d.err
		candidate := d.candidate
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
	var originalContent []map[string]json.RawMessage
	var calls []string
	var usage runtime.Usage
	stopped := false
	total := 0
	protocolError := func(message string) {
		fail(502, "api_error", "upstream_protocol", message)
	}
	type recvResult struct {
		item runtime.MessagesEvent
		err  error
	}
	for {
		got := awaitAlive(h.clock, ping, func() recvResult {
			item, err := stream.Recv()
			return recvResult{item, err}
		})
		item, err := got.item, got.err
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
				h.recordUsageOn(r.Context(), record.Candidate.NodeID, usage, time.Since(start), endpoint, req.Model)
				return
			}
		}
		if err != nil {
			if !received {
				committed = committed || !messagesNotApplied(err)
				dispatchError(err)
			} else if !upstreamFail(err) {
				protocolError("native Messages stream failed")
			}
			return
		}
		committed = true
		applyUpstreamHeader(item.Header)
		total += len(item.JSON)
		if total > maxNativeResponseBytes {
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
				PartialJSON                     string          `json:"partial_json"`
				StopReason                      string          `json:"stop_reason"`
				StopSequence                    string          `json:"stop_sequence"`
				Safeguards                      json.RawMessage `json:"safeguard_results"`
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
			if unchanged {
				var raw struct {
					Block map[string]json.RawMessage `json:"content_block"`
				}
				_ = json.Unmarshal(item.JSON, &raw)
				originalContent = append(originalContent, raw.Block)
			}
			if event.Block.Type == "tool_use" {
				calls = append(calls, event.Block.ID)
			}
		case "content_block_delta":
			if event.Index < 0 || event.Index >= len(content) {
				protocolError("invalid native Messages block index")
				return
			}
			if unchanged && runtime.MergeMessagesCitation(originalContent[event.Index], item.JSON) != nil {
				protocolError("invalid native Messages citation")
				return
			}
			if unchanged && runtime.MergeMessagesCompaction(originalContent[event.Index], item.JSON) != nil {
				protocolError("invalid native Messages compaction")
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
			if unchanged && runtime.MergeMessagesResponseDelta(response, item.JSON) != nil {
				protocolError("invalid native Messages delta")
				return
			}
			if len(event.Delta.Safeguards) > 0 {
				response["safeguard_results"] = event.Delta.Safeguards
			}
			if event.Delta.StopSequence != "" {
				response["stop_sequence"] = event.Delta.StopSequence
			}
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
			if unchanged {
				usage, err = nativeMessagesBilledUsage(prior)
				if err != nil {
					protocolError("invalid native Messages usage")
					return
				}
			}
		case "message_stop":
			if !req.Stream {
				for _, block := range content {
					if len(block.Input) > 0 && !json.Valid(block.Input) {
						protocolError("native Messages ended with incomplete tool input")
						return
					}
				}
			}
			if len(calls) > 0 && !unchanged {
				record.IDs = calls
				if h.messagesSessions.Publish(r.Context(), record) != nil {
					fail(503, "api_error", "session_unavailable", "Messages continuation storage unavailable")
					return
				}
			}
			stopped = true
			response["content"] = content
			if unchanged {
				for i, block := range content {
					data, _ := json.Marshal(block)
					var accumulated map[string]json.RawMessage
					_ = json.Unmarshal(data, &accumulated)
					for key, value := range accumulated {
						if block.Type == "compaction" && key == "content" {
							continue // already merged from the native delta / 已由原生增量合并
						}
						originalContent[i][key] = value
					}
				}
				response["content"] = originalContent
			}
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
				if !received && out.streaming() {
					_, metric := out.endpoints()
					h.logTTFT(r, req.Model, record.Candidate.NodeID, start, true)
					h.metrics.TTFT(metric, time.Since(start))
					received = true
				}
				started = out.streaming()
				continue
			}
			if !started {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				started = true
			}
			if !received {
				h.logTTFT(r, req.Model, record.Candidate.NodeID, start, true)
				received = true
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

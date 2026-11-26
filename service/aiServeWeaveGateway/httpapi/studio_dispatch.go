package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"AIServeWeave/common/reqid"
	"AIServeWeave/service/aiServeWeaveGateway/flow"
)

// studioFlowDispatcher makes a flow's model calls through the Gateway's own
// Chat Completions handler, in process, exactly as a conversation turn does:
// model wait, native CLI chat, scheduling, usage recording and billing all
// apply. ctx carries the member's identity and scheduling access from the
// request that started the run.
//
// Each call is its own request as far as accounting is concerned: it gets a
// fresh request id, so the usage ledger, which deduplicates on it, records
// every call; and it passes the tenant's rate limit on its own, with its own
// token sink, rather than charging tokens to the request that started the
// run long after that request's quota lease was released.
//
// studioFlowDispatcher 经 Gateway 自己的 Chat Completions handler 在进程内进行编排的模型
// 调用，与对话的一轮完全相同：等待模型、原生 CLI 对话、调度、用量记录与计费都照常生效。
// ctx 携带启动运行的那个请求中成员的身份与调度权限。
//
// 就计量而言，每次调用都是一个独立的请求：它获得新的 request id，因此以它去重的用量账本
// 会记录每一次调用；它也单独经过租户的限流，使用自己的 token 计数，而不是在启动运行的
// 请求的配额租约早已释放之后，把 token 记到那个请求头上。
type studioFlowDispatcher struct {
	h *handlers
	// session is the X-Session-Id of every call, so the calls of one run
	// prefer the node that has their prompts cached.
	// session 是每次调用的 X-Session-Id，使同一运行的调用优先落到缓存了其提示的节点。
	session string
}

var _ flow.Dispatcher = studioFlowDispatcher{}

// Chat implements flow.Dispatcher.
func (d studioFlowDispatcher) Chat(ctx context.Context, call flow.ChatCall, delta func(string)) (flow.ChatReply, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	messages := make([]message, 0, 2)
	if call.System != "" {
		messages = append(messages, message{"system", call.System})
	}
	messages = append(messages, message{"user", call.Prompt})
	body := map[string]any{
		"model": call.Model, "messages": messages, "stream": true,
		"stream_options": map[string]bool{"include_usage": true},
	}
	if call.Temperature != nil {
		body["temperature"] = *call.Temperature
	}
	if call.MaxTokens != nil {
		body["max_tokens"] = *call.MaxTokens
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return flow.ChatReply{}, &flow.Error{Code: flow.CodeUpstream}
	}

	ctx = reqid.WithValue(ctx, reqid.New())
	tap := newChatTap(delta, nil)
	err = d.h.withQuota(ctx, func(ctx context.Context) {
		req := (&http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/v1/chat/completions"}, Header: http.Header{
			"Content-Type": {"application/json"}, "X-Session-Id": {d.session},
		}}).WithContext(ctx)
		req.Body, req.ContentLength = io.NopCloser(bytes.NewReader(encoded)), int64(len(encoded))
		d.h.chatCompletions(tap, req)
	})
	if err != nil {
		return flow.ChatReply{}, &flow.Error{Code: "rate_limited"}
	}

	reply := flow.ChatReply{Text: tap.content.String(), Usage: flow.Usage{PromptTokens: tap.usage.PromptTokens, CompletionTokens: tap.usage.CompletionTokens}}
	status, code := tap.outcome(ctx.Err() != nil)
	switch {
	case status == studioReplyComplete:
		return reply, nil
	case ctx.Err() != nil:
		return reply, ctx.Err()
	case status == studioReplyIncomplete:
		// The stream ended without finishing and nobody stopped it.
		// 流在没有完成的情况下结束了，而且没有人停止它。
		return reply, &flow.Error{Code: flow.CodeUpstream}
	default:
		return reply, &flow.Error{Code: code}
	}
}

// withQuota runs call under the tenant's rate limit, as rateLimit runs a
// request: a refusal is returned without running call, a limiter that cannot
// answer lets it through, and the tokens call records are charged when it
// returns. call always gets a fresh token sink, so it never writes to one
// belonging to another request.
//
// withQuota 在租户的限流下运行 call，与 rateLimit 运行一个请求的方式相同：被拒绝时直接
// 返回、不运行 call；限流器无法作答时放行；call 记录的 token 在其返回时扣除。call 总是
// 得到一个新的 token 计数，因此永远不会写入属于其他请求的计数。
func (h *handlers) withQuota(ctx context.Context, call func(context.Context)) error {
	sink := &usageSink{}
	ctx = context.WithValue(ctx, usageSinkKey{}, sink)
	identity, ok := IdentityFrom(ctx)
	if h.limiter == nil || !ok || identity.TenantID == "" || identity.Limits.Unlimited() {
		call(ctx)
		return nil
	}
	lease, err := h.limiter.Acquire(ctx, identity.TenantID, identity.Limits)
	if err != nil {
		if rejected, isRejection := asRejection(err); isRejection {
			h.metrics.RateLimited(rejected.Reason)
			return rejected
		}
		h.logger.Error("the rate limiter could not answer; letting the call through",
			slog.Any("error", err), slog.String("request_id", requestIDFrom(ctx)))
		h.metrics.LimiterUnavailable()
		call(ctx)
		return nil
	}
	defer func() { lease.Release(context.WithoutCancel(ctx), sink.tokens) }()
	call(ctx)
	return nil
}

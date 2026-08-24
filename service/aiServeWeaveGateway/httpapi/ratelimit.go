package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/ratelimit"
)

// This file enforces a tenant's quota on the way in, and charges what the
// request actually cost on the way out.
//
// Two things make that awkward enough to be worth stating. The cost of a
// request is not known when it is admitted — a chat completion's token count
// arrives with the response — so the charge happens at the end, through a sink
// the handlers write their usage into. And the enforcement has to sit inside
// authentication, because there is no tenant to enforce against until the key
// has been resolved.
//
// 本文件在入口处执行租户的配额，并在出口处按请求的实际代价扣费。
//
// 有两件事让这件事别扭到值得写下来。请求被接纳时它的代价还不知道——一次 chat
// completion 的 token 数随响应一起到达——因此扣费发生在末尾，经由一个各处理器把用量
// 写进去的 sink。而执行必须坐在鉴权内侧，因为在 key 被解析出来之前，根本没有可供执行
// 的租户。

// usageSink collects what one request cost, for the charge at its end. It is
// carried on the request context rather than returned, because the handlers
// that know the cost are several layers below the middleware that charges it.
//
// usageSink 收集一个请求的代价，供其末尾扣费使用。它挂在请求 context 上而不是被返回，
// 因为知道代价的那些处理器，比扣费的那个中间件低好几层。
type usageSink struct {
	tokens int
}

type usageSinkKey struct{}

// recordUsage records a backend-reported usage against both the metrics and,
// when the request is subject to a quota, the tenant's token allowance. Every
// handler that learns a token count calls it instead of the metrics recorder
// directly, so a new endpoint cannot start serving traffic that is metered but
// never billed.
//
// recordUsage 把后端上报的用量同时记入指标，以及（当该请求受配额约束时）租户的 token
// 额度。每个得知 token 数的处理器都调用它而不是直接调用指标记录器，这样新端点就不会
// 开始承载「有度量却从不计费」的流量。
func (h *handlers) recordUsage(ctx context.Context, usage runtime.Usage, elapsed time.Duration) {
	h.metrics.Usage(usage, elapsed)
	if sink, ok := ctx.Value(usageSinkKey{}).(*usageSink); ok {
		sink.tokens += usage.TotalTokens
	}
}

// rateLimit wraps next with quota enforcement. It is a no-op for a request
// with no identity: an unauthenticated deployment has no tenant to bill, and
// inventing one would mean every local development request shared a single
// bucket.
//
// rateLimit 用配额执行包裹 next。对没有身份的请求它什么都不做：未启用认证的部署没有
// 可记账的租户，而凭空造一个会让所有本地开发请求共用同一个桶。
func (h *handlers) rateLimit(next http.Handler) http.Handler {
	if h.limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok || identity.TenantID == "" || identity.Limits.Unlimited() {
			next.ServeHTTP(w, r)
			return
		}

		lease, err := h.limiter.Acquire(r.Context(), identity.TenantID, identity.Limits)
		if err != nil {
			if rejected, isRejection := asRejection(err); isRejection {
				h.metrics.RateLimited(rejected.Reason)
				writeRateLimited(w, rejected)
				return
			}
			// The limiter itself could not answer. Failing the request would
			// hand every caller a 5xx the moment the limiter's backing store
			// blinks, so the request is let through and the failure is logged:
			// a quota briefly unenforced is a smaller harm than an outage, and
			// this is the choice the Limiter contract leaves to the caller.
			//
			// 限流器自身无法作答。让请求失败，就会在限流器的后端存储一眨眼的工夫里
			// 给每个调用方一个 5xx，因此请求被放行、失败被记录：配额短暂失去执行，
			// 比一次服务中断危害更小，而这正是 Limiter 契约留给调用方的选择。
			h.logger.Error("the rate limiter could not answer; letting the request through",
				slog.Any("error", err),
				slog.String("request_id", requestIDFrom(r.Context())))
			h.metrics.LimiterUnavailable()
			next.ServeHTTP(w, r)
			return
		}

		sink := &usageSink{}
		r = r.WithContext(context.WithValue(r.Context(), usageSinkKey{}, sink))
		// Release runs on the way out whatever happened, including a panic
		// unwinding through here: a concurrency slot that is only returned on
		// the happy path is a slot that leaks on the unhappy one.
		//
		// 无论发生什么，Release 都在出口处运行，包括 panic 从这里穿过：一个只在顺利
		// 路径上归还的并发槽，在不顺利的路径上就是泄漏。
		defer func() {
			// The request's own context may already be cancelled — a client
			// that hung up — and the charge must still be recorded, so the
			// release does not inherit that cancellation.
			//
			// 请求自身的 context 可能已被取消——调用方挂断了——而这笔扣费仍然必须
			// 记上，因此释放不继承那次取消。
			lease.Release(context.WithoutCancel(r.Context()), sink.tokens)
		}()
		next.ServeHTTP(w, r)
	})
}

// asRejection narrows an Acquire error to a limit rejection.
//
// asRejection 把一个 Acquire 错误收窄为「被限制拒绝」。
func asRejection(err error) (*ratelimit.RejectedError, bool) {
	var rejected *ratelimit.RejectedError
	if errors.As(err, &rejected) {
		return rejected, true
	}
	return nil, false
}

// writeRateLimited answers a rejected request. Retry-After carries the
// limiter's own computed wait, so a well-behaved client retries once at the
// right moment instead of polling and turning one rejection into a storm.
//
// writeRateLimited 应答一个被拒绝的请求。Retry-After 携带限流器自己算出的等待时长，
// 因此守规矩的客户端只会在正确的时刻重试一次，而不是靠轮询把一次拒绝变成一场风暴。
func writeRateLimited(w http.ResponseWriter, rejected *ratelimit.RejectedError) {
	seconds := int(rejected.RetryAfter.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_error", string(rejected.Reason),
		"this tenant is over its "+string(rejected.Reason)+" limit; retry after "+strconv.Itoa(seconds)+"s")
}

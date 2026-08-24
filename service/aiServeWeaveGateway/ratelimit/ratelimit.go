// Package ratelimit enforces a tenant's quota.Limits on the Gateway's request
// path.
//
// It is an interface with two implementations because the two answer different
// questions. Memory is exact within one replica and wrong across a fleet: N
// replicas each admit the full allowance, so a tenant limited to 60/min gets
// 60N. Redis is exact across the fleet and costs a round trip per request.
// Which is right depends on the deployment, so the choice is configuration
// rather than a decision this package makes for everyone.
//
// Enforcement is deliberately not queueing. A request over the limit is
// rejected immediately with a retry hint, matching the Gateway's standing
// no-queue property: holding it would consume the very capacity the limit
// exists to protect, and the caller learns nothing until it eventually times
// out.
//
// ratelimit 包在 Gateway 的请求路径上执行租户的 quota.Limits。
//
// 它是一个接口加两个实现，因为两者回答的是不同的问题。Memory 在单副本内精确、在整个
// 集群上失真：N 个副本各自放行完整额度，因此一个限 60/分钟的租户实际拿到 60N。Redis
// 在整个集群上精确，代价是每个请求一次往返。哪一个正确取决于部署方式，所以这个选择
// 是配置项，而不是本包替所有人做的决定。
//
// 执行刻意不排队。超限的请求立刻被拒并附带重试提示，这与 Gateway 一贯的「不排队」
// 性质一致：把它挂住，消耗的正是限制所要保护的那份容量，而调用方在最终超时之前
// 什么也学不到。
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"AIServeWeave/common/quota"
)

// Reason names which dimension rejected a request. The set is closed so it can
// be a metric label without a caller widening it.
//
// Reason 指出是哪个维度拒绝了请求。集合是封闭的，因此它可以做指标标签，而不会被
// 调用方撑开。
type Reason string

const (
	ReasonRequests    Reason = "requests_per_minute"
	ReasonTokens      Reason = "tokens_per_minute"
	ReasonConcurrency Reason = "max_concurrent"
)

// RejectedError is a request refused by a limit. RetryAfter is how long the
// caller should wait before the same request would be admitted — a real
// figure computed from the bucket's refill rate, not a fixed constant, so a
// well-behaved client backs off by the right amount instead of guessing.
//
// RejectedError 是被某个限制拒绝的请求。RetryAfter 是调用方在同一请求能被接纳之前
// 应当等待的时长——它由桶的补充速率算出，是真实数值而非固定常量，这样守规矩的客户端
// 就能按正确的量退避，而不是靠猜。
type RejectedError struct {
	Reason     Reason
	RetryAfter time.Duration
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("ratelimit: rejected by %s, retry after %s", e.Reason, e.RetryAfter)
}

// Lease is one admitted request's hold on the tenant's allowance. Release must
// be called exactly once per successful Acquire — a lease that is never
// released holds a concurrency slot until the implementation's own reaper
// takes it back, and Memory has no reaper.
//
// Lease 是一个被接纳的请求对租户额度的占用。每次成功的 Acquire 必须恰好调用一次
// Release——从不释放的租约会一直占着并发槽，直到实现自身的回收器把它收回，而 Memory
// 没有回收器。
type Lease interface {
	// Release returns the concurrency slot and charges tokens against the
	// token bucket. tokens is what the backend reported for this request; zero
	// charges nothing, which is the right answer for an endpoint that produced
	// no tokens rather than one that produced an unknown number.
	//
	// Release 归还并发槽，并按 tokens 扣减 token 桶。tokens 是后端为本次请求上报的
	// 数量；为零时不扣减，这对「没有产出 token」的端点是正确答案，而不是对「产出了
	// 未知数量」的端点。
	Release(ctx context.Context, tokens int)
}

// Limiter admits or rejects one request against a tenant's limits.
//
// Limiter 依据租户的限制接纳或拒绝一个请求。
type Limiter interface {
	// Acquire admits a request, or returns a *RejectedError naming the
	// dimension that refused it. An error that is not a *RejectedError means
	// the limiter itself could not answer; the caller decides whether that
	// fails the request or lets it through, since that is a policy question
	// this package must not settle silently.
	//
	// Acquire 接纳一个请求，或返回一个点名了拒绝维度的 *RejectedError。非
	// *RejectedError 的错误表示限流器本身无法作答；由调用方决定这是让请求失败还是
	// 放行，因为那是一个策略问题，本包不得默默替它决定。
	Acquire(ctx context.Context, tenantID string, limits quota.Limits) (Lease, error)
}

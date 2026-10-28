package scheduler

import (
	"context"
	"hash/fnv"

	"AIServeWeave/common/runtime"
)

// cooledCandidates collects the candidates a dispatch loop skipped because
// they are still cooling down after a rate limit, keeping the one whose quota
// comes back first. When every candidate was skipped, that one becomes the
// request's answer: rate_limited with the shortest RetryAfter, which tells a
// client both what happened and when to come back.
//
// cooledCandidates 收集派发循环因限流冷却尚未结束而跳过的候选，保留额度最早恢复的那一个。
// 所有候选都被跳过时，它就成为这次请求的回答：带最短 RetryAfter 的 rate_limited，同时
// 告诉客户端发生了什么、什么时候再来。
type cooledCandidates struct {
	earliest *runtime.RuntimeError
}

// skip reports whether c is cooling down and should not be dispatched to, and
// remembers it if so.
//
// skip 报告 c 是否正在冷却、不应派发，若是则记下它。
func (s *Scheduler) skipCooling(c Candidate, cooled *cooledCandidates) bool {
	remaining, ok := s.breakers.limited(c, s.clock.Now())
	if !ok {
		return false
	}
	if cooled.earliest == nil || remaining < cooled.earliest.RetryAfter {
		cooled.earliest = &runtime.RuntimeError{
			Code:       runtime.ErrorRateLimited,
			RuntimeID:  c.RuntimeID,
			Kind:       c.Kind,
			Operation:  "schedule",
			Message:    "every candidate is cooling down after a rate limit",
			Retryable:  true,
			RetryAfter: remaining,
		}
	}
	return true
}

// or returns lastErr when a dispatch actually failed, and the cooling error
// only when nothing was dispatched at all: a real failure is what happened to
// this request, a cooldown is merely why the rest were not tried.
//
// or 在确有派发失败时返回 lastErr，只有一次都没有派发时才返回冷却错误：真实的失败才是
// 这次请求实际遇到的事，冷却只是其余候选没有被尝试的原因。
func (c *cooledCandidates) or(lastErr error) error {
	if lastErr != nil || c.earliest == nil {
		return lastErr
	}
	return c.earliest
}

// sessionKeyContext is the context key WithSessionKey stores under.
// sessionKeyContext 是 WithSessionKey 存放所用的 context 键。
type sessionKeyContext struct{}

// WithSessionKey returns ctx carrying key, the caller's own name for the
// conversation a request belongs to (a client session header, a
// prompt_cache_key). With Config.SessionAffinity on, requests carrying the
// same key are ranked by rendezvous hashing of it, so one conversation keeps
// landing on the same node and runtime — and so on the same CLI account,
// whose upstream prompt cache it has already warmed. An empty key leaves ctx
// unchanged. Only a hash of the key is ever kept; callers should still scope
// it to the tenant so two tenants choosing the same name do not share a
// placement.
//
// WithSessionKey 返回携带 key 的 ctx。key 是调用方给请求所属对话起的名字（客户端的会话
// 请求头、prompt_cache_key）。开启 Config.SessionAffinity 时，携带同一 key 的请求按它的
// rendezvous 哈希排序，使一段对话持续落在同一个节点与运行时上——也就落在同一个 CLI 账户
// 上，它的上游提示缓存已经被这段对话预热过。key 为空时原样返回 ctx。只保留 key 的哈希；
// 调用方仍应按租户限定它，免得两个租户恰好取了同一个名字而共用同一个落点。
func WithSessionKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	h := fnv.New64a()
	h.Write([]byte(key))
	return context.WithValue(ctx, sessionKeyContext{}, h.Sum64())
}

// sessionAffinity returns the affinity of the session key on ctx, or the zero
// affinity when the feature is off or the request carries none.
//
// sessionAffinity 返回 ctx 上会话 key 的亲和性；功能关闭或请求没有携带时返回零值。
func (s *Scheduler) sessionAffinity(ctx context.Context) affinity {
	if !s.sessionAffinityOn {
		return affinity{}
	}
	hash, ok := ctx.Value(sessionKeyContext{}).(uint64)
	if !ok {
		return affinity{}
	}
	return affinity{hash: hash, ok: true, session: true}
}

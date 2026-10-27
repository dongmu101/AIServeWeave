package scheduler

import (
	"context"
	"fmt"

	"AIServeWeave/common/modelaccess"
)

// ErrModelNotAllowed is returned when the caller's common/modelaccess policy
// excludes the requested model. It wraps ErrNoCapableNode, so every front door
// that already maps "no node serves this model" to a not-found answers it the
// same way: a model a tenant may not use must look exactly like a model that
// does not exist, or the refusal itself tells the tenant what is deployed.
//
// ErrModelNotAllowed 在调用方的 common/modelaccess 授权排除了所请求模型时返回。它
// 包装了 ErrNoCapableNode，因此每个已经把「没有节点能服务这个模型」映射成 not-found
// 的前门都会以同样方式作答：租户不能使用的模型，必须看起来与不存在的模型完全一样，
// 否则拒绝本身就会告诉租户部署了什么。
var ErrModelNotAllowed = fmt.Errorf("scheduler: the caller may not use this model: %w", ErrNoCapableNode)

// modelAccessKey is the context key a model access policy is stored under.
//
// modelAccessKey 是模型授权在 context 中存放所用的键。
type modelAccessKey struct{}

// WithModelAccess returns ctx carrying policy, which every dispatch made with
// that context is held to. The policy travels on the context rather than as a
// parameter because the scheduler is where each request's model is resolved,
// so enforcing it here covers every front door, current and future, without
// each one having to remember to.
//
// WithModelAccess 返回携带 policy 的 ctx，用这个 context 发起的每一次派发都受它约束。
// 授权放在 context 上而不是作为参数传递，是因为调度器正是每个请求的模型被解析的地方，
// 在这里执行就覆盖了现在和将来的每个前门，而不必让每个前门各自记得去做。
func WithModelAccess(ctx context.Context, policy modelaccess.Policy) context.Context {
	return context.WithValue(ctx, modelAccessKey{}, policy)
}

// modelAllowed reports whether ctx's policy allows model. A context without a
// policy is unrestricted.
//
// modelAllowed 报告 ctx 上的授权是否允许 model。没有授权的 context 不受限制。
func modelAllowed(ctx context.Context, model string) bool {
	policy, ok := ctx.Value(modelAccessKey{}).(modelaccess.Policy)
	return !ok || policy.Allows(model)
}

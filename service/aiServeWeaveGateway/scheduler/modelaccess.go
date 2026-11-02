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

// ownNodesOnlyKey marks a context whose dispatch admitModel limited to the
// caller's own tenant nodes.
//
// ownNodesOnlyKey 标记一个已被 admitModel 限定为只能派发到调用方自有节点的 context。
type ownNodesOnlyKey struct{}

// admitModel decides whether ctx's caller may request model. A model the
// policy allows is admitted unchanged. A model it excludes is still admitted
// for a caller with a tenant, but the returned context limits every node
// choice to that tenant's own nodes (common/nodeaccess.OwnerOf): a merchant
// may serve itself any model from a node it runs, and the platform's model
// access lists only govern what the platform's nodes provide. Without a
// tenant the excluded model is refused.
//
// admitModel 判断 ctx 上的调用方能否请求 model。授权允许的模型原样放行。授权排除的模型
// 对有租户身份的调用方仍然放行，但返回的 context 把每一次节点选择限定在该租户的自有节点
// （common/nodeaccess.OwnerOf）：商户可以用自己运行的节点为自己提供任何模型，平台模型
// 授权只管平台节点提供什么。没有租户身份时拒绝被排除的模型。
func admitModel(ctx context.Context, model string) (context.Context, bool) {
	if modelAllowed(ctx, model) {
		return ctx, true
	}
	access, ok := ctx.Value(nodeAccessKey{}).(nodeAccess)
	if !ok || access.tenantID == "" {
		return ctx, false
	}
	return context.WithValue(ctx, ownNodesOnlyKey{}, true), true
}

// ownNodesOnly reports whether admitModel limited ctx to the caller's own
// nodes.
//
// ownNodesOnly 报告 admitModel 是否已把 ctx 限定为调用方的自有节点。
func ownNodesOnly(ctx context.Context) bool {
	only, _ := ctx.Value(ownNodesOnlyKey{}).(bool)
	return only
}

// noCandidates is the error for an empty candidate list. Under ownNodesOnly
// it is ErrModelNotAllowed, so a model the policy excludes and no own node
// serves is refused exactly as before.
//
// noCandidates 是候选列表为空时的错误。在 ownNodesOnly 下为 ErrModelNotAllowed，使授权
// 排除且没有自有节点提供的模型，与以前一样被拒绝。
func noCandidates(ctx context.Context) error {
	if ownNodesOnly(ctx) {
		return ErrModelNotAllowed
	}
	return ErrNoCapableNode
}

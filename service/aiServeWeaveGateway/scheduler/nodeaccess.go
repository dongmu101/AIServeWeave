package scheduler

import (
	"context"
	"fmt"

	"AIServeWeave/common/nodeaccess"
)

// ErrNodeNotAllowed is returned when a dispatch names a node the caller's
// tenant may not use under common/nodeaccess — a pinned continuation or a
// pre-selected candidate whose node became dedicated to another tenant, or a
// node another tenant owns. It wraps ErrNoCapableNode for the reason
// ErrModelNotAllowed does: a node a tenant may not use must look exactly like
// one that serves nothing.
//
// ErrNodeNotAllowed 在一次派发点名了调用方租户按 common/nodeaccess 不能使用的节点时
// 返回——一次固定节点的续接，或一个预先选定、而其节点已变为其他租户专属的候选，或另一
// 商户自有的节点。它包装 ErrNoCapableNode，理由与 ErrModelNotAllowed 相同：租户不能
// 使用的节点，必须看起来与什么都不提供的节点完全一样。
var ErrNodeNotAllowed = fmt.Errorf("scheduler: the caller may not use this node: %w", ErrNoCapableNode)

// nodeAccessKey is the context key a nodeAccess is stored under.
//
// nodeAccessKey 是 nodeAccess 在 context 中存放所用的键。
type nodeAccessKey struct{}

// nodeAccess is the caller's tenant and its dedicated-node policy, the two
// inputs nodeaccess.Permits needs.
//
// nodeAccess 是调用方租户及其专属节点 policy，即 nodeaccess.Permits 需要的两个输入。
type nodeAccess struct {
	tenantID string
	policy   nodeaccess.Policy
}

// WithNodeAccess returns ctx carrying the caller's tenant and dedicated-node
// policy, which every selection and dispatch made with that context is held
// to: a node another tenant owns, or one dedicated to other tenants, is never
// chosen. It travels on the context for the reason WithModelAccess gives. A
// context without one — a background job recovery, or a deployment without a
// control plane — is unrestricted.
//
// WithNodeAccess 返回携带调用方租户及其专属节点 policy 的 ctx，用这个 context 发起的
// 每一次选择与派发都受它约束：另一商户自有的节点、或专属于其他租户的节点，都不会被选中。
// 它放在 context 上的理由与 WithModelAccess 相同。不带它的 context——后台的 Job 恢复，
// 或没有控制面的部署——不受限制。
func WithNodeAccess(ctx context.Context, tenantID string, policy nodeaccess.Policy) context.Context {
	return context.WithValue(ctx, nodeAccessKey{}, nodeAccess{tenantID: tenantID, policy: policy})
}

// nodeAllowed reports whether ctx's caller may use nodeID.
//
// nodeAllowed 报告 ctx 上的调用方是否可以使用 nodeID。
func nodeAllowed(ctx context.Context, nodeID string) bool {
	access, ok := ctx.Value(nodeAccessKey{}).(nodeAccess)
	return !ok || nodeaccess.Permits(access.tenantID, access.policy, nodeID)
}

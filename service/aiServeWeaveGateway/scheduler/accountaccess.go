package scheduler

import (
	"AIServeWeave/common/accountaccess"
	"context"
)

type accountAccessKey struct{}

// WithAccountAccess applies exclusive runtime leases to selection and pinned
// continuations. Expiry is evaluated at use, even with a cached verification.
func WithAccountAccess(ctx context.Context, policy accountaccess.Policy) context.Context {
	return context.WithValue(ctx, accountAccessKey{}, policy)
}

func (s *Scheduler) accountAllowed(ctx context.Context, nodeID, runtimeID string) bool {
	p, _ := ctx.Value(accountAccessKey{}).(accountaccess.Policy)
	if access, ok := ctx.Value(nodeAccessKey{}).(nodeAccess); ok {
		return p.Permits(access.tenantID, access.policy, nodeID, runtimeID, s.clock.Now())
	}
	return p.Allows(nodeID, runtimeID, s.clock.Now())
}

func (s *Scheduler) candidateAllowed(ctx context.Context, c Candidate) bool {
	return nodeAllowed(ctx, c.NodeID) && s.accountAllowed(ctx, c.NodeID, c.RuntimeID)
}

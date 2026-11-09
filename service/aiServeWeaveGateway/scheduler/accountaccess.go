package scheduler

import (
	"context"
	"AIServeWeave/common/accountaccess"
)

type accountAccessKey struct{}

// WithAccountAccess applies exclusive runtime leases to selection and pinned
// continuations. Expiry is evaluated at use, even with a cached verification.
func WithAccountAccess(ctx context.Context, policy accountaccess.Policy) context.Context {
	return context.WithValue(ctx,accountAccessKey{},policy)
}

func (s *Scheduler) accountAllowed(ctx context.Context,nodeID,runtimeID string) bool {
	p,ok := ctx.Value(accountAccessKey{}).(accountaccess.Policy)
	return !ok || p.Allows(nodeID,runtimeID,s.clock.Now())
}

func (s *Scheduler) candidateAllowed(ctx context.Context,c Candidate) bool {
	return nodeAllowed(ctx,c.NodeID) && s.accountAllowed(ctx,c.NodeID,c.RuntimeID)
}

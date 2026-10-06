package scheduler

import (
	"AIServeWeave/common/runtime"
	"context"
)

// MessagesCandidates returns only nodes supporting native Anthropic Messages.
// MessagesCandidates 仅返回支持原生 Anthropic Messages 的节点。
func (s *Scheduler) MessagesCandidates(model string) []Candidate {
	return s.candidates(model, runtime.CapabilityMessages)
}

// Messages dispatches once to the selected node, including all tool continuations.
// Messages 仅向所选节点分派一次，所有工具续接也使用该节点。
func (s *Scheduler) Messages(ctx context.Context, c Candidate, req runtime.MessagesRequest) (runtime.Stream[runtime.MessagesEvent], error) {
	req.Model = c.Model
	return s.server.Runtime(c.NodeID, c.RuntimeID).Messages(ctx, req)
}

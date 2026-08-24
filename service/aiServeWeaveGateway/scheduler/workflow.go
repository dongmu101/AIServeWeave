package scheduler

import (
	"context"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
)

// SubmitWorkflow queues req on a workflow-capable node and returns the run
// handle together with the candidate that accepted it. The caller must keep
// that candidate: every later question about this run — status, events,
// cancellation, artifacts — goes back to the same node, because the run only
// exists in that one ComfyUI's queue.
//
// SubmitWorkflow 把 req 排入某个具备工作流能力的节点，返回 run 句柄与接受它的候选。
// 调用方必须保存这个候选：此后关于这次运行的一切问题——状态、事件、取消、产物——都要
// 回到同一个节点，因为这次运行只存在于那一个 ComfyUI 的队列里。
func (s *Scheduler) SubmitWorkflow(ctx context.Context, req runtime.WorkflowRequest) (runtime.WorkflowRun, Candidate, error) {
	candidates := s.workflowCandidates(runtime.CapabilityWorkflowExecution)
	s.metrics.Selection(runtime.CapabilityWorkflowExecution, len(candidates))
	if len(candidates) == 0 {
		return runtime.WorkflowRun{}, Candidate{}, ErrNoCapableNode
	}
	var lastErr error
	for _, c := range candidates {
		run, err := s.server.Runtime(c.NodeID, c.RuntimeID).Submit(ctx, req)
		s.breakers.record(c, err, s.clock.Now())
		s.metrics.Dispatch(c, err)
		if err == nil {
			return run, c, nil
		}
		lastErr = err
		if !submitRetryable(err) {
			return runtime.WorkflowRun{}, c, err
		}
		s.metrics.Retry(runtime.CapabilityWorkflowExecution)
	}
	return runtime.WorkflowRun{}, Candidate{}, lastErr
}

// WorkflowStatus asks c for the state of runID. It takes the candidate rather
// than choosing one: a run id is a single ComfyUI's prompt_id, so re-selecting
// would ask a node that has never heard of it.
//
// WorkflowStatus 向 c 询问 runID 的状态。它接受候选而不是自己选：run id 是某一个
// ComfyUI 的 prompt_id，重新选择等于去问一个从未听说过它的节点。
func (s *Scheduler) WorkflowStatus(ctx context.Context, c Candidate, runID string) (runtime.WorkflowStatus, error) {
	status, err := s.server.Runtime(c.NodeID, c.RuntimeID).Status(ctx, runID)
	s.breakers.record(c, err, s.clock.Now())
	s.metrics.Dispatch(c, err)
	return status, err
}

// WorkflowEvents opens the run's normalized event stream on c. Like
// WorkflowStatus it takes the candidate rather than choosing one, and for a
// stronger reason: a subscription is to one ComfyUI's WebSocket, so there is
// no other node on which this run has a stream to open.
//
// The stream is returned unread. Unlike ChatStream there is no first-event
// peek, because there is nothing to decide with it: a failure here cannot be
// retried elsewhere whether or not anything has been delivered yet.
//
// WorkflowEvents 在 c 上打开该次运行的归一化事件流。它和 WorkflowStatus 一样接受候选
// 而不是自己选，而且理由更强：订阅连的是某一个 ComfyUI 的 WebSocket，别的节点上根本
// 不存在这次运行的流可开。
//
// 返回的流未被读取。与 ChatStream 不同，这里不预读首个事件，因为预读了也无事可决：
// 无论是否已经送出过内容，这里的失败都不能换节点重试。
func (s *Scheduler) WorkflowEvents(ctx context.Context, c Candidate, runID string) (runtime.Stream[runtime.WorkflowEvent], error) {
	stream, err := s.server.Runtime(c.NodeID, c.RuntimeID).Subscribe(ctx, runID)
	s.breakers.record(c, err, s.clock.Now())
	s.metrics.Dispatch(c, err)
	return stream, err
}

// CancelWorkflow asks c to interrupt runID. It takes the candidate for the
// same reason Status and Events do, and it does not retry: an interrupt that
// failed on one node has no meaning on another, and re-sending it to the same
// node would risk interrupting whatever ComfyUI is running by then.
//
// CancelWorkflow 请求 c 中断 runID。它接受候选的理由与 Status、Events 相同，而且不
// 重试：在一个节点上失败的中断，对另一个节点毫无意义；而重发给同一个节点，则可能中断
// 那时 ComfyUI 正在跑的任何东西。
func (s *Scheduler) CancelWorkflow(ctx context.Context, c Candidate, runID string) error {
	err := s.server.Runtime(c.NodeID, c.RuntimeID).Cancel(ctx, runID)
	s.breakers.record(c, err, s.clock.Now())
	s.metrics.Dispatch(c, err)
	return err
}

// WorkflowArtifacts lists what a run produced, on the node that ran it. The
// reply names the artifacts and carries none of their bytes, so it travels
// like a status query.
//
// WorkflowArtifacts 在运行它的那个节点上列举一次运行产出了什么。回复只点名产物、
// 不携带它们的任何字节，因此它像一次状态查询那样传输。
func (s *Scheduler) WorkflowArtifacts(ctx context.Context, c Candidate, runID string) ([]runtime.ArtifactRef, error) {
	refs, err := s.server.Runtime(c.NodeID, c.RuntimeID).Artifacts(ctx, runID)
	s.breakers.record(c, err, s.clock.Now())
	s.metrics.Dispatch(c, err)
	return refs, err
}

// OpenArtifact opens one artifact's body on the node that produced it. The
// body is read as the caller reads it — nothing here holds an artifact whole,
// which is the point of returning the stream rather than the bytes.
//
// OpenArtifact 在产出该产物的节点上打开它的响应体。响应体随调用方的读取而读取——这里
// 没有任何东西会完整持有一个产物，这正是返回流而不是字节的意义。
func (s *Scheduler) OpenArtifact(ctx context.Context, c Candidate, ref runtime.ArtifactRef) (runtime.Artifact, error) {
	artifact, err := s.server.Runtime(c.NodeID, c.RuntimeID).OpenArtifact(ctx, ref)
	s.breakers.record(c, err, s.clock.Now())
	s.metrics.Dispatch(c, err)
	return artifact, err
}

// submitRetryable reports whether a failed submit can be re-sent to another
// node. It is deliberately narrower than retryable(): the question is not
// "might this succeed elsewhere" but "is it certain the backend never saw
// it". Only a failure raised on the way in qualifies — no idle slot, a
// connection that never came up, a runtime already closed. An upstream error
// or a timeout leaves the submit's fate unknown, and a workflow submitted
// twice produces a second generation nobody asked for, on hardware someone
// is paying for.
//
// submitRetryable 报告一次失败的提交能否改发给另一个节点。它刻意比 retryable() 更窄：
// 要问的不是「换个地方会不会成功」，而是「能否确定后端从未见过它」。只有在去程上抛出
// 的失败才算——没有空闲槽、连接根本没建起来、runtime 已关闭。上游错误与超时会让这次
// 提交的下场变成未知，而一个被提交两次的工作流会产出第二次没人要的生成，还占着有人
// 在为之付费的硬件。
func submitRetryable(err error) bool {
	code, ok := errorCode(err)
	if !ok {
		return false
	}
	switch code {
	case runtime.ErrorBackpressure, runtime.ErrorRateLimited, runtime.ErrorConnection, runtime.ErrorClosed:
		return true
	default:
		return false
	}
}

// workflowCandidates ranks the nodes able to run workflows. Unlike an
// inference request there is no model to match: a ComfyUI instance advertises
// its capability at the runtime level, and which checkpoints a given graph
// needs is a property of the template, not of the request's model field.
//
// workflowCandidates 对能运行工作流的节点排序。与推理请求不同，这里没有模型要匹配：
// ComfyUI 实例在 runtime 这一层声明能力，而某张图需要哪些 checkpoint 是模板的属性，
// 不是请求 model 字段的属性。
func (s *Scheduler) workflowCandidates(cap runtime.Capability) []Candidate {
	// An empty target matches every node: a workflow request carries no model
	// to route, so there is nothing for a routing rule to select on.
	//
	// 空 target 匹配所有节点：工作流请求不携带可路由的模型，因此路由规则无从选择。
	return s.pickBy(routing.Target{}, func(snap runtime.Snapshot) bool {
		return snap.Discovery.Capabilities.Require(cap) == nil
	})
}

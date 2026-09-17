package scheduler

import (
	"context"
	"io"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
)

// SubmitWorkflow queues req on a workflow-capable node and returns the run
// handle together with the candidate that accepted it. The caller must keep
// that candidate: every later question about this run — status, events,
// cancellation, artifacts — goes back to the same node, because the run only
// exists in that one ComfyUI's queue.
//
// When every current candidate answers with a retryable backpressure-style
// failure — every node is busy right now, not permanently unable to serve
// this request — and the Scheduler was constructed with Config.QueueMaxWait
// positive, SubmitWorkflow does not give up immediately. It waits up to that
// long, re-polling the candidate set, before returning the failure
// (STATUS.md's P2 bounded task queueing; see queueSubmitWorkflow). The
// default (QueueMaxWait zero) keeps today's behavior: an exhausted candidate
// set fails the call right away.
//
// SubmitWorkflow 把 req 排入某个具备工作流能力的节点，返回 run 句柄与接受它的候选。
// 调用方必须保存这个候选：此后关于这次运行的一切问题——状态、事件、取消、产物——都要
// 回到同一个节点，因为这次运行只存在于那一个 ComfyUI 的队列里。
//
// 当每一个当前候选都以可重试的背压类失败作答——每个节点此刻都忙，不是永久无法服务
// 这次请求——且 Scheduler 构造时 Config.QueueMaxWait 为正值，SubmitWorkflow 不会
// 立即放弃。它会等待至多这么久、期间重新轮询候选集，再把失败返回给调用方
// （STATUS.md 的 P2 有界任务排队，见 queueSubmitWorkflow）。默认值（QueueMaxWait
// 为零）保持今天的行为：候选集耗尽立即让这次调用失败。
func (s *Scheduler) SubmitWorkflow(ctx context.Context, req runtime.WorkflowRequest) (runtime.WorkflowRun, Candidate, error) {
	run, c, err, exhausted := s.attemptSubmitWorkflow(ctx, req)
	if err == nil || !exhausted || s.queueSlots == nil {
		return run, c, err
	}
	return s.queueSubmitWorkflow(ctx, req, err)
}

// attemptSubmitWorkflow runs exactly one pass over the current candidate
// set, exactly what SubmitWorkflow did before queueing existed. exhausted
// reports whether every tried candidate failed with a retryable error — the
// only condition queueSubmitWorkflow will wait on. It is false both when no
// candidate exists at all (ErrNoCapableNode) and when a candidate failed
// with a non-retryable error, because waiting cannot change either outcome.
//
// attemptSubmitWorkflow 对当前候选集跑恰好一轮——就是排队功能出现之前 SubmitWorkflow
// 所做的事。exhausted 报告是否每一个被尝试的候选都以可重试错误失败——这是
// queueSubmitWorkflow 唯一会等待的条件。候选完全不存在（ErrNoCapableNode）与候选以
// 不可重试错误失败这两种情形下它都是 false，因为等待无法改变这两种结果中的任何一个。
func (s *Scheduler) attemptSubmitWorkflow(ctx context.Context, req runtime.WorkflowRequest) (run runtime.WorkflowRun, c Candidate, err error, exhausted bool) {
	candidates := s.workflowCandidates(runtime.CapabilityWorkflowExecution, req.MinGPUMemoryBytes)
	s.metrics.Selection(runtime.CapabilityWorkflowExecution, len(candidates))
	if len(candidates) == 0 {
		return runtime.WorkflowRun{}, Candidate{}, ErrNoCapableNode, false
	}
	var lastErr error
	for _, cand := range candidates {
		attemptRun, attemptErr := s.server.Runtime(cand.NodeID, cand.RuntimeID).Submit(ctx, req)
		s.breakers.record(cand, attemptErr, s.clock.Now())
		s.metrics.Dispatch(cand, attemptErr)
		if attemptErr == nil {
			return attemptRun, cand, nil, false
		}
		lastErr = attemptErr
		if !submitRetryable(attemptErr) {
			return runtime.WorkflowRun{}, cand, attemptErr, false
		}
		s.metrics.Retry(runtime.CapabilityWorkflowExecution)
	}
	return runtime.WorkflowRun{}, Candidate{}, lastErr, true
}

// queueSubmitWorkflow implements STATUS.md's P2 bounded task queueing. It is
// reached only after attemptSubmitWorkflow found every current candidate
// busy in a retryable way; firstErr is that first exhausted attempt's
// failure, returned unchanged if the wait budget runs out without a
// candidate becoming available.
//
// The wait is bounded on two independent axes, per AGENTS.md's "任何一跳都
// 不得无界缓冲": a maximum duration (s.queueMaxWait) and a maximum number of
// concurrently queued submissions (the s.queueSlots semaphore). A submission
// that finds the semaphore already full is rejected immediately — it does
// not become a third, unbounded axis of queueing.
//
// queueSubmitWorkflow 实现 STATUS.md 的 P2 有界任务排队。只有在 attemptSubmitWorkflow
// 发现每一个当前候选都以可重试方式繁忙之后才会走到这里；firstErr 是那次耗尽尝试的
// 失败，等待预算用完、仍没有候选可用时会原样返回它。
//
// 等待在两条彼此独立的轴上都设了界，对应 AGENTS.md「任何一跳都不得无界缓冲」：一个
// 最长时长（s.queueMaxWait）与一个最大并发排队数（s.queueSlots 信号量）。发现信号量
// 已满的提交会被立即拒绝——它不会成为排队的第三条无界轴。
func (s *Scheduler) queueSubmitWorkflow(ctx context.Context, req runtime.WorkflowRequest, firstErr error) (runtime.WorkflowRun, Candidate, error) {
	if !s.acquireQueueSlot() {
		s.metrics.QueueRejected()
		return runtime.WorkflowRun{}, Candidate{}, firstErr
	}
	defer s.releaseQueueSlot()

	start := s.clock.Now()
	deadline := start.Add(s.queueMaxWait)
	lastErr := firstErr
	for {
		remaining := deadline.Sub(s.clock.Now())
		if remaining <= 0 {
			s.metrics.QueueWait(s.clock.Now().Sub(start), queueOutcomeTimeout)
			return runtime.WorkflowRun{}, Candidate{}, lastErr
		}
		timer, stop := s.clock.NewTimer(min(s.queueRetryInterval, remaining))
		select {
		case <-ctx.Done():
			stop()
			s.metrics.QueueWait(s.clock.Now().Sub(start), queueOutcomeCanceled)
			return runtime.WorkflowRun{}, Candidate{}, ctx.Err()
		case <-timer:
		}

		run, c, err, exhausted := s.attemptSubmitWorkflow(ctx, req)
		if err == nil {
			s.metrics.QueueWait(s.clock.Now().Sub(start), queueOutcomeResolved)
			return run, c, nil
		}
		lastErr = err
		if !exhausted {
			s.metrics.QueueWait(s.clock.Now().Sub(start), queueOutcomeResolved)
			return runtime.WorkflowRun{}, c, err
		}
	}
}

// acquireQueueSlot claims one of the queue's bounded waiter slots, reporting
// whether one was available.
func (s *Scheduler) acquireQueueSlot() bool {
	select {
	case s.queueSlots <- struct{}{}:
		s.metrics.QueueDepth(len(s.queueSlots))
		return true
	default:
		return false
	}
}

// releaseQueueSlot returns a waiter slot claimed by acquireQueueSlot.
func (s *Scheduler) releaseQueueSlot() {
	<-s.queueSlots
	s.metrics.QueueDepth(len(s.queueSlots))
}

// SubmitWorkflowTo queues req on c specifically, with no candidate selection
// and no retry on failure. It exists for a submission that has already
// committed to one node — because it uploaded input files there via
// UploadInput (STATUS.md's P04) — where SubmitWorkflow's own
// multi-candidate retry loop is not safe to reuse: retrying on another
// candidate would mean the files just uploaded to this one are not where
// that other node's ComfyUI would look for them, and re-uploading to every
// candidate a retry might try is exactly the "no automatic retry across
// candidates" trade-off this API embodies rather than hides.
//
// SubmitWorkflowTo 把 req 排入 c 这一个指定候选，不做候选挑选，失败也不重试。
// 它的存在，是为了服务一次已经押定某个节点的提交——因为它已经把输入文件
// 经 UploadInput 上传到了那一个节点上（STATUS.md 的 P04）——这种情形下复用
// SubmitWorkflow 自己的多候选重试循环并不安全：换个候选重试，意味着刚上传
// 到这一个节点上的文件，并不在那个节点的 ComfyUI 会去找的地方；而对重试
// 可能尝试的每一个候选都重新上传一遍，正是这个 API 主动体现、而不是悄悄
// 掩盖的「不跨候选自动重试」这个取舍。
func (s *Scheduler) SubmitWorkflowTo(ctx context.Context, c Candidate, req runtime.WorkflowRequest) (runtime.WorkflowRun, error) {
	run, err := s.server.Runtime(c.NodeID, c.RuntimeID).Submit(ctx, req)
	s.breakers.record(c, err, s.clock.Now())
	s.metrics.Dispatch(c, err)
	return run, err
}

// UploadInput streams body to c's runtime backend, so a later
// SubmitWorkflowTo's req.Template can reference the result through
// runtime.InputUploadResult.InputRef (STATUS.md's P04). Like OpenArtifact in
// the opposite direction, body is read as the tunnel consumes it: nothing
// here holds an upload whole.
//
// UploadInput 把 body 流式送到 c 的 runtime 后端，这样之后某个
// SubmitWorkflowTo 的 req.Template 就能通过 runtime.InputUploadResult.InputRef
// 引用这次上传的结果（STATUS.md 的 P04）。与相反方向的 OpenArtifact 一样，
// body 随隧道的消费而被读取：这里不会有什么完整持有一次上传。
func (s *Scheduler) UploadInput(ctx context.Context, c Candidate, meta runtime.InputUploadMeta, body io.Reader) (runtime.InputUploadResult, error) {
	result, err := s.server.Runtime(c.NodeID, c.RuntimeID).UploadInput(ctx, meta, body)
	s.breakers.record(c, err, s.clock.Now())
	s.metrics.Dispatch(c, err)
	return result, err
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
// minGPUMemoryBytes carries STATUS.md's P2 admission-threshold filtering
// through to pickBy exactly as a routed model's Target.MinGPUMemoryBytes
// does for Chat/Embed/Rerank; zero (the common case today, since no caller
// yet has a source for this number) applies no filter.
//
// workflowCandidates 对能运行工作流的节点排序。与推理请求不同，这里没有模型要匹配：
// ComfyUI 实例在 runtime 这一层声明能力，而某张图需要哪些 checkpoint 是模板的属性，
// 不是请求 model 字段的属性。minGPUMemoryBytes 把 STATUS.md P2 的准入门槛过滤一路
// 带到 pickBy，与一个有路由的模型经 Target.MinGPUMemoryBytes 对 Chat/Embed/Rerank
// 所做的完全一样；零值（今天的常见情形，因为还没有调用方有这个数字的来源）不做任何
// 过滤。
func (s *Scheduler) workflowCandidates(cap runtime.Capability, minGPUMemoryBytes int64) []Candidate {
	// An empty target matches every node: a workflow request carries no model
	// to route, so there is nothing for a routing rule to select on beyond
	// the capacity filter minGPUMemoryBytes may add.
	//
	// 空 target 匹配所有节点：工作流请求不携带可路由的模型，因此除了
	// minGPUMemoryBytes 可能带来的容量过滤外，路由规则无从选择。
	return s.pickBy(routing.Target{MinGPUMemoryBytes: minGPUMemoryBytes}, func(snap runtime.Snapshot) bool {
		return snap.Discovery.Capabilities.Require(cap) == nil
	})
}

// WorkflowCapableCandidates returns every currently connected node/runtime
// this replica could dispatch a workflow submission to right now. STATUS.md's
// J06 uses it to know which (NodeID, RuntimeID) route bindings this replica
// might owe a recovery check to: a run recorded against a node this replica
// cannot currently reach is not this replica's to recover, and this method
// is what tells the caller which nodes qualify without duplicating
// workflowCandidates' own routing and capability logic.
//
// WorkflowCapableCandidates 返回本副本此刻真的能把工作流提交过去的每一个已连接
// 节点/运行时。STATUS.md 的 J06 用它来判断本副本可能欠着恢复检查的是哪些
// (NodeID, RuntimeID) 路由绑定：一次记录在本副本此刻够不着的节点上的运行，不该
// 由本副本去恢复，而这个方法正是在不重复 workflowCandidates 自己的路由与能力
// 判断逻辑的前提下，告知调用方哪些节点符合条件。它查的是一个已经在跑的 run 记录
// 在案的绑定，不是新提交要满足的显存要求，因此不带 minGPUMemoryBytes 过滤——一个
// 节点的显存声明会随时间变化，但已经在它上面跑的 run 不会因此被重新判定为不该
// 恢复。
func (s *Scheduler) WorkflowCapableCandidates() []Candidate {
	return s.workflowCandidates(runtime.CapabilityWorkflowExecution, 0)
}

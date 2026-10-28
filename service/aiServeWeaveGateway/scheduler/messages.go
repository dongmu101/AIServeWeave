package scheduler

import (
	"context"
	"errors"
	"io"
	"math/rand"

	"AIServeWeave/common/runtime"
)

// MessagesCandidates returns only nodes supporting native Anthropic Messages.
// MessagesCandidates 仅返回支持原生 Anthropic Messages 的节点。
func (s *Scheduler) MessagesCandidates(model string) []Candidate {
	return s.candidates(model, runtime.CapabilityMessages)
}

// Messages dispatches once to the selected node. Tool continuations use it
// because they must return to the node holding their CLI; a new session
// starts through MessagesStart instead.
//
// Messages 仅向所选节点分派一次。工具续接使用它，因为续接必须回到持有其 CLI 的节点；
// 新会话改走 MessagesStart。
func (s *Scheduler) Messages(ctx context.Context, c Candidate, req runtime.MessagesRequest) (runtime.Stream[runtime.MessagesEvent], error) {
	if !modelAllowed(ctx, req.Model) {
		return nil, ErrModelNotAllowed
	}
	req.Model = c.Model
	return s.server.Runtime(c.NodeID, c.RuntimeID).Messages(ctx, req)
}

// MessagesStart opens a new native Messages session on the best candidate.
// Until the first event arrives no client has seen the session, so a node
// that is full, closing or unreachable hands the request to the next
// candidate; once an event arrives the stream belongs to the caller, the same
// Committed rule ChatStream follows. Candidates rank by tunnel slots,
// which cannot see an Agent's CLI session limit, so this fallback is what
// keeps one full node from turning into a 429 while another sits idle.
//
// MessagesStart 在最佳候选上开启一个新的原生 Messages 会话。首个事件到达之前，
// 没有客户端见过这个会话，因此节点已满、正在关闭或不可达时，请求交给下一个候选；
// 一旦有事件到达，流就归调用方所有，与 ChatStream 遵循的 Committed 规则相同。候选
// 按隧道槽位排序，看不到 Agent 的 CLI 会话上限，正是这层换节点避免了一个节点已满、
// 另一个节点空闲时仍返回 429。
func (s *Scheduler) MessagesStart(ctx context.Context, req runtime.MessagesRequest) (runtime.Stream[runtime.MessagesEvent], Candidate, error) {
	if !modelAllowed(ctx, req.Model) {
		return nil, Candidate{}, ErrModelNotAllowed
	}
	candidates := s.candidatesKeyed(s.routes.Load(), req.Model, runtime.CapabilityMessages, s.sessionAffinity(ctx), rand.Float64)
	s.metrics.Selection(runtime.CapabilityMessages, len(candidates))
	if len(candidates) == 0 {
		return nil, Candidate{}, ErrNoCapableNode
	}
	var lastErr error
	var cooled cooledCandidates
	for _, c := range candidates {
		if s.skipCooling(c, &cooled) {
			continue
		}
		stream, err := s.Messages(ctx, c, req)
		if err == nil {
			var first runtime.MessagesEvent
			first, err = stream.Recv()
			if err == nil || err == io.EOF {
				s.breakers.record(c, nil, s.clock.Now())
				s.metrics.Dispatch(c, nil)
				s.logDispatchDecision(ctx, "messages", c, nil)
				return &prefetchStream[runtime.MessagesEvent]{first: first, hasFirst: err == nil, eof: err == io.EOF, underlying: stream}, c, nil
			}
			stream.Close()
		}
		s.breakers.record(c, err, s.clock.Now())
		s.metrics.Dispatch(c, err)
		s.logDispatchDecision(ctx, "messages", c, err)
		lastErr = err
		// A failed first Recv delivered nothing, so the stream is never
		// Committed here and the session can still start elsewhere.
		// 首次 Recv 失败意味着什么都没交付，此处的流不可能已 Committed，会话仍可在别处开启。
		if !messagesStartRetryable(err) {
			return nil, c, err
		}
		s.metrics.Retry(runtime.CapabilityMessages)
	}
	return nil, Candidate{}, cooled.or(lastErr)
}

// messagesStartRetryable reports whether a failed session start may move to
// the next candidate: any Retryable error, plus the codes the Claude adapter
// sends without that flag when a node cannot take a session now —
// backpressure while its CLI sessions are all held, closed while it shuts
// down, rate_limited while its account's quota is spent. Checking codes
// rather than the flag also covers Agents built before they reported it.
//
// messagesStartRetryable 判断一次失败的会话开启能否换到下一个候选：任何 Retryable
// 错误都可以；另外还有 Claude 适配器在节点暂时无法接收会话时发出、但不带该标志的错误码
// ——CLI 会话全部占用时的 backpressure、关闭过程中的 closed，以及账户额度用完时的
// rate_limited。按错误码而不是标志判断，也覆盖了尚不上报该标志的旧版 Agent。
func messagesStartRetryable(err error) bool {
	if retryable(err) {
		return true
	}
	var rtErr *runtime.RuntimeError
	return errors.As(err, &rtErr) && (rtErr.Code == runtime.ErrorBackpressure || rtErr.Code == runtime.ErrorClosed || rtErr.Code == runtime.ErrorRateLimited)
}

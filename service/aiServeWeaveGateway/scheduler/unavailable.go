package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
)

// ErrModelUnavailable is returned when no candidate can serve a model right
// now, but a node the caller may use served it recently: the node is
// reconnecting, unhealthy, or behind an open breaker. Unlike ErrNoCapableNode
// it does not wrap a not-found, because the model exists and a client that
// retries shortly will most likely be served; front doors answer it as a
// retryable 503.
//
// It never reveals more than the caller's /v1/models already does: only nodes
// and models the caller's access policies admit are consulted.
//
// ErrModelUnavailable 在此刻没有候选能服务某模型、但调用方可用的某个节点最近提供过它时
// 返回：该节点正在重连、不健康或处于熔断中。它与 ErrNoCapableNode 不同，不包装
// not-found，因为模型是存在的，客户端稍后重试多半就能得到服务；各前门把它作为可重试的
// 503 返回。
//
// 它透露的信息不会超过调用方 /v1/models 已经给出的：只查看调用方授权允许的节点与模型。
var ErrModelUnavailable = errors.New("scheduler: the model's nodes are temporarily unavailable")

// defaultUnavailableWait is Config.UnavailableWait's default: long enough to
// ride out a typical Agent reconnect, short enough that a client's own
// timeout is never the one that fires.
//
// defaultUnavailableWait 是 Config.UnavailableWait 的默认值：足以等过一次典型的 Agent
// 重连，又足够短，不会让客户端自己的超时先触发。
const defaultUnavailableWait = 15 * time.Second

// unavailablePollInterval is how often AwaitModel re-checks for a candidate.
// unavailablePollInterval 是 AwaitModel 重新检查候选的间隔。
const unavailablePollInterval = 500 * time.Millisecond

// noCandidatesFor is the error for an empty candidate list for model with
// cap: ErrModelUnavailable when a node the caller may use served it
// recently, otherwise noCandidates.
//
// noCandidatesFor 是 model 以 cap 找不到候选时的错误：调用方可用的节点最近提供过它时为
// ErrModelUnavailable，否则为 noCandidates。
func (s *Scheduler) noCandidatesFor(ctx context.Context, model string, cap runtime.Capability) error {
	if s.servedRecently(ctx, model, cap) {
		return ErrModelUnavailable
	}
	return noCandidates(ctx)
}

// servedRecently reports whether a node ctx's caller may use advertises model
// with cap, ignoring everything that only makes it unusable for now:
// liveness, draining, runtime health and the circuit breaker. Nodes that left
// this replica within tunnelserver's DepartedRetention count too, so the
// seconds an Agent spends reconnecting read as unavailable, not unknown. The
// filters that decide who may use what — node and model access, accounts,
// the target's node selector and capacity — still apply, and maintenance
// nodes are excluded, since an operator took them out on purpose.
//
// servedRecently 报告 ctx 上的调用方可用的某个节点是否以 cap 声明了 model，忽略一切只让
// 它暂时不可用的因素：存活、draining、运行时健康与熔断。在 tunnelserver 的
// DepartedRetention 内离开本副本的节点也计入，使 Agent 重连的那几秒读作暂时不可用，而不是
// 不存在。决定谁能用什么的过滤——节点与模型授权、账户、target 的节点选择器与容量——仍然
// 生效；维护中的节点被排除，因为那是运维有意摘下的。
func (s *Scheduler) servedRecently(ctx context.Context, model string, cap runtime.Capability) bool {
	targets, routed := s.routes.Load().Resolve(model)
	if !routed {
		targets = []routing.Target{{RuntimeModel: model}}
	}
	nodes := s.server.AppendDepartedNodes(s.server.AppendSchedNodes(nil))
	for _, target := range targets {
		eligible := modelEligible(target, cap)
		for i := range nodes {
			node := &nodes[i]
			if node.Maintenance || !target.MatchesNode(node.Labels) || !nodeAllowed(ctx, node.NodeID) ||
				!nodeHasCapacity(node, target.MinGPUMemoryBytes) {
				continue
			}
			for r := range node.Runtimes {
				snap := &node.Runtimes[r]
				if s.accountAllowed(ctx, node.NodeID, snap.Descriptor.ID) && eligible(snap) {
					return true
				}
			}
		}
	}
	return false
}

// AwaitModel holds a request whose model has no candidate for any of caps
// right now, but was served recently (see servedRecently), until a candidate
// appears or Config.UnavailableWait runs out; it then returns
// ErrModelUnavailable. It returns nil at once when a candidate exists, when
// the model is unknown or not admitted — the dispatch that follows reports
// those exactly as before — and when ctx ends, since nobody is left to
// answer.
//
// A front door calls it before choosing between protocol paths, so that a
// node coming back mid-wait is chosen on the path it would have been chosen
// on had it never left.
//
// AwaitModel 让一个此刻对任一 caps 都没有候选、但最近有人提供过（见 servedRecently）的
// 模型的请求等待，直到出现候选或 Config.UnavailableWait 用尽，届时返回
// ErrModelUnavailable。存在候选、模型未知或未被准入（随后的派发会与以前完全一样地报告
// 这些情况）、以及 ctx 结束（已无人可答复）时立即返回 nil。
//
// 前门在选择协议路径之前调用它，使等待途中回来的节点按它从未离开时会走的那条路径被选中。
func (s *Scheduler) AwaitModel(ctx context.Context, model string, caps ...runtime.Capability) error {
	ctx, admitted := admitModel(ctx, model)
	if !admitted || s.anyCandidate(ctx, model, caps) {
		return nil
	}
	recent := false
	for _, cap := range caps {
		if s.servedRecently(ctx, model, cap) {
			recent = true
			break
		}
	}
	if !recent {
		return nil
	}
	if s.unavailableWait <= 0 {
		return ErrModelUnavailable
	}
	start := s.clock.Now()
	deadline := start.Add(s.unavailableWait)
	for {
		timer, stop := s.clock.NewTimer(unavailablePollInterval)
		select {
		case <-ctx.Done():
			stop()
			return nil
		case <-timer:
		}
		if s.anyCandidate(ctx, model, caps) {
			s.logger.Info("model became available while waiting",
				slog.String("model", model), slog.Duration("waited", s.clock.Now().Sub(start)))
			return nil
		}
		if !s.clock.Now().Before(deadline) {
			s.logger.Warn("model still unavailable after waiting",
				slog.String("model", model), slog.Duration("waited", s.clock.Now().Sub(start)))
			return ErrModelUnavailable
		}
	}
}

// anyCandidate reports whether model has a candidate for at least one of caps.
// anyCandidate 报告 model 是否至少对 caps 之一存在候选。
func (s *Scheduler) anyCandidate(ctx context.Context, model string, caps []runtime.Capability) bool {
	view := s.acquireView()
	defer releaseView(view)
	routes := s.routes.Load()
	for _, cap := range caps {
		if s.hasCandidate(ctx, routes, model, cap, view) {
			return true
		}
	}
	return false
}

package scheduler

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"sort"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// ErrNoCapableNode is returned when no connected, live node reports a
// runtime instance that has the requested model with the requested
// capability.
var ErrNoCapableNode = errors.New("scheduler: no node can serve this model")

// Candidate is the (node, runtime instance) pair a request was dispatched
// to, kept around so a caller can log or attribute the response to it.
type Candidate struct {
	NodeID    string
	RuntimeID string
	// Model is the runtime model id this candidate serves the request as. It
	// differs from what the client asked for whenever a routing alias was
	// resolved, and it is what the request is rewritten to before dispatch —
	// the client never learns it, which is the point of the alias.
	//
	// Model 是本候选据以服务该请求的运行时模型 id。只要解析过一个路由别名，它就与
	// 客户端所请求的不同；请求在派发前会被改写成它——客户端始终不会得知它，而这正是
	// 别名的意义。
	Model string
}

// ModelInfo is one model advertised, with chat capability, by at least one
// connected node.
type ModelInfo struct {
	ID string
}

// Scheduler selects a node for a request and applies the retry policy from
// README's "调度流程": a request may move to another node only while it has
// not yet produced any output, which is the point up to which a client can
// safely see it retried without risking duplicate or discontinuous content.
//
// Beyond that, Scheduler now also owns a circuit breaker per candidate (see
// breaker.go): every dispatch attempt's outcome is recorded, so a candidate
// that keeps failing is excluded from future selections for a while. That
// state must survive across requests, so — unlike before — a Scheduler is no
// longer stateless and must be constructed once and shared for the life of
// the process; main.go already does this.
type Scheduler struct {
	server   *tunnelserver.Server
	clock    runtime.Clock
	breakers *breakerRegistry
	metrics  *recorder
	routes   *routing.Table
}

// Config configures New. Every field is optional.
type Config struct {
	// Clock supplies time for the circuit breaker's cooldown windows. Nil
	// uses the system clock; tests inject a fake so cooldown expiry is
	// exercised without sleeping.
	Clock runtime.Clock
	// FailureThreshold is how many consecutive breaker-qualifying failures
	// (see breakerFailure) trip a candidate's breaker open. Zero uses
	// defaultFailureThreshold.
	FailureThreshold int
	// BaseCooldown and MaxCooldown bound how long a tripped breaker stays
	// open before its next probe, doubling per repeated trip. Zero uses
	// defaultBaseCooldown / defaultMaxCooldown.
	BaseCooldown time.Duration
	MaxCooldown  time.Duration

	// Metrics receives the scheduler's instruments, described by
	// Descriptions. Nil discards them.
	//
	// Metrics 接收调度器的仪器，其描述见 Descriptions。为 nil 时全部丢弃。
	Metrics runtime.Metrics

	// Routes maps logical model names onto the deployments that serve them.
	// Nil, or a table with no entry for the requested model, means the model
	// id is used as the node advertises it — a deployment that never writes a
	// routing table goes on working exactly as before.
	//
	// Routes 把逻辑模型名映射到服务它们的部署上。为 nil、或表中没有所请求模型的条目
	// 时，模型 id 按节点声明的原样使用——从不编写路由表的部署，行为与此前完全一致。
	Routes *routing.Table
}

// New returns a Scheduler that selects among the nodes connected to server.
func New(server *tunnelserver.Server, cfg Config) *Scheduler {
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	rec := newRecorder(cfg.Metrics)
	return &Scheduler{
		server:   server,
		clock:    clock,
		breakers: newBreakerRegistry(cfg.FailureThreshold, cfg.BaseCooldown, cfg.MaxCooldown, rec),
		metrics:  rec,
		routes:   cfg.Routes,
	}
}

// Chat dispatches req to the best available node, retrying on the next
// candidate while the failure is Retryable.
func (s *Scheduler) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, Candidate, error) {
	candidates := s.candidates(req.Model, runtime.CapabilityChat)
	s.metrics.Selection(runtime.CapabilityChat, len(candidates))
	if len(candidates) == 0 {
		return runtime.ChatResponse{}, Candidate{}, ErrNoCapableNode
	}
	var lastErr error
	for _, c := range candidates {
		resp, err := s.server.Runtime(c.NodeID, c.RuntimeID).Chat(ctx, withModel(req, c.Model))
		s.breakers.record(c, err, s.clock.Now())
		s.metrics.Dispatch(c, err)
		if err == nil {
			return resp, c, nil
		}
		lastErr = err
		if !retryable(err) {
			return runtime.ChatResponse{}, c, err
		}
		s.metrics.Retry(runtime.CapabilityChat)
	}
	return runtime.ChatResponse{}, Candidate{}, lastErr
}

// Embed dispatches req the same way Chat does.
func (s *Scheduler) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, Candidate, error) {
	candidates := s.candidates(req.Model, runtime.CapabilityEmbeddings)
	s.metrics.Selection(runtime.CapabilityEmbeddings, len(candidates))
	if len(candidates) == 0 {
		return runtime.EmbeddingResponse{}, Candidate{}, ErrNoCapableNode
	}
	var lastErr error
	for _, c := range candidates {
		resp, err := s.server.Runtime(c.NodeID, c.RuntimeID).Embed(ctx, withEmbedModel(req, c.Model))
		s.breakers.record(c, err, s.clock.Now())
		s.metrics.Dispatch(c, err)
		if err == nil {
			return resp, c, nil
		}
		lastErr = err
		if !retryable(err) {
			return runtime.EmbeddingResponse{}, c, err
		}
		s.metrics.Retry(runtime.CapabilityEmbeddings)
	}
	return runtime.EmbeddingResponse{}, Candidate{}, lastErr
}

// ChatStream dispatches req and returns a stream already positioned to
// deliver its first event (or io.EOF for a valid empty response). Whether a
// request "has produced output yet" can only be answered by reading from the
// stream, so ChatStream reads the first event itself: a failure before that
// point retries on the next candidate, and a failure after it is handed to
// the caller exactly as the underlying Stream reports it, with no further
// node switch.
func (s *Scheduler) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], Candidate, error) {
	candidates := s.candidates(req.Model, runtime.CapabilityChatStream)
	s.metrics.Selection(runtime.CapabilityChatStream, len(candidates))
	if len(candidates) == 0 {
		return nil, Candidate{}, ErrNoCapableNode
	}
	var lastErr error
	for _, c := range candidates {
		stream, err := s.server.Runtime(c.NodeID, c.RuntimeID).ChatStream(ctx, withModel(req, c.Model))
		if err != nil {
			s.breakers.record(c, err, s.clock.Now())
			s.metrics.Dispatch(c, err)
			lastErr = err
			if retryable(err) {
				s.metrics.Retry(runtime.CapabilityChatStream)
				continue
			}
			return nil, c, err
		}

		first, err := stream.Recv()
		if err == nil {
			s.breakers.record(c, nil, s.clock.Now())
			s.metrics.Dispatch(c, nil)
			return &prefetchStream{first: first, hasFirst: true, underlying: stream}, c, nil
		}
		stream.Close()
		if err == io.EOF {
			// A valid, empty response: nothing was produced, but nothing
			// failed either. Do not retry a successful call.
			s.breakers.record(c, nil, s.clock.Now())
			s.metrics.Dispatch(c, nil)
			return &prefetchStream{eof: true}, c, nil
		}
		s.breakers.record(c, err, s.clock.Now())
		s.metrics.Dispatch(c, err)
		lastErr = err
		// Committed() is defined to flip only once an event has been
		// delivered, so it is necessarily still false here; the check
		// documents that this is the property being relied on, not an
		// accident of call order.
		if !stream.Committed() && retryable(err) {
			s.metrics.Retry(runtime.CapabilityChatStream)
			continue
		}
		return nil, c, err
	}
	return nil, Candidate{}, lastErr
}

// Models is the public catalogue: what a client may ask for right now.
//
// With a routing table it lists aliases, not the real model ids behind them —
// publishing both would invite clients to bind to a runtime model, which is
// exactly what the alias exists to prevent. An alias no live node can serve is
// left out: a catalogue entry that 404s on use is worse than an absent one.
//
// Without a routing table it lists what the nodes advertise, so a deployment
// that never writes one is unaffected.
//
// Models 是公开目录：客户端此刻可以请求什么。
//
// 有路由表时它列出别名，而不是其背后的真实模型 id——两者都公布，等于邀请客户端绑定到
// 某个运行时模型，而那正是别名存在所要防止的事。没有任何活节点能服务的别名会被略去：
// 一个用起来就 404 的目录条目，比一个缺失的条目更糟。
//
// 没有路由表时它列出节点声明的内容，因此从不编写路由表的部署不受影响。
func (s *Scheduler) Models(ctx context.Context) []ModelInfo {
	_ = ctx
	if s.routes.Len() > 0 {
		var out []ModelInfo
		for _, alias := range s.routes.Models() {
			if len(s.candidates(alias, runtime.CapabilityChat)) > 0 {
				out = append(out, ModelInfo{ID: alias})
			}
		}
		return out
	}

	seen := make(map[string]struct{})
	var out []ModelInfo
	for _, node := range s.server.Nodes() {
		if !node.Live || node.Draining {
			continue
		}
		for _, snap := range node.Runtimes {
			if !runtimeHealthy(snap.State) {
				continue
			}
			for _, m := range snap.Discovery.Models {
				if _, ok := seen[m.ID]; ok {
					continue
				}
				effective := runtime.Intersect(snap.Discovery.Capabilities, m.Capabilities)
				if effective.Require(runtime.CapabilityChat) != nil {
					continue
				}
				seen[m.ID] = struct{}{}
				out = append(out, ModelInfo{ID: m.ID})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// candidates resolves the requested model through the routing table and
// returns every (node, runtime, runtime-model) triple that can serve it, best
// first.
//
// The ordering has two levels, and the outer one is the operator's. Targets
// are tried in the priority they were configured with, and only within one
// target does the load heuristic decide. That is what makes "the local Mac
// before the rented GPU" mean what it says: a stated preference that a
// momentarily idler machine cannot override.
//
// candidates 把请求的模型经路由表解析，返回每一个能服务它的 (node, runtime, 运行时
// 模型) 三元组，最优的在前。
//
// 排序有两层，外层属于运维。target 按配置的优先级依次尝试，只有在同一个 target 内部
// 才由负载启发式决定。这正是「先用本地那台 Mac，再用租来的 GPU」名副其实的原因：一个
// 声明的偏好，不会被一台一时更空闲的机器推翻。
func (s *Scheduler) candidates(model string, cap runtime.Capability) []Candidate {
	targets, routed := s.routes.Resolve(model)
	if !routed {
		// An unrouted model is used exactly as the node advertises it. A
		// deployment with no routing table is the common case, not an error.
		//
		// 未配置路由的模型按节点声明的原样使用。没有路由表的部署是常态，不是错误。
		targets = []routing.Target{{RuntimeModel: model}}
	}

	var out []Candidate
	seen := make(map[Candidate]struct{})
	for _, target := range targets {
		for _, c := range s.pick(target, cap) {
			if _, dup := seen[c]; dup {
				// Two targets can select the same node for the same runtime
				// model. Keeping the first occurrence preserves the higher
				// priority's position.
				//
				// 两个 target 可能为同一个运行时模型选中同一个节点。保留首次出现，
				// 就保住了较高优先级的位置。
				continue
			}
			seen[c] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}

// pick returns every (node, runtime) pair that can serve target with cap,
// ordered best first: most idle inference slots, then fewest in-flight
// requests, matching README's "最少正在执行请求" strategy. Nodes are shuffled
// before that sort so requests do not all pile onto the same node whenever
// several are tied.
//
// pick 返回每一个能以 cap 服务 target 的 (node, runtime) 对，最优的在前：空闲推理槽
// 最多的优先，其次是在途请求最少的，对应 README 的「最少正在执行请求」策略。排序前先
// 打乱节点顺序，这样在多个节点打平时请求不会全堆到同一个上。
func (s *Scheduler) pick(target routing.Target, cap runtime.Capability) []Candidate {
	return s.pickBy(target, func(snap runtime.Snapshot) bool {
		for _, m := range snap.Discovery.Models {
			if m.ID != target.RuntimeModel {
				continue
			}
			effective := runtime.Intersect(snap.Discovery.Capabilities, m.Capabilities)
			if effective.Require(cap) == nil {
				return true
			}
		}
		return false
	})
}

// pickBy is the shared selection walk: node health, the target's node
// selector, the caller's own eligibility test, and the circuit breaker, then
// the load ordering.
//
// What "eligible" means is the caller's to define — a model plus a capability
// for inference, a runtime-level capability for workflows — so that the two
// share one health filter, one breaker check and one ranking rather than
// growing two copies that drift.
//
// pickBy 是共用的选择流程：节点健康、target 的节点选择器、调用方自己的资格判定、
// 熔断器，然后是负载排序。
//
// 「有资格」的含义由调用方定义——推理是模型加能力，工作流是 runtime 层的能力——这样
// 两者共用同一套健康过滤、同一次熔断检查与同一套排序，而不是长出两份会各自漂移的拷贝。
func (s *Scheduler) pickBy(target routing.Target, eligible func(runtime.Snapshot) bool) []Candidate {
	type scored struct {
		Candidate
		idle     int
		inflight int
	}

	nodes := s.server.Nodes()
	rand.Shuffle(len(nodes), func(i, j int) { nodes[i], nodes[j] = nodes[j], nodes[i] })
	now := s.clock.Now()

	var found []scored
	for _, node := range nodes {
		if !node.Live || node.Draining {
			continue
		}
		// The node selector is checked before anything else about the node:
		// a target that does not apply here should not consume a breaker
		// lookup or a capability walk.
		//
		// 节点选择器先于关于该节点的任何其他判断：一个在此不适用的 target，不该消耗
		// 一次熔断查询或一次能力遍历。
		if !target.MatchesNode(node.Labels) {
			continue
		}
		idle := node.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_INFERENCE]
		for _, snap := range node.Runtimes {
			if !runtimeHealthy(snap.State) {
				continue
			}
			if !eligible(snap) {
				continue
			}
			candidate := Candidate{NodeID: node.NodeID, RuntimeID: snap.Descriptor.ID, Model: target.RuntimeModel}
			if !s.breakers.eligible(candidate, now) {
				continue
			}
			found = append(found, scored{
				Candidate: candidate,
				idle:      idle,
				inflight:  node.InflightRequests,
			})
		}
	}

	sort.SliceStable(found, func(i, j int) bool {
		if found[i].idle != found[j].idle {
			return found[i].idle > found[j].idle
		}
		return found[i].inflight < found[j].inflight
	})

	candidates := make([]Candidate, len(found))
	for i, f := range found {
		candidates[i] = f.Candidate
	}
	return candidates
}

// withModel returns req rewritten to the runtime model a candidate serves.
// The caller's request is never mutated: one request may be tried against
// several candidates, and each has to see its own model id.
//
// withModel 返回被改写为某候选所服务的运行时模型的 req。调用方的请求从不被修改：一个
// 请求可能被投向多个候选，而每个候选都必须看到属于它自己的模型 id。
func withModel(req runtime.ChatRequest, model string) runtime.ChatRequest {
	if model == "" || model == req.Model {
		return req
	}
	out := req
	out.Model = model
	return out
}

// withEmbedModel is withModel for embedding requests.
//
// withEmbedModel 是嵌入请求版的 withModel。
func withEmbedModel(req runtime.EmbeddingRequest, model string) runtime.EmbeddingRequest {
	if model == "" || model == req.Model {
		return req
	}
	out := req
	out.Model = model
	return out
}

// runtimeHealthy reports whether a runtime instance in state should still be
// offered as a candidate. Manager's health probe (common/runtime) already
// decided this — a Gateway inventing a second opinion from the same signal
// would just be a slower, staler copy of it — so this is a straight read of
// the state Agent reported, not a new judgment. registering and unknown stay
// eligible: an instance that has not finished its first probe yet is not the
// same thing as one a probe has already condemned.
func runtimeHealthy(state runtime.State) bool {
	return state != runtime.StateUnhealthy && state != runtime.StateClosed
}

// errorCode returns the runtime error code err carries, if any.
//
// errorCode 返回 err 携带的 runtime 错误码（如果有）。
func errorCode(err error) (runtime.ErrorCode, bool) {
	var rtErr *runtime.RuntimeError
	if errors.As(err, &rtErr) {
		return rtErr.Code, true
	}
	return "", false
}

// retryable reports whether err is a *runtime.RuntimeError marked
// Retryable. Any other error — including io.EOF, which callers handle
// separately — is not.
func retryable(err error) bool {
	var rtErr *runtime.RuntimeError
	if errors.As(err, &rtErr) {
		return rtErr.Retryable
	}
	return false
}

// prefetchStream replays an already-received first event before delegating
// to the underlying Stream, so ChatStream can peek at the first event to
// decide whether to retry without losing it.
type prefetchStream struct {
	first      runtime.ChatEvent
	hasFirst   bool
	eof        bool
	underlying runtime.Stream[runtime.ChatEvent]
}

func (p *prefetchStream) Recv() (runtime.ChatEvent, error) {
	if p.hasFirst {
		p.hasFirst = false
		return p.first, nil
	}
	if p.eof {
		return runtime.ChatEvent{}, io.EOF
	}
	return p.underlying.Recv()
}

func (p *prefetchStream) Committed() bool {
	if p.underlying == nil {
		return false
	}
	return p.underlying.Committed()
}

func (p *prefetchStream) Close() error {
	if p.underlying == nil {
		return nil
	}
	return p.underlying.Close()
}

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
		resp, err := s.server.Runtime(c.NodeID, c.RuntimeID).Chat(ctx, req)
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
		resp, err := s.server.Runtime(c.NodeID, c.RuntimeID).Embed(ctx, req)
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
		stream, err := s.server.Runtime(c.NodeID, c.RuntimeID).ChatStream(ctx, req)
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

// Models returns every model, deduplicated by ID and sorted, that at least
// one live node currently reports chat capability for.
func (s *Scheduler) Models(ctx context.Context) []ModelInfo {
	_ = ctx
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

// candidates returns every (node, runtime) pair currently capable of
// serving model with cap, ordered best first: most idle inference slots,
// then fewest in-flight requests, matching README's "最少正在执行请求"
// strategy. Nodes are shuffled before that sort so requests do not all pile
// onto the same node whenever several are tied.
func (s *Scheduler) candidates(model string, cap runtime.Capability) []Candidate {
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
		idle := node.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_INFERENCE]
		for _, snap := range node.Runtimes {
			if !runtimeHealthy(snap.State) {
				continue
			}
			for _, m := range snap.Discovery.Models {
				if m.ID != model {
					continue
				}
				effective := runtime.Intersect(snap.Discovery.Capabilities, m.Capabilities)
				if effective.Require(cap) != nil {
					continue
				}
				candidate := Candidate{NodeID: node.NodeID, RuntimeID: snap.Descriptor.ID}
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

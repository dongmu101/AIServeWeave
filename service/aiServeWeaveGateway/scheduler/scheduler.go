package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"AIServeWeave/common/reqid"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// ErrNoCapableNode is returned when no connected, live node reports a
// runtime instance that has the requested model with the requested
// capability.
var ErrNoCapableNode = errors.New("scheduler: no node can serve this model")

// defaultQueueRetryInterval and defaultQueueMaxWaiters are the defaults for
// Config.QueueRetryInterval and Config.QueueMaxWaiters when queueing is
// enabled (Config.QueueMaxWait > 0) but a caller leaves one of them unset.
//
// defaultQueueRetryInterval 与 defaultQueueMaxWaiters 是排队开启
// （Config.QueueMaxWait > 0）但调用方未设置其中某项时使用的默认值。
const (
	defaultQueueRetryInterval = 500 * time.Millisecond
	defaultQueueMaxWaiters    = 64
)

// Candidate is the (node, runtime instance) pair a request was dispatched
// to, kept around so a caller can attribute the response and identify backend compatibility.
// Candidate 记录实际派发的节点与运行时，供调用方归属响应并识别后端兼容行为。
type Candidate struct {
	NodeID    string
	RuntimeID string
	// Kind identifies the selected backend, including after a retry to another node.
	// Kind 标识实际选中的后端类型，包括重试切换节点后的类型。
	Kind runtime.Kind
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

// ModelInfo is an available Chat or Messages model with optional display metadata.
// ModelInfo 是可用的 Chat 或 Messages 模型及其可选显示信息。
type ModelInfo struct {
	ID           string
	DisplayName  string
	DisplayOrder uint32
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
	logger   *slog.Logger
	routes   atomic.Pointer[routing.Table]

	// policy and prefixBytes order candidates inside one target; see Policy.
	// intn is the randomness PolicyPowerOfTwo draws from, a field so tests
	// can pin it.
	//
	// policy 与 prefixBytes 决定同一 target 内候选的排序，见 Policy。intn 是
	// PolicyPowerOfTwo 取随机数的来源，做成字段以便测试固定它。
	policy      Policy
	prefixBytes int
	intn        func(int) int

	// sessionAffinityOn is Config.SessionAffinity.
	// sessionAffinityOn 即 Config.SessionAffinity。
	sessionAffinityOn bool

	// queueMaxWait, queueRetryInterval and queueSlots implement STATUS.md's
	// P2 bounded task queueing for workflow submissions (see workflow.go's
	// queueSubmitWorkflow). queueSlots is nil, and queueing disabled, unless
	// Config.QueueMaxWait was positive at construction.
	queueMaxWait       time.Duration
	queueRetryInterval time.Duration
	queueSlots         chan struct{}
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

	// RateLimitCooldown is how long a candidate that answered rate_limited
	// without a reset time is skipped. A rate_limited error that carries
	// RetryAfter (a CLI subscription window, a provider's Retry-After) is
	// skipped for exactly that long instead, up to MaxRateLimitCooldown. Zero
	// uses 30s and 1h respectively. Unlike the breaker this never counts as a
	// failure of the node: the next success ends the cooldown at once.
	//
	// RateLimitCooldown 是回答 rate_limited 但没有给出重置时间的候选被跳过的时长。
	// 携带 RetryAfter 的 rate_limited（CLI 订阅窗口、供应商的 Retry-After）则恰好跳过
	// 那么久，但不超过 MaxRateLimitCooldown。零值分别使用 30s 与 1h。与熔断不同，它从不
	// 算作节点故障：下一次成功会立即结束冷却。
	RateLimitCooldown    time.Duration
	MaxRateLimitCooldown time.Duration

	// SessionAffinity ranks chat and Messages requests that carry a session
	// key (see WithSessionKey) by rendezvous hashing of that key, whatever
	// Policy is, so one conversation keeps reusing the prompt cache of the
	// node and account it started on. A candidate with no idle inference slot
	// still yields to one that has, and a cooling or broken candidate is still
	// skipped, so affinity is a preference, never a pin. Requests without a
	// key are unaffected.
	//
	// SessionAffinity 让携带会话 key（见 WithSessionKey）的聊天与 Messages 请求按该 key
	// 的 rendezvous 哈希排序，无论 Policy 是什么，使一段对话持续复用它起始所在节点与账户
	// 的提示缓存。没有空闲推理槽的候选仍让位于有空闲槽的，冷却中或已熔断的候选仍被跳过，
	// 因此亲和只是偏好，从不是钉死。没有 key 的请求不受影响。
	SessionAffinity bool

	// Metrics receives the scheduler's instruments, described by
	// Descriptions. Nil discards them.
	//
	// Metrics 接收调度器的仪器，其描述见 Descriptions。为 nil 时全部丢弃。
	Metrics runtime.Metrics

	// Logger receives one "dispatch decided" event per attempt, keyed by the
	// request_id carried on ctx (see common/reqid). Nil discards them.
	//
	// Logger 接收每次尝试一条「派发决策」事件，以 ctx 上携带的 request_id
	// (见 common/reqid)为键。为 nil 时丢弃。
	Logger *slog.Logger

	// Routes maps logical model names onto the deployments that serve them.
	// Nil, or a table with no entry for the requested model, means the model
	// id is used as the node advertises it — a deployment that never writes a
	// routing table goes on working exactly as before.
	//
	// Routes 把逻辑模型名映射到服务它们的部署上。为 nil、或表中没有所请求模型的条目
	// 时，模型 id 按节点声明的原样使用——从不编写路由表的部署，行为与此前完全一致。
	Routes *routing.Table

	// Policy orders the candidates inside one routing target for inference
	// requests. The zero value means PolicyLeastLoad, the behavior that
	// predates this field. Workflow submissions always use PolicyLeastLoad:
	// see workflowCandidates for why.
	//
	// Policy 决定推理请求在同一个路由 target 内部的候选排序。零值表示
	// PolicyLeastLoad，即此字段出现之前的行为。工作流提交始终使用 PolicyLeastLoad，
	// 原因见 workflowCandidates。
	Policy Policy
	// PrefixBytes caps how much of a chat request's leading messages
	// PolicyPrefixHash fingerprints. Zero uses defaultPrefixBytes (4096).
	// Ignored by every other policy.
	//
	// PrefixBytes 限定 PolicyPrefixHash 对聊天请求开头消息取指纹的最大字节数。零值使用
	// defaultPrefixBytes（4096）。其他策略忽略此字段。
	PrefixBytes int

	// QueueMaxWait bounds STATUS.md's P2 bounded task queueing for
	// SubmitWorkflow: once every currently connected workflow-capable
	// candidate has answered one submission attempt with a retryable
	// backpressure-style failure, SubmitWorkflow waits up to this long,
	// re-polling the candidate set, before giving up and returning that
	// failure — instead of returning it immediately, which is what happens
	// today and what the zero value (the default) still does. Queueing never
	// applies when no candidate exists at all (ErrNoCapableNode) or when a
	// failure is not retryable: see the P2 design doc's 有界任务排队 section
	// for why those two cases must not wait.
	//
	// QueueMaxWait 限定 STATUS.md P2 的有界任务排队：当每一个当前已连接、具备工作流
	// 能力的候选，在一轮提交尝试里都以可重试的背压类失败作答时，SubmitWorkflow 会
	// 等待至多这么久、期间重新轮询候选集，而不是像今天（以及零值默认）这样立即把
	// 失败返回给调用方。候选完全不存在（ErrNoCapableNode）或失败不可重试的情形永远
	// 不排队：理由见 P2 设计文档的「有界任务排队」一节。
	QueueMaxWait time.Duration
	// QueueRetryInterval is how often a queued submission re-polls the
	// candidate set while it waits. Zero (with QueueMaxWait positive) uses
	// defaultQueueRetryInterval. Ignored when QueueMaxWait is zero.
	QueueRetryInterval time.Duration
	// QueueMaxWaiters bounds how many submissions may be waiting at once, so
	// the queue itself cannot become the unbounded buffer the project's
	// security rules forbid ("任何一跳都不得无界缓冲"). A submission that
	// finds the bound already reached is rejected immediately rather than
	// becoming an unbounded third waiter axis. Zero (with QueueMaxWait
	// positive) uses defaultQueueMaxWaiters. Ignored when QueueMaxWait is
	// zero.
	QueueMaxWaiters int
}

// New returns a Scheduler that selects among the nodes connected to server.
// New 返回在连接到 server 的节点之间调度请求的 Scheduler。
func New(server *tunnelserver.Server, cfg Config) *Scheduler {
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	rec := newRecorder(cfg.Metrics)
	policy := cfg.Policy
	if policy == "" {
		policy = PolicyLeastLoad
	}
	prefixBytes := cfg.PrefixBytes
	if prefixBytes <= 0 {
		prefixBytes = defaultPrefixBytes
	}
	s := &Scheduler{
		server:            server,
		clock:             clock,
		breakers:          newBreakerRegistry(cfg.FailureThreshold, cfg.BaseCooldown, cfg.MaxCooldown, rec).withRateLimit(cfg.RateLimitCooldown, cfg.MaxRateLimitCooldown, logger),
		metrics:           rec,
		logger:            logger,
		policy:            policy,
		prefixBytes:       prefixBytes,
		intn:              rand.Intn,
		sessionAffinityOn: cfg.SessionAffinity,
	}
	s.routes.Store(cfg.Routes)

	if cfg.QueueMaxWait > 0 {
		interval := cfg.QueueRetryInterval
		if interval <= 0 {
			interval = defaultQueueRetryInterval
		}
		maxWaiters := cfg.QueueMaxWaiters
		if maxWaiters <= 0 {
			maxWaiters = defaultQueueMaxWaiters
		}
		s.queueMaxWait = cfg.QueueMaxWait
		s.queueRetryInterval = interval
		s.queueSlots = make(chan struct{}, maxWaiters)
	}
	return s
}

// SetRoutes atomically replaces routes for future requests. / SetRoutes 原子替换后续请求使用的路由。
func (s *Scheduler) SetRoutes(table *routing.Table) { s.routes.Store(table) }

// logDispatchDecision logs one dispatch attempt's outcome, keyed by the
// request_id carried on ctx — the same one tunnelserver.Dispatch reads back
// off ctx (common/reqid), so this line and the tunnel-side dispatch lines it
// precedes share one correlation key across the hop.
//
// logDispatchDecision 记录一次派发尝试的结果，以 ctx 上携带的 request_id 为键
// ——与 tunnelserver.Dispatch 从 ctx 读回的是同一个(common/reqid)，因此这一行
// 与它之后隧道侧的分发日志，跨这一跳共用同一个关联键。
func (s *Scheduler) logDispatchDecision(ctx context.Context, op string, c Candidate, err error) {
	s.logger.Info("scheduler dispatch decided",
		slog.String("request_id", reqid.FromContext(ctx)),
		slog.String("operation", op),
		slog.String("node_id", c.NodeID),
		slog.String("runtime_id", c.RuntimeID),
		slog.Bool("success", err == nil))
}

// Chat dispatches req to the best available node, retrying on the next
// candidate while the failure is Retryable.
func (s *Scheduler) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, Candidate, error) {
	ctx, admitted := admitModel(ctx, req.Model)
	if !admitted {
		return runtime.ChatResponse{}, Candidate{}, ErrModelNotAllowed
	}
	candidates := s.chatCandidates(ctx, req, runtime.CapabilityChat)
	s.metrics.Selection(runtime.CapabilityChat, len(candidates))
	if len(candidates) == 0 {
		return runtime.ChatResponse{}, Candidate{}, noCandidates(ctx)
	}
	var lastErr error
	var cooled cooledCandidates
	for _, c := range candidates {
		if !s.candidateAllowed(ctx, c) {
			lastErr = ErrNodeNotAllowed
			continue
		}
		if s.skipCooling(c, &cooled) {
			continue
		}
		resp, err := s.server.Runtime(c.NodeID, c.RuntimeID).Chat(ctx, withModel(req, c.Model))
		s.breakers.record(c, err, s.clock.Now())
		s.metrics.Dispatch(c, err)
		s.logDispatchDecision(ctx, "chat", c, err)
		if err == nil {
			return resp, c, nil
		}
		lastErr = err
		if !retryable(err) {
			return runtime.ChatResponse{}, c, err
		}
		s.metrics.Retry(runtime.CapabilityChat)
	}
	return runtime.ChatResponse{}, Candidate{}, cooled.or(lastErr)
}

// Embed dispatches req the same way Chat does.
func (s *Scheduler) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, Candidate, error) {
	ctx, admitted := admitModel(ctx, req.Model)
	if !admitted {
		return runtime.EmbeddingResponse{}, Candidate{}, ErrModelNotAllowed
	}
	candidates := s.candidates(ctx, req.Model, runtime.CapabilityEmbeddings)
	s.metrics.Selection(runtime.CapabilityEmbeddings, len(candidates))
	if len(candidates) == 0 {
		return runtime.EmbeddingResponse{}, Candidate{}, noCandidates(ctx)
	}
	var lastErr error
	var cooled cooledCandidates
	for _, c := range candidates {
		if !s.candidateAllowed(ctx, c) {
			lastErr = ErrNodeNotAllowed
			continue
		}
		if s.skipCooling(c, &cooled) {
			continue
		}
		resp, err := s.server.Runtime(c.NodeID, c.RuntimeID).Embed(ctx, withEmbedModel(req, c.Model))
		s.breakers.record(c, err, s.clock.Now())
		s.metrics.Dispatch(c, err)
		s.logDispatchDecision(ctx, "embed", c, err)
		if err == nil {
			return resp, c, nil
		}
		lastErr = err
		if !retryable(err) {
			return runtime.EmbeddingResponse{}, c, err
		}
		s.metrics.Retry(runtime.CapabilityEmbeddings)
	}
	return runtime.EmbeddingResponse{}, Candidate{}, cooled.or(lastErr)
}

// Rerank dispatches req the same way Chat and Embed do.
func (s *Scheduler) Rerank(ctx context.Context, req runtime.RerankRequest) (runtime.RerankResponse, Candidate, error) {
	ctx, admitted := admitModel(ctx, req.Model)
	if !admitted {
		return runtime.RerankResponse{}, Candidate{}, ErrModelNotAllowed
	}
	candidates := s.candidates(ctx, req.Model, runtime.CapabilityRerank)
	s.metrics.Selection(runtime.CapabilityRerank, len(candidates))
	if len(candidates) == 0 {
		return runtime.RerankResponse{}, Candidate{}, noCandidates(ctx)
	}
	var lastErr error
	var cooled cooledCandidates
	for _, c := range candidates {
		if !s.candidateAllowed(ctx, c) {
			lastErr = ErrNodeNotAllowed
			continue
		}
		if s.skipCooling(c, &cooled) {
			continue
		}
		resp, err := s.server.Runtime(c.NodeID, c.RuntimeID).Rerank(ctx, withRerankModel(req, c.Model))
		s.breakers.record(c, err, s.clock.Now())
		s.metrics.Dispatch(c, err)
		s.logDispatchDecision(ctx, "rerank", c, err)
		if err == nil {
			return resp, c, nil
		}
		lastErr = err
		if !retryable(err) {
			return runtime.RerankResponse{}, c, err
		}
		s.metrics.Retry(runtime.CapabilityRerank)
	}
	return runtime.RerankResponse{}, Candidate{}, cooled.or(lastErr)
}

// TranscriptionCandidates ranks the nodes able to serve req.Model with
// CapabilityAudioTranscription. It is exported, unlike Chat/Embed's own
// candidates() call, because a caller must pick one candidate itself before
// Transcribe starts streaming audio — see Transcribe's doc comment for why.
//
// TranscriptionCandidates 对能以 CapabilityAudioTranscription 服务 req.Model
// 的节点排序。与 Chat/Embed 内部直接调用的 candidates() 不同，这里导出，是
// 因为调用方必须在 Transcribe 开始流式发送音频之前自己选定一个候选——原因
// 见 Transcribe 的文档注释。
//
// Candidates still cooling down after a rate limit are moved to the back:
// the caller dispatches to the first one only, so this is the one place the
// cooldown can act.
//
// 仍在限流冷却中的候选被移到最后：调用方只派发给第一个候选，因此这里是冷却唯一能起
// 作用的地方。
func (s *Scheduler) TranscriptionCandidates(ctx context.Context, model string) []Candidate {
	ctx, _ = admitModel(ctx, model) // own nodes only for an excluded model / 被排除的模型只用自有节点
	return s.coolingLast(s.candidates(ctx, model, runtime.CapabilityAudioTranscription))
}

// coolingLast moves the candidates still cooling down after a rate limit
// behind the rest, keeping each group's order.
// coolingLast 把仍在限流冷却中的候选移到其余候选之后，各组内部保持原有顺序。
func (s *Scheduler) coolingLast(candidates []Candidate) []Candidate {
	now := s.clock.Now()
	cooling := make(map[Candidate]bool, len(candidates))
	for _, c := range candidates {
		_, cooling[c] = s.breakers.limited(c, now)
	}
	slices.SortStableFunc(candidates, func(a, b Candidate) int {
		switch {
		case cooling[a] == cooling[b]:
			return 0
		case cooling[a]:
			return 1
		}
		return -1
	})
	return candidates
}

// Transcribe streams audio to a single, already-chosen candidate for a
// transcription or translation task. Unlike Chat/Embed it is not a
// multi-candidate retry loop: once bytes start flowing to c, retrying on a
// different candidate would require the caller to re-read the whole file
// from the start, the same reasoning UploadInput's doc comment gives for not
// retrying an upload across nodes.
//
// Transcribe 把音频流式发送给一个已经选定的候选，用于一次转录或翻译任务。
// 与 Chat/Embed 不同，它不是多候选重试循环：字节一旦开始流向 c，换一个候选
// 重试就需要调用方从头重新读一遍整个文件——与 UploadInput 文档注释里「不跨
// 节点重试一次上传」给出的理由相同。
func (s *Scheduler) Transcribe(ctx context.Context, c Candidate, req runtime.AudioTranscriptionRequest, audio io.Reader) (runtime.AudioTranscriptionResponse, error) {
	ctx, admitted := admitModel(ctx, req.Model)
	if !admitted {
		return runtime.AudioTranscriptionResponse{}, ErrModelNotAllowed
	}
	if !s.candidateAllowed(ctx, c) {
		return runtime.AudioTranscriptionResponse{}, ErrNodeNotAllowed
	}
	resp, err := s.server.Runtime(c.NodeID, c.RuntimeID).Transcribe(ctx, req, audio)
	s.breakers.record(c, err, s.clock.Now())
	s.metrics.Dispatch(c, err)
	return resp, err
}

// ChatStream dispatches req and returns a stream already positioned to
// deliver its first event (or io.EOF for a valid empty response). Whether a
// request "has produced output yet" can only be answered by reading from the
// stream, so ChatStream reads the first event itself: a failure before that
// point retries on the next candidate, and a failure after it is handed to
// the caller exactly as the underlying Stream reports it, with no further
// node switch.
func (s *Scheduler) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], Candidate, error) {
	ctx, admitted := admitModel(ctx, req.Model)
	if !admitted {
		return nil, Candidate{}, ErrModelNotAllowed
	}
	candidates := s.chatCandidates(ctx, req, runtime.CapabilityChatStream)
	s.metrics.Selection(runtime.CapabilityChatStream, len(candidates))
	if len(candidates) == 0 {
		return nil, Candidate{}, noCandidates(ctx)
	}
	var lastErr error
	var cooled cooledCandidates
	for _, c := range candidates {
		if !s.candidateAllowed(ctx, c) {
			lastErr = ErrNodeNotAllowed
			continue
		}
		if s.skipCooling(c, &cooled) {
			continue
		}
		stream, err := s.server.Runtime(c.NodeID, c.RuntimeID).ChatStream(ctx, withModel(req, c.Model))
		if err != nil {
			s.breakers.record(c, err, s.clock.Now())
			s.metrics.Dispatch(c, err)
			s.logDispatchDecision(ctx, "chat_stream", c, err)
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
			s.logDispatchDecision(ctx, "chat_stream", c, nil)
			return &prefetchStream[runtime.ChatEvent]{first: first, hasFirst: true, underlying: stream}, c, nil
		}
		stream.Close()
		if err == io.EOF {
			// A valid, empty response: nothing was produced, but nothing
			// failed either. Do not retry a successful call.
			s.breakers.record(c, nil, s.clock.Now())
			s.metrics.Dispatch(c, nil)
			s.logDispatchDecision(ctx, "chat_stream", c, nil)
			return &prefetchStream[runtime.ChatEvent]{eof: true}, c, nil
		}
		s.breakers.record(c, err, s.clock.Now())
		s.metrics.Dispatch(c, err)
		s.logDispatchDecision(ctx, "chat_stream", c, err)
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
	return nil, Candidate{}, cooled.or(lastErr)
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
// Backend display positions precede unranked models; equal positions use ID order.
// Either way a model ctx's WithModelAccess policy excludes is left out unless
// the caller's own node serves it (see admitModel), and so is a node ctx's
// WithNodeAccess policy closes, so the catalogue a tenant sees is exactly what
// it may call. With a routing table, models on the caller's own nodes that no
// alias names are listed by their own id, the id they are requested by.
//
// Models 是公开目录：客户端此刻可以请求什么。
//
// 有路由表时它列出别名，而不是其背后的真实模型 id——两者都公布，等于邀请客户端绑定到
// 某个运行时模型，而那正是别名存在所要防止的事。没有任何活节点能服务的别名会被略去：
// 一个用起来就 404 的目录条目，比一个缺失的条目更糟。
//
// 没有路由表时它列出节点声明的内容，因此从不编写路由表的部署不受影响。
// 后端显示位置排在未指定位置的模型之前；相同位置按 ID 排序。
// 两种情况下，被 ctx 上 WithModelAccess 授权排除的模型（调用方自有节点提供的除外，见
// admitModel）、被 WithNodeAccess 授权关闭的节点都会略去，使租户看到的目录恰好就是它能
// 调用的内容。有路由表时，调用方自有节点上没有别名指向的模型按原 id 列出，即请求它所用的 id。
func (s *Scheduler) Models(ctx context.Context) []ModelInfo {
	routes := s.routes.Load()
	// One node view serves the whole call, so the catalogue costs one pass
	// over the node list instead of one per alias and capability probe. It is
	// also a consistent picture: every alias is judged against the same fleet.
	//
	// 整个调用共用一份节点视图，目录因此只遍历一遍节点列表，而不是每个别名、每次能力
	// 探测各一遍。它也是一致的：所有别名都针对同一份机群状态做判断。
	view := s.acquireView()
	defer releaseView(view)
	if routes.Len() > 0 {
		var out []ModelInfo
		aliases := make(map[string]bool)
		for _, alias := range routes.Models() {
			aliases[alias] = true
			actx, admitted := admitModel(ctx, alias)
			if !admitted {
				continue
			}
			if s.hasCandidate(actx, routes, alias, runtime.CapabilityChat, view) || s.hasCandidate(actx, routes, alias, runtime.CapabilityMessages, view) ||
				s.hasCandidate(actx, routes, alias, runtime.CapabilityImageGeneration, view) {
				out = append(out, ModelInfo{ID: alias})
			}
		}
		// A model on the caller's own node is requested by its own id when
		// no alias names it, so it is listed the same way.
		// 调用方自有节点上的模型在没有别名指向它时按原 id 请求，因此也按原 id 列出。
		for _, info := range s.advertisedModels(ctx, true, view) {
			if !aliases[info.ID] {
				out = append(out, info)
			}
		}
		return out
	}
	return s.advertisedModels(ctx, false, view)
}

// hasCandidate reports whether candidatesOn would return at least one
// candidate for model with cap, judged against view. It walks the same nodes
// through the same filters and breaker checks as candidatesOn does, but skips
// ordering and building the candidate list nobody reads.
//
// hasCandidate 报告针对 view 判断时，candidatesOn 是否至少会为带 cap 的 model 返回一个
// 候选。它经由与 candidatesOn 相同的过滤与熔断检查遍历同样的节点，只是略去了排序，以及
// 构造一份没人会读的候选列表。
func (s *Scheduler) hasCandidate(ctx context.Context, routes *routing.Table, model string, cap runtime.Capability, view *nodeView) bool {
	targets, routed := routes.Resolve(model)
	if !routed {
		targets = []routing.Target{{RuntimeModel: model}}
	}
	any := false
	for _, target := range targets {
		if len(s.collect(ctx, view, target, modelEligible(target, cap))) > 0 {
			any = true
		}
	}
	return any
}

// advertisedModels lists the chat, Messages and image models live nodes
// advertise, deduplicated by id and sorted by modelInfoLess. view is the node
// view it judges against. A node ctx's
// WithNodeAccess policy closes is skipped. A model the WithModelAccess policy
// excludes is kept only from the caller's own nodes; ownOnly keeps nothing
// else.
//
// advertisedModels 列出活节点声明的聊天、Messages 与图像模型，按 id 去重并按
// modelInfoLess 排序。被 ctx 上 WithNodeAccess 授权关闭的节点被跳过。被 WithModelAccess
// 授权排除的模型只从调用方自有节点保留；ownOnly 时其他一概不保留。
func (s *Scheduler) advertisedModels(ctx context.Context, ownOnly bool, view *nodeView) []ModelInfo {
	_, hasTenant := ctx.Value(nodeAccessKey{}).(nodeAccess)
	own := context.WithValue(ctx, ownNodesOnlyKey{}, true)
	seen := make(map[string]ModelInfo)
	for i := range view.nodes {
		node := &view.nodes[i]
		if !node.Live || node.Draining || node.Maintenance || !nodeAllowed(ctx, node.NodeID) {
			continue
		}
		owned := hasTenant && nodeAllowed(own, node.NodeID)
		if ownOnly && !owned {
			continue
		}
		for r := range node.Runtimes {
			snap := &node.Runtimes[r]
			if !s.accountAllowed(ctx, node.NodeID, snap.Descriptor.ID) {
				continue
			}
			if !runtimeHealthy(snap.State) {
				continue
			}
			for _, m := range snap.Discovery.Models {
				if !owned && !modelAllowed(ctx, m.ID) {
					continue
				}
				if !modelServes(snap.Discovery.Capabilities, m.Capabilities, runtime.CapabilityChat) &&
					!modelServes(snap.Discovery.Capabilities, m.Capabilities, runtime.CapabilityMessages) &&
					!modelServes(snap.Discovery.Capabilities, m.Capabilities, runtime.CapabilityImageGeneration) {
					continue
				}
				info := ModelInfo{ID: m.ID, DisplayName: m.DisplayName, DisplayOrder: m.DisplayOrder}
				if prior, ok := seen[m.ID]; !ok || modelInfoLess(info, prior) {
					seen[m.ID] = info
				}
			}
		}
	}
	out := make([]ModelInfo, 0, len(seen))
	for _, info := range seen {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return modelInfoLess(out[i], out[j]) })
	return out
}

// modelInfoLess keeps backend positions first and resolves ties deterministically.
// modelInfoLess 优先采用后端目录位置，并以确定性规则处理相同位置。
func modelInfoLess(a, b ModelInfo) bool {
	if (a.DisplayOrder == 0) != (b.DisplayOrder == 0) {
		return a.DisplayOrder != 0
	}
	if a.DisplayOrder != b.DisplayOrder {
		return a.DisplayOrder < b.DisplayOrder
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	if (a.DisplayName == "") != (b.DisplayName == "") {
		return a.DisplayName != ""
	}
	return a.DisplayName < b.DisplayName
}

// candidates resolves the requested model through the routing table and
// returns every (node, runtime, runtime-model) triple that can serve it, best
// first.
//
// The ordering has two levels, and the outer one is the operator's. Targets
// are tried in priority order, with weighted choice among equal priorities;
// only within one target does the load heuristic decide. That is what makes "the local Mac
// before the rented GPU" mean what it says: a stated preference that a
// momentarily idler machine cannot override.
//
// candidates 把请求的模型经路由表解析，返回每一个能服务它的 (node, runtime, 运行时
// 模型) 三元组，最优的在前。
//
// 外层按运维配置的优先级依次尝试，同优先级目标按权重选择；只有在同一个 target 内部
// 才由负载启发式决定。这正是「先用本地那台 Mac，再用租来的 GPU」名副其实的原因：一个
// 声明的偏好，不会被一台一时更空闲的机器推翻。
func (s *Scheduler) candidates(ctx context.Context, model string, cap runtime.Capability) []Candidate {
	return s.candidatesWith(ctx, s.routes.Load(), model, cap)
}

// chatCandidates is candidates for a chat request, which alone carries a
// prompt prefix for PolicyPrefixHash to pin. A session key on ctx (see
// WithSessionKey) takes precedence over the prefix: it names the conversation
// outright instead of inferring it. The fingerprint is only computed when
// PolicyPrefixHash is active and no session key is present, so the other
// policies pay nothing for it.
//
// chatCandidates 是聊天请求版的 candidates：只有聊天请求携带可供 PolicyPrefixHash
// 固定的提示前缀。ctx 上的会话 key（见 WithSessionKey）优先于前缀：它直接点名对话，
// 而不是去推断。指纹只在 PolicyPrefixHash 生效且没有会话 key 时才计算，其他策略不为此
// 付出任何代价。
func (s *Scheduler) chatCandidates(ctx context.Context, req runtime.ChatRequest, cap runtime.Capability) []Candidate {
	key := s.sessionAffinity(ctx)
	if !key.ok && s.policy == PolicyPrefixHash {
		key = prefixAffinity(req, s.prefixBytes)
	}
	return s.candidatesKeyed(ctx, s.routes.Load(), req.Model, cap, key, rand.Float64)
}

func (s *Scheduler) candidatesWith(ctx context.Context, routes *routing.Table, model string, cap runtime.Capability) []Candidate {
	return s.candidatesWithRandom(ctx, routes, model, cap, rand.Float64)
}

func (s *Scheduler) candidatesWithRandom(ctx context.Context, routes *routing.Table, model string, cap runtime.Capability, draw func() float64) []Candidate {
	return s.candidatesKeyed(ctx, routes, model, cap, affinity{}, draw)
}

// candidatesKeyed is the shared body of every candidates variant: key is the
// request's prefix fingerprint, empty for requests that have none.
//
// candidatesKeyed 是各 candidates 变体共用的实现：key 是请求的前缀指纹，没有前缀的
// 请求传空值。
func (s *Scheduler) candidatesKeyed(ctx context.Context, routes *routing.Table, model string, cap runtime.Capability, key affinity, draw func() float64) []Candidate {
	return s.candidatesOn(ctx, routes, model, cap, key, draw, nil)
}

// candidatesOn is candidatesKeyed against a caller-held node view, so a
// caller that resolves several models in one go (Models) walks the node list
// once. A nil view takes a fresh one for each routing target.
//
// candidatesOn 是针对调用方持有的节点视图的 candidatesKeyed，使一次解析多个模型的调用方
// （Models）只需遍历一遍节点列表。view 为 nil 时，每个路由 target 各取一份新的视图。
func (s *Scheduler) candidatesOn(ctx context.Context, routes *routing.Table, model string, cap runtime.Capability, key affinity, draw func() float64, view *nodeView) []Candidate {
	targets, routed := routes.Resolve(model)
	if !routed {
		// An unrouted model is used exactly as the node advertises it. A
		// deployment with no routing table is the common case, not an error.
		//
		// 未配置路由的模型按节点声明的原样使用。没有路由表的部署是常态，不是错误。
		targets = []routing.Target{{RuntimeModel: model}}
	}

	groups := make([]targetCandidates, 0, len(targets))
	for _, target := range targets {
		groups = append(groups, targetCandidates{priority: target.Priority, weight: target.Weight, candidates: s.pick(ctx, target, cap, key, view)})
	}
	return weightedCandidates(groups, draw)
}

// pick returns every (node, runtime) pair that can serve target with cap,
// ordered best first by the configured Policy — by default most idle
// inference slots, then fewest in-flight requests, matching README's
// "最少正在执行请求" strategy. Nodes are shuffled before that sort so requests
// do not all pile onto the same node whenever several are tied.
//
// pick 返回每一个能以 cap 服务 target 的 (node, runtime) 对，按配置的 Policy 最优的
// 在前——默认是空闲推理槽最多的优先，其次是在途请求最少的，对应 README 的「最少正在
// 执行请求」策略。排序前先打乱节点顺序，这样在多个节点打平时请求不会全堆到同一个上。
func (s *Scheduler) pick(ctx context.Context, target routing.Target, cap runtime.Capability, key affinity, view *nodeView) []Candidate {
	policy := s.policy
	if key.session {
		policy = PolicyPrefixHash
	}
	return s.pickOn(ctx, view, target, policy, key, modelEligible(target, cap))
}

// modelEligible is the eligibility test for an inference request: the runtime
// serves target's runtime model with cap.
//
// modelEligible 是推理请求的资格判定：该运行时以 cap 服务 target 的运行时模型。
func modelEligible(target routing.Target, cap runtime.Capability) func(*runtime.Snapshot) bool {
	return func(snap *runtime.Snapshot) bool {
		for i := range snap.Discovery.Models {
			m := &snap.Discovery.Models[i]
			if m.ID != target.RuntimeModel {
				continue
			}
			if modelServes(snap.Discovery.Capabilities, m.Capabilities, cap) {
				return true
			}
		}
		return false
	}
}

// modelServes reports whether a model with modelCaps on a runtime with
// runtimeCaps supports cap: runtime.Intersect(runtimeCaps, modelCaps).
// Require(cap) == nil, computed for the one capability asked about. Intersect
// builds a map of every capability, which this per-node, per-model test ran
// on each request; the answer for one capability needs none of it.
//
// modelServes 报告运行时能力为 runtimeCaps 的运行时上、能力为 modelCaps 的模型是否支持
// cap：等价于 runtime.Intersect(runtimeCaps, modelCaps).Require(cap) == nil，只针对被问
// 到的那一个能力计算。Intersect 会为所有能力构造一个 map，而这个按节点、按模型的判断每个
// 请求都要跑一遍；一个能力的答案用不着其中任何东西。
func modelServes(runtimeCaps, modelCaps runtime.CapabilitySet, cap runtime.Capability) bool {
	if modelEv, ok := modelCaps[cap]; ok {
		if runtimeEv, ok := runtimeCaps[cap]; ok && runtimeEv.Level == runtime.SupportUnsupported {
			return false
		}
		return modelEv.Level == runtime.SupportSupported
	}
	runtimeEv, ok := runtimeCaps[cap]
	return ok && runtimeEv.Level == runtime.SupportSupported
}

// nodeView is a reusable buffer of the scheduler's node view. pickBy borrows
// one per call; Models holds one across all its probes. A holder may reorder
// nodes freely — selection shuffles them — but must not retain the view after
// releaseView.
//
// nodeView 是调度器节点视图的可复用缓冲。pickBy 每次调用借用一个；Models 在其全部探测
// 期间持有一个。持有者可以随意重排 nodes——选择流程会打乱它们——但在 releaseView 之后
// 不得继续持有该视图。
type nodeView struct {
	nodes []tunnelserver.SchedNode
	// found is the scratch buffer pickOn collects scored candidates into. It
	// is kept with the view so a selection does not regrow a few hundred
	// entries from empty on every request.
	//
	// found 是 pickOn 收集带评分候选所用的暂存缓冲。它随视图保留，这样每个请求的选择过程
	// 就不必从零开始反复扩容出几百个条目。
	found []scored
}

var nodeViewPool = sync.Pool{New: func() any { return new(nodeView) }}

// acquireView returns a node view of the nodes connected right now.
// acquireView 返回此刻已连接节点的节点视图。
func (s *Scheduler) acquireView() *nodeView {
	v := nodeViewPool.Get().(*nodeView)
	v.nodes = s.server.AppendSchedNodes(v.nodes[:0])
	return v
}

// releaseView returns v to the pool, dropping its references so a pooled
// buffer does not keep a departed node's inventory alive.
//
// releaseView 把 v 还给池，并清除其中的引用，使池中的缓冲不会让已离开节点的清单一直
// 存活。
func releaseView(v *nodeView) {
	clear(v.nodes)
	v.nodes = v.nodes[:0]
	clear(v.found)
	v.found = v.found[:0]
	nodeViewPool.Put(v)
}

// pickBy is the shared selection walk: node health, the target's node
// selector, ctx's WithNodeAccess policy, the caller's own eligibility test,
// and the circuit breaker, then the ordering policy names (see
// orderCandidates).
//
// What "eligible" means is the caller's to define — a model plus a capability
// for inference, a runtime-level capability for workflows — so that the two
// share one health filter, one breaker check and one ranking rather than
// growing two copies that drift.
//
// pickBy 是共用的选择流程：节点健康、target 的节点选择器、ctx 上的 WithNodeAccess
// 授权、调用方自己的资格判定、熔断器，然后按 policy 指定的方式排序（见
// orderCandidates）。
//
// 「有资格」的含义由调用方定义——推理是模型加能力，工作流是 runtime 层的能力——这样
// 两者共用同一套健康过滤、同一次熔断检查与同一套排序，而不是长出两份会各自漂移的拷贝。
func (s *Scheduler) pickBy(ctx context.Context, target routing.Target, policy Policy, key affinity, eligible func(*runtime.Snapshot) bool) []Candidate {
	return s.pickOn(ctx, nil, target, policy, key, eligible)
}

// pickOn is pickBy against a caller-held node view; a nil view takes a fresh
// one for this call. Node state is read once, when the view is built: idle
// slots, in-flight count, liveness and the drain and maintenance flags are as
// current as that moment, exactly as fresh as the Nodes() call this replaces
// made them, and the inventory is whatever each node last published.
//
// pickOn 是针对调用方持有的节点视图的 pickBy；view 为 nil 时为本次调用取一份新的。节点
// 状态在构造视图时读取一次：空闲槽、在途数、存活以及 draining 与 maintenance 标志与那一
// 刻一样新，和它所取代的 Nodes() 调用所给出的新鲜度完全一致；清单则是各节点最近一次
// 发布的内容。
func (s *Scheduler) pickOn(ctx context.Context, view *nodeView, target routing.Target, policy Policy, key affinity, eligible func(*runtime.Snapshot) bool) []Candidate {
	if view == nil {
		view = s.acquireView()
		defer releaseView(view)
	}
	nodes := view.nodes
	rand.Shuffle(len(nodes), func(i, j int) { nodes[i], nodes[j] = nodes[j], nodes[i] })

	found := s.collect(ctx, view, target, eligible)
	orderCandidates(found, policy, key, s.intn)

	candidates := make([]Candidate, len(found))
	for i, f := range found {
		candidates[i] = f.Candidate
	}
	return candidates
}

// collect walks view's nodes in their current order and returns every
// (node, runtime) pair that passes the filters pickBy documents, unordered.
// The result lives in view's scratch buffer and is valid only until the next
// collect on the same view.
//
// collect 按当前顺序遍历 view 中的节点，返回通过 pickBy 所述各项过滤的每一个
// (node, runtime) 对，未排序。结果位于 view 的暂存缓冲中，仅在同一视图上下一次 collect
// 之前有效。
func (s *Scheduler) collect(ctx context.Context, view *nodeView, target routing.Target, eligible func(*runtime.Snapshot) bool) []scored {
	nodes := view.nodes
	now := s.clock.Now()

	found := view.found[:0]
	for i := range nodes {
		node := &nodes[i]
		if !node.Live || node.Draining || node.Maintenance {
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
		// A node dedicated to another tenant is skipped at the same point,
		// but as a separate check: the selector reads labels the Agent
		// declared, while this reads the control plane's binding by node id,
		// and the two must never stand in for each other.
		//
		// 专属于其他租户的节点在同一时机被跳过，但作为一次独立检查：选择器读的是
		// Agent 自己声明的标签，这里读的是控制面按节点 id 建立的绑定，两者绝不能互相
		// 替代。
		if !nodeAllowed(ctx, node.NodeID) {
			continue
		}
		// STATUS.md's P2 admission-threshold filtering: a node whose
		// declared GPU memory total cannot meet the target's requirement is
		// excluded before either runtime on it is even looked at, the same
		// way the node selector is. Checked once per node, not per runtime,
		// because NodeResources describes the machine, not any one runtime
		// instance on it.
		//
		// STATUS.md P2 的准入门槛过滤：一个声明的 GPU 显存总量达不到 target 要求的
		// 节点，在其上任何一个 runtime 被看到之前就被排除，与节点选择器同一时机。
		// 按节点而非按 runtime 检查一次，因为 NodeResources 描述的是这台机器，不是
		// 它上面的某一个 runtime 实例。
		if !nodeHasCapacity(node, target.MinGPUMemoryBytes) {
			continue
		}
		idle := node.IdleInference
		for r := range node.Runtimes {
			snap := &node.Runtimes[r]
			if !s.accountAllowed(ctx, node.NodeID, snap.Descriptor.ID) {
				continue
			}
			if !runtimeHealthy(snap.State) {
				continue
			}
			if !eligible(snap) {
				continue
			}
			candidate := Candidate{NodeID: node.NodeID, RuntimeID: snap.Descriptor.ID, Kind: snap.Descriptor.Kind, Model: target.RuntimeModel}
			if !s.breakers.eligible(candidate, now) {
				continue
			}
			found = append(found, scored{
				Candidate: candidate,
				idle:      idle,
				queued:    snap.Health.QueueRunning + snap.Health.QueuePending,
				inflight:  node.InflightRequests,
			})
		}
	}

	// Keep the (possibly regrown) buffer with the view for the next selection;
	// releaseView clears what it holds.
	// 把（可能已扩容的）缓冲留在视图里供下次选择使用；其中的内容由 releaseView 清除。
	view.found = found
	return found
}

// nodeHasCapacity reports whether node's declared GPU memory total meets
// minBytes. minBytes <= 0 means the target set no requirement, which always
// passes. A node that has not reported hardware, or reported zero GPU
// memory, also always passes: an undeclared capacity is not evidence of
// insufficient capacity — see modelroute.Target.MinGPUMemoryBytes's doc
// comment for why this default-open behavior is deliberate.
//
// nodeHasCapacity 报告 node 声明的 GPU 显存总量是否达到 minBytes。minBytes <= 0
// 表示 target 未设置要求，永远通过。一个尚未上报硬件、或上报零 GPU 显存的节点同样
// 永远通过：未声明的容量不是容量不足的证据——这个默认放行的行为为何是刻意的，见
// modelroute.Target.MinGPUMemoryBytes 的文档注释。
func nodeHasCapacity(node *tunnelserver.SchedNode, minBytes int64) bool {
	if minBytes <= 0 || node.Resources == nil || node.Resources.GpuMemoryBytes <= 0 {
		return true
	}
	return node.Resources.GpuMemoryBytes >= minBytes
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

// withRerankModel is withModel for rerank requests.
//
// withRerankModel 是 rerank 请求版的 withModel。
func withRerankModel(req runtime.RerankRequest, model string) runtime.RerankRequest {
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
// to the underlying Stream, so ChatStream and MessagesStart can peek at the
// first event to decide whether to retry without losing it.
//
// prefetchStream 先重放已收到的首个事件，再交给底层 Stream，使 ChatStream 与
// MessagesStart 能先看首个事件来决定是否重试，又不丢失该事件。
type prefetchStream[T any] struct {
	first      T
	hasFirst   bool
	eof        bool
	underlying runtime.Stream[T]
}

func (p *prefetchStream[T]) Recv() (T, error) {
	if p.hasFirst {
		p.hasFirst = false
		return p.first, nil
	}
	if p.eof {
		var zero T
		return zero, io.EOF
	}
	return p.underlying.Recv()
}

func (p *prefetchStream[T]) Committed() bool {
	if p.underlying == nil {
		return false
	}
	return p.underlying.Committed()
}

func (p *prefetchStream[T]) Close() error {
	if p.underlying == nil {
		return nil
	}
	return p.underlying.Close()
}

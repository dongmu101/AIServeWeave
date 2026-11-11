// Package claudecode provides locally authenticated Claude Code Messages inference.
// Package claudecode 提供使用本机登录状态的 Claude Code Messages 推理。
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveAgent/attachments"
	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

// Runtime owns bounded CLI sessions; callers execute all requested tools.
// Runtime 管理有界的 CLI 会话；所有请求的工具均由调用方执行。
type Runtime struct {
	cfg      runtime.Config
	bridge   *messagesBridge
	root     context.Context
	cancel   context.CancelFunc
	modelsMu sync.RWMutex
	models   map[string]struct{}
	logger   *slog.Logger
}

var _ runtime.MessagesRuntime = (*Runtime)(nil)

// New constructs a locally declared Claude adapter without launching a process.
// New 构造本地声明的 Claude 适配器，不启动进程。
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	return NewFactory("claude")(cfg, deps)
}

// NewFactory binds an executable supplied by local administration only.
// NewFactory 绑定仅由本地管理员提供的可执行文件。
func NewFactory(executable string) runtime.Factory {
	return NewFactoryWithAttachments(executable, nil)
}

// NewFactoryWithAttachments binds shared, locally configured converters.
// NewFactoryWithAttachments 绑定共享、仅由本地配置的附件转换器。
func NewFactoryWithAttachments(executable string, processor *attachments.Processor) runtime.Factory {
	return func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		requested := cfg.MaxConcurrent // before Normalize fills the generic default / 在 Normalize 填入通用默认值之前读取
		cfg = cfg.Normalize()
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		if cfg.Kind != runtime.KindClaude || executable == "" || deps.Clock == nil {
			return nil, claudeError(runtime.ErrorInvalidConfig)
		}
		cfg.MaxConcurrent = sessionCapacity(requested)
		root, cancel := context.WithCancel(context.Background())
		bridge := newMessagesBridge(executable, "sonnet", nil, deps.Clock)
		if processor != nil {
			bridge.processor = processor
		}
		bridge.capacity = cfg.MaxConcurrent
		bridge.env = withAccountDir(probeEnv(os.Environ()), cfg.AccountDir)
		logger := deps.Logger
		if logger == nil {
			logger = slog.Default()
		}
		return &Runtime{cfg: cfg, bridge: bridge, root: root, cancel: cancel, logger: logger}, nil
	}
}

// Session capacity. Every session is a resident CLI process that may wait for
// tool results for hours, so even an unlimited configuration stays bounded.
//
// 会话容量。每个会话都是一个可能为等待工具结果驻留数小时的 CLI 进程，因此即使配置
// 为不限并发也保持有界。
const (
	// defaultSessions applies when max_concurrent is not configured.
	// defaultSessions 用于未配置 max_concurrent 的情况。
	defaultSessions = 8
	// maxSessions caps any configured value, including the negative "unlimited".
	// maxSessions 限制任何配置值，包括表示不限并发的负值。
	maxSessions = 32
)

// sessionCapacity maps the raw max_concurrent setting to resident sessions:
// unset uses defaultSessions, and anything else is clamped to maxSessions.
//
// sessionCapacity 将原始 max_concurrent 配置换算为驻留会话数：未配置时取
// defaultSessions，其余值收敛到 maxSessions 以内。
func sessionCapacity(requested int) int {
	switch {
	case requested == 0:
		return defaultSessions
	case requested < 0:
		return maxSessions
	default:
		return min(requested, maxSessions)
	}
}

func claudeError(code runtime.ErrorCode) error {
	return &runtime.RuntimeError{Code: code, Kind: runtime.KindClaude, Operation: "messages", Message: "Claude Messages operation failed: " + string(code)}
}

// Descriptor returns non-secret identity and the bounded process capacity.
// Descriptor 返回不含凭据的身份与有界进程容量。
func (r *Runtime) Descriptor() runtime.Descriptor {
	return runtime.Descriptor{ID: r.cfg.ID, Kind: runtime.KindClaude, MaxConcurrent: r.cfg.MaxConcurrent}
}

// Close cancels and reaps every session and authentication check.
// Close 取消并回收全部会话及认证检查。
func (r *Runtime) Close() error { r.cancel(); r.bridge.close(); return nil }

func (r *Runtime) inspect(parent context.Context, discover bool) ([]string, error) {
	r.bridge.mu.Lock()
	if r.bridge.closed {
		r.bridge.mu.Unlock()
		return nil, claudeError(runtime.ErrorClosed)
	}
	r.bridge.wg.Add(1)
	r.bridge.mu.Unlock()
	defer r.bridge.wg.Done()
	ctx, cancel := context.WithCancel(parent)
	stopRoot := context.AfterFunc(r.root, cancel)
	timer, stopTimer := r.bridge.clock.NewTimer(r.cfg.ProbeTimeout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
		case <-timer:
			cancel()
		}
	}()
	defer func() { cancel(); stopRoot(); stopTimer(); <-done }()
	if checkAuth(ctx, r.bridge.program, r.bridge.cliEnv()) != nil {
		return nil, claudeError(runtime.ErrorUnauthorized)
	}
	if !discover {
		return nil, nil
	}
	models, err := discoverModels(ctx, r.bridge.program, r.bridge.cliEnv())
	if err != nil {
		return nil, claudeError(runtime.ErrorProtocol)
	}
	return models, nil
}

// claudeCapabilities is the runtime profile with cfg.CapabilityOverrides
// applied on top. Overriding anthropic_messages_native to unsupported keeps
// this node's unguarded requests on the compatibility bridge.
//
// claudeCapabilities 是运行时自身声明的能力，再叠加 cfg.CapabilityOverrides。把
// anthropic_messages_native 覆盖为 unsupported 后，本节点不带 safeguards 的请求继续走兼容桥接。
func claudeCapabilities(cfg runtime.Config) runtime.CapabilitySet {
	profile := runtime.CapabilitySet{
		runtime.CapabilityMessages:       {Capability: runtime.CapabilityMessages, Level: runtime.SupportSupported, Source: runtime.SourceRuntimeProfile, Detail: "local Claude Code Messages sessions"},
		runtime.CapabilityMessagesNative: {Capability: runtime.CapabilityMessagesNative, Level: runtime.SupportSupported, Source: runtime.SourceRuntimeProfile, Detail: "unchanged Messages documents sent through the local Claude Code login"},
	}
	overrides := make(runtime.CapabilitySet, len(cfg.CapabilityOverrides))
	for capability, level := range cfg.CapabilityOverrides {
		overrides[capability] = runtime.CapabilityEvidence{Capability: capability, Level: level, Source: runtime.SourceConfigOverride, Detail: "declared by runtime configuration"}
	}
	return runtime.Merge(profile, overrides)
}

// Probe verifies native subscription login without spending model quota.
// Probe 验证原生订阅登录，不消耗模型额度。
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	_, err := r.inspect(ctx, false)
	return runtime.ProbeResult{Kind: runtime.KindClaude, IdentityVerified: err == nil, ProbedAt: r.bridge.clock.Now()}, err
}

// Health verifies that the local subscription login remains available.
// Health 验证本机订阅登录仍可用。
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	_, err := r.inspect(ctx, false)
	state := runtime.StateHealthy
	if err != nil {
		state = runtime.StateUnhealthy
	}
	return runtime.HealthReport{State: state, CheckedAt: r.bridge.clock.Now()}, err
}

// Discover reads the CLI model catalogue without inference and publishes
// Messages capability and the CLI version reported by `claude --version`;
// Version stays empty when the CLI does not report one.
//
// Discover 不执行推理，读取 CLI 模型目录，发布 Messages 能力以及 `claude --version`
// 报告的 CLI 版本；CLI 未报告版本时 Version 留空。
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	models, err := r.inspect(ctx, true)
	if err != nil {
		return runtime.Discovery{}, err
	}
	r.modelsMu.Lock()
	r.models = make(map[string]struct{}, len(models))
	for _, id := range models {
		r.models[id] = struct{}{}
	}
	r.modelsMu.Unlock()
	caps := claudeCapabilities(r.cfg)
	list := make([]runtime.Model, 0, len(models))
	for i, id := range models {
		list = append(list, runtime.Model{ID: id, DisplayName: id, DisplayOrder: uint32(i + 1), Capabilities: caps})
	}
	version := cliprocess.Version(ctx, r.bridge.program, r.bridge.env)
	return runtime.Discovery{Version: version, Capabilities: caps, Models: list, DiscoveredAt: r.bridge.clock.Now()}, nil
}

type nativeItem struct {
	event runtime.MessagesEvent
	err   error
}
type nativeStream struct {
	ctx                         context.Context
	cancel                      context.CancelFunc
	s                           *messagesSession
	items                       chan nativeItem
	done                        chan struct{}
	mu                          sync.Mutex
	complete, committed, closed bool
}

// Messages validates native semantics and preserves an existing owned tool session.
// Messages 校验原生语义，并保留已归属的工具会话。
func (r *Runtime) Messages(parent context.Context, req runtime.MessagesRequest) (runtime.Stream[runtime.MessagesEvent], error) {
	if req.ValidateProtocol() != nil {
		return nil, r.rejectMessages(runtime.ErrorCapability, "invalid_protocol_selectors", nil)
	}
	if req.TenantID == "" || req.KeyID == "" || len(req.TenantID) > 256 || len(req.KeyID) > 256 {
		return nil, r.rejectMessages(runtime.ErrorInvalidConfig, "invalid_principal", nil)
	}
	if len(req.JSON) > tunnelwire.MaxMessagesJSONBytes {
		return nil, r.rejectMessages(runtime.ErrorInvalidConfig, "request_too_large", nil)
	}
	var parsed messagesRequest
	if decodeMessagesRequest(req.JSON, req.Native, &parsed) != nil {
		return nil, r.rejectMessages(runtime.ErrorCapability, "unsupported_request_shape", nil)
	}
	parsed.Model = req.Model
	parsed.apiVersion, parsed.betaFeatures = req.APIVersion, req.BetaFeatures
	r.modelsMu.RLock()
	_, known := r.models[req.Model]
	r.modelsMu.RUnlock()
	if !known {
		return nil, r.rejectMessages(runtime.ErrorCapability, "unknown_model", nil)
	}
	if parsed.validate(req.Model) != nil {
		return nil, r.rejectMessages(runtime.ErrorCapability, "unsupported_request_config", nil)
	}
	parsed.cliModel = req.Model
	s, err := r.bridge.acquire(principal{req.TenantID, req.KeyID}, parsed)
	if err != nil {
		code := runtime.ErrorInvalidConfig
		reason := "session_rejected"
		switch {
		case errors.Is(err, errCapacity):
			code = runtime.ErrorBackpressure
			reason = "capacity"
		case errors.Is(err, errClosed):
			// Closing is not a caller mistake, and a new session may try another node.
			// 正在关闭不是调用方的错误，新会话可以换节点。
			code = runtime.ErrorClosed
			reason = "closed"
		}
		return nil, r.rejectMessages(code, reason, err)
	}
	ctx, cancel := context.WithCancel(parent)
	stopRoot := context.AfterFunc(r.root, cancel)
	r.bridge.mu.Lock()
	if r.bridge.closed {
		r.bridge.mu.Unlock()
		stopRoot()
		cancel()
		s.cancel()
		return nil, claudeError(runtime.ErrorClosed)
	}
	r.bridge.wg.Add(1)
	r.bridge.mu.Unlock()
	out := &nativeStream{ctx: ctx, cancel: cancel, s: s, items: make(chan nativeItem), done: make(chan struct{})}
	go func() {
		defer r.bridge.wg.Done()
		defer stopRoot()
		defer close(out.done)
		defer close(out.items)
		diagnostics := newMessagesRoundDiagnostics(parsed.isNative(), len(parsed.Safeguards) > 0)
		roundID := randomID()
		r.logger.Info("Claude Messages round started", "runtime_id", r.cfg.ID, "round_id", roundID, "message_path", diagnostics.path, "request_bytes", len(req.JSON))
		writer := &nativeWriter{stream: out, header: make(http.Header), status: 200, diagnostics: diagnostics}
		err := r.bridge.respond(ctx, writer, s, true)
		outcome := "completed"
		if err != nil {
			outcome = "failed"
			if ctx.Err() != nil {
				outcome = "canceled"
			}
		}
		r.logger.Info("Claude Messages round finished", "runtime_id", r.cfg.ID, "round_id", roundID,
			"message_path", diagnostics.path, "outcome", outcome, "safeguard_status", diagnostics.status)
		s.mu.Lock()
		s.busy = false
		keep := err == nil && s.waiting && !s.request.isNative()
		s.mu.Unlock()
		if !keep {
			s.cancel()
		}
		if err != nil {
			failure := withUpstream(r.roundError(err), err, s.upstream)
			if ctx.Err() == nil {
				select {
				case out.items <- nativeItem{err: failure}:
				case <-ctx.Done():
				}
			}
		}
	}()
	return out, nil
}

// roundError reports a failed round. A quota rejection becomes a retryable
// rate_limited carrying how long until the limit resets, so the Gateway can
// start the session on another account and leave this one alone until then;
// local CLI setup, isolation, stream and result failures are backend failures;
// an expired session timer is a timeout. Other failures retain their protocol
// classification with details withheld.
//
// roundError 报告一个失败的回合。额度拒绝成为可重试的 rate_limited，并携带距限额重置
// 还有多久，使 Gateway 可以在另一个账户上开启会话、并在此之前不再打扰这个账户；
// 本机 CLI 的设置、隔离、流与结果失败按后端失败报告，会话计时器到期按超时报告，其他失败仍保留
// 隐去细节的协议错误分类。
func (r *Runtime) roundError(err error) error {
	if message := runtime.MessagesRejectionMessage(err); message != "" {
		var failure *runtime.RuntimeError
		_ = errors.As(err, &failure)
		r.logger.Warn("Claude Messages round failed", "runtime_id", r.cfg.ID, "reason", "capability_rejected")
		return &runtime.RuntimeError{Code: runtime.ErrorCapability, RuntimeID: r.cfg.ID, Kind: runtime.KindClaude, Operation: "messages", StatusCode: failure.StatusCode, Message: message}
	}
	if failure := r.attachmentError(err); failure != nil {
		return failure
	}
	// Log only fixed categories and parser positions, never CLI output or errors supplied by it.
	// 仅记录固定分类与解析位置，不记录 CLI 输出或其中携带的任意错误文本。
	reason := "unknown"
	switch {
	case errors.Is(err, errCLIProtocol):
		reason = "cli_protocol"
	case errors.Is(err, errCLIResult):
		reason = "cli_result"
	case errors.Is(err, errCLIExit):
		reason = "cli_exit"
	case errors.Is(err, errMissingResult):
		reason = "missing_result"
	case errors.Is(err, errUnsafeTools):
		reason = "unsafe_tools"
	case errors.Is(err, errQuotaExhausted):
		reason = "quota_exhausted"
	case errors.Is(err, errCLIStart):
		reason = "cli_start"
	case errors.Is(err, errProbeSetup):
		reason = "setup"
	case errors.Is(err, errCapacity):
		reason = "capacity"
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "deadline_exceeded"
	}
	attrs := []any{"runtime_id", r.cfg.ID, "reason", reason}
	var eventFailure *cliEventError
	if errors.As(err, &eventFailure) {
		attrs = append(attrs, "cli_event_line", eventFailure.line, "cli_event_error", eventFailure.reason)
	}
	r.logger.Warn("Claude Messages round failed", attrs...)
	if errors.Is(err, context.DeadlineExceeded) {
		return &runtime.RuntimeError{Code: runtime.ErrorTimeout, RuntimeID: r.cfg.ID, Kind: runtime.KindClaude, Operation: "messages",
			Message: "Claude Messages operation failed: " + string(runtime.ErrorTimeout)}
	}
	if errors.Is(err, errMissingResult) || errors.Is(err, errCLIExit) || errors.Is(err, errCLIProtocol) ||
		errors.Is(err, errCLIResult) || errors.Is(err, errUnsafeTools) || errors.Is(err, errCLIStart) || errors.Is(err, errProbeSetup) {
		return &runtime.RuntimeError{Code: runtime.ErrorUpstream, RuntimeID: r.cfg.ID, Kind: runtime.KindClaude, Operation: "messages",
			Message: "Claude Messages operation failed: " + string(runtime.ErrorUpstream)}
	}
	var quota *quotaError
	if !errors.As(err, &quota) {
		return claudeError(runtime.ErrorProtocol)
	}
	rtErr := &runtime.RuntimeError{Code: runtime.ErrorRateLimited, RuntimeID: r.cfg.ID, Kind: runtime.KindClaude, Operation: "messages",
		Message: "Claude Messages operation failed: " + string(runtime.ErrorRateLimited), Retryable: true}
	if !quota.resetsAt.IsZero() {
		rtErr.RetryAfter = max(quota.resetsAt.Sub(r.bridge.clock.Now()), 0)
	}
	return rtErr
}

// Recv returns one native event with no prefetch queue.
// Recv 返回一条原生事件，不设置预读队列。
func (s *nativeStream) Recv() (runtime.MessagesEvent, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return runtime.MessagesEvent{}, runtime.ErrStreamClosed
	}
	select {
	case item, ok := <-s.items:
		if !ok {
			return runtime.MessagesEvent{}, io.EOF
		}
		if item.err == nil {
			var shape struct{ Type string }
			_ = json.Unmarshal(item.event.JSON, &shape)
			s.mu.Lock()
			s.committed = true
			if shape.Type == "message_stop" {
				s.complete = true
			}
			s.mu.Unlock()
		}
		return item.event, item.err
	case <-s.ctx.Done():
		return runtime.MessagesEvent{}, s.ctx.Err()
	}
}

// Committed reports whether the consumer has received any model event.
// Committed 报告消费方是否已收到模型事件。
func (s *nativeStream) Committed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.committed }

// Close cancels incomplete rounds and retains only completed tool-waiting sessions.
// Close 取消未完成回合，仅保留已完成回合中等待工具的会话。
func (s *nativeStream) Close() error {
	s.mu.Lock()
	s.closed = true
	complete := s.complete
	s.mu.Unlock()
	s.cancel()
	if !complete {
		s.s.cancel()
	}
	<-s.done
	return nil
}

type nativeWriter struct {
	stream      *nativeStream
	diagnostics *messagesRoundDiagnostics
	header      http.Header
	status      int
	buffer      []byte
	// forwarded is set once the first event, which carries the upstream headers, is out.
	// forwarded 在携带上游响应头的首个事件发出后置位。
	forwarded bool
}

// Header returns the private HTTP adapter's header map.
// Header 返回内部 HTTP 适配器的请求头表。
func (w *nativeWriter) Header() http.Header { return w.header }

// WriteHeader records status without opening any HTTP connection.
// WriteHeader 记录状态，不建立任何 HTTP 连接。
func (w *nativeWriter) WriteHeader(status int) { w.status = status }

// Flush satisfies the in-memory SSE writer contract without buffering events.
// Flush 满足内存 SSE 写入契约，不缓冲事件。
func (w *nativeWriter) Flush() {}

// Write strips HTTP framing and applies synchronous event backpressure.
// Write 去掉 HTTP 帧，并同步施加事件背压。
func (w *nativeWriter) Write(data []byte) (int, error) {
	if w.status >= 400 || len(w.buffer)+len(data) > maxEventBytes+1024 {
		return 0, errCLIProtocol
	}
	w.buffer = append(w.buffer, data...)
	for {
		end := bytes.Index(w.buffer, []byte("\n\n"))
		if end < 0 {
			break
		}
		frame := w.buffer[:end]
		if bytes.HasPrefix(frame, []byte("event: error\n")) {
			// The bridge's generic failure frame: the classified failure,
			// with any upstream answer, follows as the stream's error.
			// 桥接层的通用失败帧：带有上游应答的分类失败随后作为流错误送达。
			w.buffer = w.buffer[end+2:]
			continue
		}
		start := bytes.Index(frame, []byte("\ndata: "))
		if start < 0 {
			return 0, errCLIProtocol
		}
		event := append(json.RawMessage(nil), frame[start+7:]...)
		item := nativeItem{event: runtime.MessagesEvent{JSON: event}}
		if !w.forwarded {
			w.forwarded = true
			item.event.Header = w.stream.s.upstream.successHeader()
		}
		select {
		case w.stream.items <- item:
		case <-w.stream.ctx.Done():
			return 0, w.stream.ctx.Err()
		}
		if w.diagnostics != nil {
			w.diagnostics.observe(event)
		}
		w.buffer = w.buffer[end+2:]
	}
	return len(data), nil
}

// Identity identifies an experimental API key without exposing its value to a session.
// Identity 标识实验 API Key，不将其值暴露给会话。
type Identity struct{ TenantID, KeyID string }

// NewExperimentalHandler creates a loopback experiment handler and its cleanup function.
// NewExperimentalHandler 创建回环实验处理器及其清理函数。
func NewExperimentalHandler(executable, model string, keys map[string]Identity, clock runtime.Clock) (http.Handler, func()) {
	owners := make(map[string]principal, len(keys))
	for key, id := range keys {
		owners[key] = principal{id.TenantID, id.KeyID}
	}
	b := newMessagesBridge(executable, model, owners, clock)
	return b, b.close
}

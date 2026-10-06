// Package claudecode provides locally authenticated Claude Code Messages inference.
// Package claudecode 提供使用本机登录状态的 Claude Code Messages 推理。
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"

	"AIServeWeave/common/runtime"
)

// Runtime owns bounded CLI sessions; callers execute all requested tools.
// Runtime 管理有界的 CLI 会话；所有请求的工具均由调用方执行。
type Runtime struct {
	cfg    runtime.Config
	bridge *messagesBridge
	root   context.Context
	cancel context.CancelFunc
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
	return func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		cfg = cfg.Normalize()
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		if cfg.Kind != runtime.KindClaude || executable == "" || deps.Clock == nil {
			return nil, claudeError(runtime.ErrorInvalidConfig)
		}
		if cfg.MaxConcurrent < 1 {
			cfg.MaxConcurrent = 2
		} else {
			cfg.MaxConcurrent = min(cfg.MaxConcurrent, 2)
		}
		root, cancel := context.WithCancel(context.Background())
		bridge := newMessagesBridge(executable, "sonnet", nil, deps.Clock)
		bridge.capacity = cfg.MaxConcurrent
		return &Runtime{cfg: cfg, bridge: bridge, root: root, cancel: cancel}, nil
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

func (r *Runtime) inspect(parent context.Context) error {
	r.bridge.mu.Lock()
	if r.bridge.closed {
		r.bridge.mu.Unlock()
		return claudeError(runtime.ErrorClosed)
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
	if checkAuth(ctx, r.bridge.program, probeEnv(os.Environ())) != nil {
		return claudeError(runtime.ErrorUnauthorized)
	}
	return nil
}

func claudeCapabilities() runtime.CapabilitySet {
	return runtime.CapabilitySet{runtime.CapabilityMessages: {Capability: runtime.CapabilityMessages, Level: runtime.SupportSupported, Source: runtime.SourceRuntimeProfile, Detail: "local Claude Code Messages sessions"}}
}

// Probe verifies native subscription login without spending model quota.
// Probe 验证原生订阅登录，不消耗模型额度。
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	err := r.inspect(ctx)
	return runtime.ProbeResult{Kind: runtime.KindClaude, IdentityVerified: err == nil, ProbedAt: r.bridge.clock.Now()}, err
}

// Health verifies that the local subscription login remains available.
// Health 验证本机订阅登录仍可用。
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	err := r.inspect(ctx)
	state := runtime.StateHealthy
	if err != nil {
		state = runtime.StateUnhealthy
	}
	return runtime.HealthReport{State: state, CheckedAt: r.bridge.clock.Now()}, err
}

// Discover publishes the locally allowed sonnet alias and native Messages capability.
// Discover 发布本地允许的 sonnet 别名与原生 Messages 能力。
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	if err := r.inspect(ctx); err != nil {
		return runtime.Discovery{}, err
	}
	caps := claudeCapabilities()
	return runtime.Discovery{Capabilities: caps, Models: []runtime.Model{{ID: "sonnet", Capabilities: caps}}, DiscoveredAt: r.bridge.clock.Now()}, nil
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
	if req.TenantID == "" || req.KeyID == "" || len(req.TenantID) > 256 || len(req.KeyID) > 256 || len(req.JSON) > 1<<20 {
		return nil, claudeError(runtime.ErrorInvalidConfig)
	}
	var parsed messagesRequest
	if decodeStrict(req.JSON, &parsed) != nil {
		return nil, claudeError(runtime.ErrorCapability)
	}
	parsed.Model = req.Model
	if parsed.validate("sonnet") != nil {
		return nil, claudeError(runtime.ErrorCapability)
	}
	s, err := r.bridge.acquire(principal{req.TenantID, req.KeyID}, parsed)
	if err != nil {
		code := runtime.ErrorInvalidConfig
		if errors.Is(err, errCapacity) {
			code = runtime.ErrorBackpressure
		}
		return nil, claudeError(code)
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
		writer := &nativeWriter{stream: out, header: make(http.Header), status: 200}
		err := r.bridge.respond(ctx, writer, s, true)
		s.mu.Lock()
		s.busy = false
		keep := err == nil && s.waiting
		s.mu.Unlock()
		if !keep {
			s.cancel()
		}
		if err != nil && ctx.Err() == nil {
			select {
			case out.items <- nativeItem{err: claudeError(runtime.ErrorProtocol)}:
			case <-ctx.Done():
			}
		}
	}()
	return out, nil
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
	stream *nativeStream
	header http.Header
	status int
	buffer []byte
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
		start := bytes.Index(frame, []byte("\ndata: "))
		if start < 0 {
			return 0, errCLIProtocol
		}
		event := append(json.RawMessage(nil), frame[start+7:]...)
		select {
		case w.stream.items <- nativeItem{event: runtime.MessagesEvent{JSON: event}}:
		case <-w.stream.ctx.Done():
			return 0, w.stream.ctx.Err()
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

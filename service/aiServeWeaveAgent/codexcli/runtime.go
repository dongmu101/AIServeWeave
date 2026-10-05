package codexcli

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

var errUnsupported = errors.New("codexcli: request feature is unsupported")
var errFinished = errors.New("codexcli: operation finished")

// Runtime serves remote inference using an isolated local Codex process per call.
// Runtime 为每次远程推理创建独立本地 Codex 进程。
type Runtime struct {
	cfg        runtime.Config
	deps       runtime.Dependencies
	executable string
	env        []string
	root       context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	closed     bool
	wg         sync.WaitGroup
	limiter    *runtime.Limiter
}

var _ runtime.InferenceRuntime = (*Runtime)(nil)

// New constructs the Agent-local Codex factory without starting a process.
// New 创建 Agent 本地 Codex 适配器，不启动进程。
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	return NewFactory("codex")(cfg, deps)
}

// NewFactory binds a trusted executable locally; its path never comes from a request.
// NewFactory 绑定可信的本地可执行文件，路径从不取自推理请求。
func NewFactory(executable string) runtime.Factory {
	return func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		if cfg.Kind == "" {
			cfg.Kind = runtime.KindCodex
		}
		cfg = cfg.Normalize()
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		if cfg.Kind != runtime.KindCodex || deps.Clock == nil || executable == "" {
			return nil, &runtime.RuntimeError{Code: runtime.ErrorInvalidConfig, Kind: cfg.Kind, RuntimeID: cfg.ID, Message: "invalid local Codex configuration"}
		}
		root, cancel := context.WithCancel(context.Background())
		return &Runtime{cfg: cfg, deps: deps, executable: executable, env: probeEnv(os.Environ()), root: root, cancel: cancel,
			limiter: runtime.NewLimiter(cfg.MaxConcurrent)}, nil
	}
}

// Descriptor returns non-secret runtime identity and capacity.
// Descriptor 返回不含凭据的运行时标识与容量。
func (r *Runtime) Descriptor() runtime.Descriptor {
	return runtime.Descriptor{ID: r.cfg.ID, Kind: runtime.KindCodex, MaxConcurrent: r.cfg.MaxConcurrent}
}

// Close cancels and reaps all owned processes before returning.
// Close 取消并回收所有所属进程后才返回。
func (r *Runtime) Close() error {
	r.mu.Lock()
	r.closed = true
	r.limiter.Close()
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
	return nil
}

// operation combines the caller, runtime lifecycle and injected-clock deadlines.
// operation 合并调用方、运行时生命周期及注入时钟控制的期限。
func (r *Runtime) operation(parent context.Context, timeout, idle time.Duration) (context.Context, func(), func(), error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, nil, r.wrap("start", runtime.ErrRuntimeClosed)
	}
	r.wg.Add(1)
	r.mu.Unlock()
	ctx, cancel := context.WithCancelCause(parent)
	stopRoot := context.AfterFunc(r.root, func() { cancel(runtime.ErrRuntimeClosed) })
	activity := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var totalCh, idleCh <-chan time.Time
		totalStop, idleStop := func() bool { return false }, func() bool { return false }
		if timeout > 0 {
			totalCh, totalStop = r.deps.Clock.NewTimer(timeout)
		}
		if idle > 0 {
			idleCh, idleStop = r.deps.Clock.NewTimer(idle)
		}
		defer totalStop()
		defer func() { idleStop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-totalCh:
				cancel(context.DeadlineExceeded)
				return
			case <-idleCh:
				cancel(context.DeadlineExceeded)
				return
			case <-activity:
				if idle > 0 {
					idleStop()
					idleCh, idleStop = r.deps.Clock.NewTimer(idle)
				}
			}
		}
	}()
	touch := func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}
	finish := func() { cancel(errFinished); stopRoot(); <-done; r.wg.Done() }
	return ctx, finish, touch, nil
}

func (r *Runtime) wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	code := runtime.ErrorProtocol
	switch {
	case errors.Is(err, runtime.ErrRuntimeClosed):
		code = runtime.ErrorClosed
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code = runtime.ErrorTimeout
	case errors.Is(err, ErrAuthentication):
		code = runtime.ErrorUnauthorized
	case errors.Is(err, ErrProcess):
		code = runtime.ErrorConnection
	case errors.Is(err, ErrConfig):
		code = runtime.ErrorInvalidConfig
	case errors.Is(err, errUnsupported):
		code = runtime.ErrorCapability
	case errors.Is(err, ErrRPC), errors.Is(err, ErrTurn):
		code = runtime.ErrorUpstream
	}
	return &runtime.RuntimeError{Code: code, RuntimeID: r.cfg.ID, Kind: runtime.KindCodex, Operation: operation, Message: "Codex operation failed: " + string(code), Cause: err}
}

func (r *Runtime) inspect(ctx context.Context) ([]runtime.Model, error) {
	ctx, finish, touch, err := r.operation(ctx, r.cfg.ProbeTimeout, 0)
	if err != nil {
		return nil, err
	}
	defer finish()
	var models []runtime.Model
	err = withServer(ctx, r.executable, r.env, func(p *protocol, _ string) error {
		p.activity = touch
		if err := initializeServer(p); err != nil {
			return err
		}
		var cursor *string
		for page := 0; page < 16; page++ {
			var list struct {
				Data []struct {
					Model string `json:"model"`
				} `json:"data"`
				NextCursor *string `json:"nextCursor"`
			}
			if err := p.request(3+page, "model/list", map[string]any{"limit": 100, "cursor": cursor}, &list); err != nil {
				return err
			}
			for _, model := range list.Data {
				if model.Model == "" || len(model.Model) > 256 || len(models) >= 256 {
					return ErrProtocol
				}
				models = append(models, runtime.Model{ID: model.Model, Capabilities: capabilities()})
			}
			if list.NextCursor == nil {
				if len(models) == 0 {
					return ErrProtocol
				}
				return nil
			}
			cursor = list.NextCursor
		}
		return ErrProtocol
	})
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	return models, r.wrap("discover", err)
}

func capabilities() runtime.CapabilitySet {
	result := runtime.CapabilitySet{}
	for _, c := range []runtime.Capability{runtime.CapabilityChat, runtime.CapabilityChatStream, runtime.CapabilityResponses, runtime.CapabilityTools, runtime.CapabilityStructuredOutput} {
		result[c] = runtime.CapabilityEvidence{Capability: c, Level: runtime.SupportSupported, Source: runtime.SourceRuntimeProfile, Detail: "isolated Codex app-server adapter"}
	}
	return result
}

// Probe verifies local ChatGPT authentication and the app-server model catalog.
// Probe 验证本地 ChatGPT 登录与 app-server 模型目录。
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	_, err := r.inspect(ctx)
	return runtime.ProbeResult{Kind: runtime.KindCodex, IdentityVerified: err == nil, Evidence: "local ChatGPT app-server authentication and model catalog", ProbedAt: r.deps.Clock.Now()}, err
}

// Health checks authentication without issuing a model inference request.
// Health 检查登录状态，不发起模型推理请求。
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	start := r.deps.Clock.Now()
	_, err := r.inspect(ctx)
	report := runtime.HealthReport{State: runtime.StateHealthy, CheckedAt: r.deps.Clock.Now(), Latency: r.deps.Clock.Now().Sub(start)}
	if err != nil {
		report.State = runtime.StateUnhealthy
		report.ErrorSummary = "local Codex unavailable"
	}
	return report, err
}

// Discover publishes the locally authenticated model catalog.
// Discover 发布本机已登录账户的模型目录。
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	models, err := r.inspect(ctx)
	return runtime.Discovery{Models: models, Capabilities: capabilities(), DiscoveredAt: r.deps.Clock.Now()}, err
}

// ListModels reads models using the local app-server, without inference.
// ListModels 通过本地 app-server 读取模型，不执行推理。
func (r *Runtime) ListModels(ctx context.Context) ([]runtime.Model, error) { return r.inspect(ctx) }

type streamResult struct {
	event runtime.ChatEvent
	err   error
}
type chatStream struct {
	items     chan streamResult
	ctx       context.Context
	cancel    context.CancelCauseFunc
	done      chan struct{}
	runtime   *Runtime
	mu        sync.Mutex
	committed bool
	closed    bool
}

func (s *chatStream) Recv() (runtime.ChatEvent, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return runtime.ChatEvent{}, runtime.ErrStreamClosed
	}
	select {
	case item, ok := <-s.items:
		if !ok {
			cause := context.Cause(s.ctx)
			if cause != nil && !errors.Is(cause, errFinished) {
				return runtime.ChatEvent{}, s.runtime.wrap("chat_stream", cause)
			}
			return runtime.ChatEvent{}, io.EOF
		}
		if item.err == nil {
			s.mu.Lock()
			s.committed = true
			s.mu.Unlock()
		}
		return item.event, item.err
	case <-s.ctx.Done():
		if errors.Is(context.Cause(s.ctx), errFinished) {
			return runtime.ChatEvent{}, io.EOF
		}
		if errors.Is(context.Cause(s.ctx), runtime.ErrStreamClosed) {
			return runtime.ChatEvent{}, runtime.ErrStreamClosed
		}
		return runtime.ChatEvent{}, s.runtime.wrap("chat_stream", context.Cause(s.ctx))
	}
}
func (s *chatStream) Committed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.committed }
func (s *chatStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel(runtime.ErrStreamClosed)
	<-s.done
	return nil
}
func (s *chatStream) send(event runtime.ChatEvent) error {
	select {
	case s.items <- streamResult{event: event}:
		return nil
	case <-s.ctx.Done():
		return context.Cause(s.ctx)
	}
}

// ChatStream streams text or one caller-executed tool call with bounded backpressure.
// ChatStream 以有界背压发送文本或一次由调用方执行的工具调用。
func (r *Runtime) ChatStream(parent context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	input, err := prepareChat(req)
	if err != nil {
		return nil, r.wrap("chat_stream", err)
	}
	release, err := r.limiter.Acquire()
	if err != nil {
		return nil, err
	}
	parent, cancel := context.WithCancelCause(parent)
	ctx, finish, touch, err := r.operation(parent, r.cfg.RequestTimeout, r.cfg.StreamIdleTimeout)
	if err != nil {
		release()
		cancel(err)
		return nil, err
	}
	s := &chatStream{items: make(chan streamResult), ctx: ctx, cancel: cancel, done: make(chan struct{}), runtime: r}
	go func() {
		defer close(s.done)
		defer release()
		defer finish()
		defer close(s.items)
		err := withServer(ctx, r.executable, r.env, func(p *protocol, dir string) error {
			p.activity = touch
			return runChat(p, dir, req, input, accountInstructions(r.env), s.send)
		})
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
		if err != nil {
			select {
			case s.items <- streamResult{err: r.wrap("chat_stream", err)}:
			case <-ctx.Done():
			}
		}
	}()
	return s, nil
}

// Chat collects a bounded non-streaming response using the same protocol driver.
// Chat 使用相同协议驱动收集有大小限制的非流式响应。
func (r *Runtime) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	s, err := r.ChatStream(ctx, req)
	if err != nil {
		return runtime.ChatResponse{}, err
	}
	defer s.Close()
	response := runtime.ChatResponse{Message: runtime.ChatMessage{Role: "assistant"}, CreatedAt: r.deps.Clock.Now()}
	size := 0
	for {
		event, err := s.Recv()
		if err == io.EOF {
			return response, nil
		}
		if err != nil {
			return runtime.ChatResponse{}, err
		}
		response.ID, response.Model = event.ID, event.Model
		size += len(event.Delta.Content)
		if size > maxFrameBytes {
			return runtime.ChatResponse{}, r.wrap("chat", ErrProtocol)
		}
		response.Message.Content += event.Delta.Content
		for _, call := range event.Delta.ToolCalls {
			response.Message.ToolCalls = append(response.Message.ToolCalls, runtime.ToolCall{ID: call.ID, Type: call.Type, Function: runtime.FunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments}})
		}
		if event.FinishReason != "" {
			response.FinishReason = event.FinishReason
		}
		if event.Usage != nil {
			response.Usage = *event.Usage
		}
	}
}

// Embed rejects embeddings because Codex is a conversational CLI backend.
// Embed 拒绝嵌入请求，Codex 是对话型 CLI 后端。
func (r *Runtime) Embed(context.Context, runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	return runtime.EmbeddingResponse{}, r.wrap("embed", errUnsupported)
}

// Transcribe rejects audio transcription, which app-server does not expose here.
// Transcribe 拒绝此 app-server 适配器未提供的音频转录。
func (r *Runtime) Transcribe(context.Context, runtime.AudioTranscriptionRequest, io.Reader) (runtime.AudioTranscriptionResponse, error) {
	return runtime.AudioTranscriptionResponse{}, r.wrap("transcribe", errUnsupported)
}

// Rerank rejects reranking, which app-server does not expose here.
// Rerank 拒绝此 app-server 适配器未提供的重排。
func (r *Runtime) Rerank(context.Context, runtime.RerankRequest) (runtime.RerankResponse, error) {
	return runtime.RerankResponse{}, r.wrap("rerank", errUnsupported)
}

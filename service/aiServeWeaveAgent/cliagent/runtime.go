// Package cliagent serves text inference through locally signed-in coding
// agent CLIs (Grok, OpenCode, Cursor, CodeBuddy, Qoder, Pi), running one
// isolated headless process per request. Everything that is the same for
// every CLI — process lifecycle, deadlines, backpressure, bounds, prompt
// rendering and error categories — lives here once; each CLI contributes a
// cliSpec with its command line, environment, output dialect and model
// discovery.
//
// Package cliagent 通过本机已登录的编程智能体 CLI（Grok、OpenCode、Cursor、
// CodeBuddy、Qoder、Pi）提供文本推理，每个请求运行一个隔离的 headless 进程。
// 所有 CLI 都相同的部分——进程生命周期、期限、背压、上限、提示词渲染与错误
// 类别——在这里只实现一次；每个 CLI 提供一份 cliSpec，描述它的命令行、环境变量、
// 输出方言与模型发现。
package cliagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

// errFinished marks an operation that ended normally, so its context cause is
// never mistaken for a failure.
// errFinished 标记正常结束的操作，避免把它的 context cause 误认为失败。
var errFinished = errors.New("cliagent: operation finished")

// errConsumerGone means the stream consumer closed the stream mid-turn.
// errConsumerGone 表示流的消费方在回合中途关闭了流。
var errConsumerGone = errors.New("cliagent: stream consumer closed")

// Runtime serves remote inference using an isolated local CLI process per call.
// Runtime 为每次远程推理创建独立的本地 CLI 进程。
type Runtime struct {
	spec       *cliSpec
	cfg        runtime.Config
	deps       runtime.Dependencies
	executable string
	env        []string
	root       context.Context
	cancel     context.CancelFunc
	limiter    *runtime.Limiter

	mu     sync.Mutex
	closed bool
	models map[string]bool // last successfully discovered catalogue / 最近一次成功发现的模型目录
	wg     sync.WaitGroup
}

var _ runtime.InferenceRuntime = (*Runtime)(nil)

// Kinds lists every CLI kind this package serves, for registry wiring.
// Kinds 列出本包服务的全部 CLI 种类，供注册表接线使用。
func Kinds() []runtime.Kind {
	kinds := make([]runtime.Kind, 0, len(specs))
	for kind := range specs {
		kinds = append(kinds, kind)
	}
	return kinds
}

// New constructs the adapter for cfg.Kind without starting a process.
// New 按 cfg.Kind 创建适配器，不启动进程。
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	return NewFactory("")(cfg, deps)
}

// NewFactory binds a trusted executable locally; its path never comes from a
// request. An empty executable uses the cliSpec's own candidate names, looked up
// on the Agent's PATH and then in the cliSpec's install directory under $HOME
// each time a process starts.
//
// NewFactory 绑定可信的本地可执行文件，路径从不取自推理请求。executable 为空
// 时使用 cliSpec 自带的候选名称，每次启动进程时先在 Agent 的 PATH 中查找，再查找
// $HOME 下 cliSpec 声明的安装目录。
func NewFactory(executable string) runtime.Factory {
	return func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		spec, ok := specs[cfg.Kind]
		if !ok || deps.Clock == nil {
			return nil, &runtime.RuntimeError{Code: runtime.ErrorInvalidConfig, Kind: cfg.Kind, RuntimeID: cfg.ID, Message: "invalid local CLI configuration"}
		}
		cfg = cfg.Normalize()
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		root, cancel := context.WithCancel(context.Background())
		return &Runtime{spec: spec, cfg: cfg, deps: deps, executable: executable, env: os.Environ(), root: root, cancel: cancel,
			limiter: runtime.NewLimiter(cfg.MaxConcurrent)}, nil
	}
}

// Descriptor returns non-secret runtime identity and capacity.
// Descriptor 返回不含凭据的运行时标识与容量。
func (r *Runtime) Descriptor() runtime.Descriptor {
	return runtime.Descriptor{ID: r.cfg.ID, Kind: r.spec.Kind, MaxConcurrent: r.cfg.MaxConcurrent}
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

// operation combines the caller, runtime lifecycle and injected-clock
// deadlines. touch resets the idle deadline; finish must be called exactly once.
//
// operation 合并调用方、运行时生命周期及注入时钟控制的期限。touch 重置空闲
// 期限；finish 必须且只能调用一次。
func (r *Runtime) operation(parent context.Context, timeout, idle time.Duration) (ctx context.Context, finish func(), touch func(), err error) {
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
	touch = func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}
	finish = func() { cancel(errFinished); stopRoot(); <-done; r.wg.Done() }
	return ctx, finish, touch, nil
}

// wrap maps an internal error to a RuntimeError whose Message is a fixed
// category; CLI output, prompts and paths stay out of it.
//
// wrap 把内部错误映射为 RuntimeError，Message 只含固定类别，不包含 CLI 输出、
// 提示词或路径。
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
	case errors.Is(err, ErrRequest):
		code = runtime.ErrorInvalidConfig
	case errors.Is(err, errUnsupported):
		code = runtime.ErrorCapability
	case errors.Is(err, ErrTurn), errors.Is(err, ErrRPC):
		code = runtime.ErrorUpstream
	}
	return &runtime.RuntimeError{Code: code, RuntimeID: r.cfg.ID, Kind: r.spec.Kind, Operation: operation, Message: r.spec.Label + " CLI operation failed: " + string(code), Cause: err}
}

// inspect reads the model catalogue without running inference and remembers
// it as the set of models a request may select.
//
// inspect 读取模型目录而不执行推理，并记住它作为请求可选择的模型集合。
func (r *Runtime) inspect(ctx context.Context) ([]runtime.Model, error) {
	ctx, finish, touch, err := r.operation(ctx, r.cfg.ProbeTimeout, 0)
	if err != nil {
		return nil, err
	}
	defer finish()
	var ids []string
	err = withHome(r.spec, r.executable, r.env, func(c *call) error {
		c.ctx, c.touch = ctx, touch
		raw, err := r.spec.Discover(c)
		if err != nil {
			return err
		}
		catalogue := modelSet{aliases: r.spec.Aliases}
		for _, id := range raw {
			if err := catalogue.add(id); err != nil {
				return err
			}
		}
		ids, err = catalogue.result()
		return err
	})
	// A cancellation only explains a failure; it never overrides success.
	// 取消只用来解释失败，从不推翻已经成功的结果。
	if cause := context.Cause(ctx); err != nil && cause != nil && !errors.Is(cause, errFinished) {
		err = cause
	}
	if err != nil {
		return nil, r.wrap("discover", err)
	}
	models := make([]runtime.Model, 0, len(ids))
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		models = append(models, runtime.Model{ID: id, Capabilities: capabilities(r.spec.Label)})
		known[id] = true
	}
	r.mu.Lock()
	r.models = known
	r.mu.Unlock()
	return models, nil
}

func (r *Runtime) knownModel(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.models[id]
}

// capabilities is every CLI model's set: text chat only. Features a headless
// CLI cannot provide safely are marked unsupported explicitly rather than
// left unknown.
//
// capabilities 是每个 CLI 模型的能力集：仅文本聊天。headless CLI 无法安全提供
// 的功能显式标为不支持，而不是留作未知。
func capabilities(label string) runtime.CapabilitySet {
	result := runtime.CapabilitySet{}
	for _, c := range []runtime.Capability{runtime.CapabilityChat, runtime.CapabilityChatStream} {
		result[c] = runtime.CapabilityEvidence{Capability: c, Level: runtime.SupportSupported, Source: runtime.SourceRuntimeProfile, Detail: "isolated " + label + " CLI headless adapter"}
	}
	for _, c := range []runtime.Capability{runtime.CapabilityTools, runtime.CapabilityParallelToolCalls, runtime.CapabilityStructuredOutput,
		runtime.CapabilityVision, runtime.CapabilityEmbeddings, runtime.CapabilityImageGeneration} {
		result[c] = runtime.CapabilityEvidence{Capability: c, Level: runtime.SupportUnsupported, Source: runtime.SourceRuntimeProfile, Detail: label + " CLI headless text adapter"}
	}
	return result
}

// Probe verifies the local CLI can report its model catalogue.
// Probe 验证本地 CLI 能够报告模型目录。
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	_, err := r.inspect(ctx)
	return runtime.ProbeResult{Kind: r.spec.Kind, IdentityVerified: err == nil, Evidence: "local " + r.spec.Label + " CLI model catalogue", ProbedAt: r.deps.Clock.Now()}, err
}

// Health checks the CLI without issuing a model inference request.
// Health 检查 CLI 状态，不发起模型推理请求。
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	start := r.deps.Clock.Now()
	_, err := r.inspect(ctx)
	report := runtime.HealthReport{State: runtime.StateHealthy, CheckedAt: r.deps.Clock.Now(), Latency: r.deps.Clock.Now().Sub(start)}
	if err != nil {
		report.State = runtime.StateUnhealthy
		report.ErrorSummary = "local " + r.spec.Label + " CLI unavailable"
	}
	return report, err
}

// Discover publishes the locally signed-in model catalogue.
// Discover 发布本机已登录账户的模型目录。
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	models, err := r.inspect(ctx)
	return runtime.Discovery{Models: models, Capabilities: capabilities(r.spec.Label), DiscoveredAt: r.deps.Clock.Now()}, err
}

// ListModels reads models from the local CLI, without inference.
// ListModels 从本地 CLI 读取模型，不执行推理。
func (r *Runtime) ListModels(ctx context.Context) ([]runtime.Model, error) { return r.inspect(ctx) }

// chatStream adds process cancellation to ChanStream: Close stops the CLI and
// waits for it to be reaped.
// chatStream 在 ChanStream 之上加入进程取消：Close 会终止 CLI 并等待回收。
type chatStream struct {
	*runtime.ChanStream[runtime.ChatEvent]
	cancel context.CancelCauseFunc
	done   chan struct{}
}

func (s *chatStream) Close() error {
	s.cancel(runtime.ErrStreamClosed)
	_ = s.ChanStream.Close()
	<-s.done
	return nil
}

// ChatStream streams text deltas with synchronous backpressure; the final
// event is sent only after the CLI has finished successfully.
//
// ChatStream 以同步背压流式发送文本增量；最终事件只在 CLI 成功结束后发送。
func (r *Runtime) ChatStream(parent context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	prompt, err := buildPrompt(r.spec.Label, req)
	if err != nil {
		return nil, r.wrap("chat_stream", err)
	}
	if !r.knownModel(req.Model) {
		return nil, r.wrap("chat_stream", ErrRequest)
	}
	if req.MaxTokens != nil && r.deps.Logger != nil {
		r.deps.Logger.Warn(r.spec.Label + " compatibility mode: output token limit is not enforced / 兼容模式：输出 token 上限不生效")
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
	s := &chatStream{ChanStream: runtime.NewChanStream[runtime.ChatEvent](0), cancel: cancel, done: make(chan struct{})}
	id := string(r.spec.Kind) + "-" + randomHex()
	go func() {
		defer close(s.done)
		defer release()
		defer finish()
		first := true
		emit := func(event runtime.ChatEvent) error {
			event.ID, event.Model = id, req.Model
			if first {
				event.Delta.Role = "assistant"
				first = false
			}
			// SendContext, not Send: a consumer that stops reading must not
			// keep the process alive past a deadline or Runtime.Close.
			// 用 SendContext 而不是 Send：停止读取的消费方不能让进程在期限到达
			// 或 Runtime.Close 之后仍然存活。
			if !s.SendContext(ctx, event) {
				if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				return errConsumerGone
			}
			return nil
		}
		err := withHome(r.spec, r.executable, r.env, func(c *call) error {
			c.ctx, c.touch = ctx, touch
			return runTurn(c, req.Model, prompt, emit)
		})
		if cause := context.Cause(ctx); err != nil && cause != nil && !errors.Is(cause, errFinished) {
			err = cause
		}
		if errors.Is(err, errConsumerGone) || errors.Is(err, runtime.ErrStreamClosed) {
			return
		}
		s.CloseWithError(r.wrap("chat_stream", err))
	}()
	return s, nil
}

// Chat collects a bounded non-streaming response using the same process driver.
// Chat 使用相同进程驱动收集有大小限制的非流式响应。
func (r *Runtime) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	s, err := r.ChatStream(ctx, req)
	if err != nil {
		return runtime.ChatResponse{}, err
	}
	defer s.Close()
	response := runtime.ChatResponse{Message: runtime.ChatMessage{Role: "assistant"}, CreatedAt: r.deps.Clock.Now()}
	for {
		event, err := s.Recv()
		if err == io.EOF {
			return response, nil
		}
		if err != nil {
			return runtime.ChatResponse{}, err
		}
		response.ID, response.Model = event.ID, event.Model
		response.Message.Content += event.Delta.Content
		if event.FinishReason != "" {
			response.FinishReason = event.FinishReason
		}
		if event.Usage != nil {
			response.Usage = *event.Usage
		}
	}
}

// Embed rejects embeddings because these CLIs are conversational backends.
// Embed 拒绝嵌入请求，这些 CLI 是对话型后端。
func (r *Runtime) Embed(context.Context, runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	return runtime.EmbeddingResponse{}, r.wrap("embed", errUnsupported)
}

// Transcribe rejects audio transcription, which the CLIs do not expose.
// Transcribe 拒绝 CLI 未提供的音频转录。
func (r *Runtime) Transcribe(context.Context, runtime.AudioTranscriptionRequest, io.Reader) (runtime.AudioTranscriptionResponse, error) {
	return runtime.AudioTranscriptionResponse{}, r.wrap("transcribe", errUnsupported)
}

// Rerank rejects reranking, which the CLIs do not expose.
// Rerank 拒绝 CLI 未提供的重排。
func (r *Runtime) Rerank(context.Context, runtime.RerankRequest) (runtime.RerankResponse, error) {
	return runtime.RerankResponse{}, r.wrap("rerank", errUnsupported)
}

func randomHex() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system entropy unavailable")
	}
	return hex.EncodeToString(b[:])
}

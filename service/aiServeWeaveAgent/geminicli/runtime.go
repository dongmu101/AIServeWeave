// Package geminicli exposes the locally installed Gemini CLI Nano Banana image extension.
// Package geminicli 提供本地已安装的 Gemini CLI Nano Banana 生图扩展。
package geminicli

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

// ImageModelID is the image-only alias; the upstream version is selected locally.
// ImageModelID 是纯图像模型别名；上游版本由本机配置选择。
const ImageModelID = "gemini-image"

var (
	errProcess        = errors.New("geminicli: local extension unavailable")
	errProtocol       = errors.New("geminicli: invalid extension response")
	errAuthentication = errors.New("geminicli: image API authentication failed")
	errUpstream       = errors.New("geminicli: image generation failed")
	errRateLimited    = errors.New("geminicli: image API quota exceeded")
	errFinished       = errors.New("geminicli: operation finished")
)

// Runtime serves images through a fixed tool on the locally installed extension.
// Runtime 通过本地已安装扩展的固定工具提供图像生成。
type Runtime struct {
	cfg     runtime.Config
	clock   runtime.Clock
	node    string
	env     []string
	root    context.Context
	cancel  context.CancelFunc
	limiter *runtime.Limiter
	mu      sync.Mutex
	closed  bool
	wg      sync.WaitGroup
}

var _ runtime.ImageRuntime = (*Runtime)(nil)

// New constructs an image-only runtime using Node from the Agent's PATH.
// New 使用 Agent PATH 中的 Node 创建仅支持生图的运行时。
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	return NewFactory("node")(cfg, deps)
}

// NewFactory binds a trusted local Node executable; requests cannot change it.
// NewFactory 绑定可信的本地 Node 可执行文件；请求不能改变它。
func NewFactory(node string) runtime.Factory {
	return func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		cfg = cfg.Normalize()
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		if cfg.Kind != runtime.KindGeminiCLI || deps.Clock == nil || node == "" {
			return nil, &runtime.RuntimeError{Code: runtime.ErrorInvalidConfig, RuntimeID: cfg.ID, Kind: cfg.Kind, Operation: "new", Message: "Gemini CLI requires its local kind, executable and clock"}
		}
		ctx, cancel := context.WithCancel(context.Background())
		return &Runtime{cfg: cfg, clock: deps.Clock, node: node, env: os.Environ(), root: ctx, cancel: cancel, limiter: runtime.NewLimiter(cfg.MaxConcurrent)}, nil
	}
}

// Descriptor returns non-secret identity and capacity.
// Descriptor 返回不含秘密的标识与容量。
func (r *Runtime) Descriptor() runtime.Descriptor {
	return runtime.Descriptor{ID: r.cfg.ID, Kind: runtime.KindGeminiCLI, MaxConcurrent: r.cfg.MaxConcurrent}
}

// Close cancels and reaps all extension processes before returning.
// Close 取消并回收全部扩展进程后才返回。
func (r *Runtime) Close() error {
	r.mu.Lock()
	r.closed = true
	r.limiter.Close()
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
	return nil
}

// operation joins caller cancellation, runtime shutdown and the injected clock.
// operation 合并调用方取消、运行时关闭与注入时钟的期限。
func (r *Runtime) operation(parent context.Context, timeout time.Duration) (context.Context, func(), error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, runtime.ErrRuntimeClosed
	}
	r.wg.Add(1)
	r.mu.Unlock()
	ctx, cancel := context.WithCancelCause(parent)
	stopRoot := context.AfterFunc(r.root, func() { cancel(runtime.ErrRuntimeClosed) })
	var timer <-chan time.Time
	stop := func() bool { return false }
	if timeout > 0 {
		timer, stop = r.clock.NewTimer(timeout)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
		case <-timer:
			cancel(context.DeadlineExceeded)
		}
	}()
	return ctx, func() { cancel(errFinished); stopRoot(); stop(); <-done; r.wg.Done() }, nil
}

func (r *Runtime) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	code := runtime.ErrorProtocol
	switch {
	case errors.Is(err, runtime.ErrRuntimeClosed):
		code = runtime.ErrorClosed
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = runtime.ErrorTimeout
	case errors.Is(err, errProcess):
		code = runtime.ErrorConnection
	case errors.Is(err, errAuthentication):
		code = runtime.ErrorUnauthorized
	case errors.Is(err, errRateLimited):
		code = runtime.ErrorRateLimited
	case errors.Is(err, errUpstream):
		code = runtime.ErrorUpstream
	case errors.Is(err, runtime.ErrCapabilityUnsupported):
		code = runtime.ErrorCapability
	}
	return &runtime.RuntimeError{Code: code, RuntimeID: r.cfg.ID, Kind: runtime.KindGeminiCLI, Operation: op, Message: "Gemini CLI image operation failed: " + string(code), Cause: err}
}

func capabilities() runtime.CapabilitySet {
	caps := runtime.CapabilitySet{}
	for _, cap := range []runtime.Capability{runtime.CapabilityImageGeneration, runtime.CapabilityChat, runtime.CapabilityChatStream, runtime.CapabilityCompletions, runtime.CapabilityResponses, runtime.CapabilityMessages, runtime.CapabilityTools, runtime.CapabilityParallelToolCalls, runtime.CapabilityStructuredOutput, runtime.CapabilityVision, runtime.CapabilityReasoning, runtime.CapabilityEmbeddings, runtime.CapabilityAudioInput, runtime.CapabilityAudioTranscription, runtime.CapabilityDocumentInput, runtime.CapabilityRerank, runtime.CapabilityWorkflowExecution, runtime.CapabilityWorkflowEvents, runtime.CapabilityWorkflowCancel, runtime.CapabilityArtifactRead, runtime.CapabilityInputWrite} {
		level := runtime.SupportUnsupported
		if cap == runtime.CapabilityImageGeneration {
			level = runtime.SupportSupported
		}
		caps[cap] = runtime.CapabilityEvidence{Capability: cap, Level: level, Source: runtime.SourceRuntimeProfile, Detail: "local Nano Banana generate_image tool; image-only adapter"}
	}
	return caps
}

func (r *Runtime) inspect(parent context.Context) error {
	ctx, finish, err := r.operation(parent, r.cfg.ProbeTimeout)
	if err != nil {
		return r.wrap("discover", err)
	}
	defer finish()
	err = r.withServer(ctx, func(p *protocol, _ string) error { return p.initialize() })
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	return r.wrap("discover", err)
}

// Probe checks local credentials and the MCP tool catalogue without generating an image.
// Probe 检查本地凭据和 MCP 工具目录，不执行图像生成。
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	err := r.inspect(ctx)
	return runtime.ProbeResult{Kind: runtime.KindGeminiCLI, IdentityVerified: err == nil, Evidence: "local Nano Banana MCP tool catalogue; API key validity not checked", ProbedAt: r.clock.Now()}, err
}

// Health checks the local extension without consuming image API quota.
// Health 检查本地扩展，不消耗图片 API 额度。
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	start := r.clock.Now()
	err := r.inspect(ctx)
	h := runtime.HealthReport{State: runtime.StateHealthy, CheckedAt: r.clock.Now(), Latency: r.clock.Now().Sub(start)}
	if err != nil {
		h.State = runtime.StateUnhealthy
		h.ErrorSummary = "local Gemini CLI image extension unavailable"
	}
	return h, err
}

// Discover publishes the single image alias after checking the local extension.
// Discover 检查本地扩展后发布唯一的图像别名。
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	if err := r.inspect(ctx); err != nil {
		return runtime.Discovery{}, err
	}
	return runtime.Discovery{Models: []runtime.Model{{ID: ImageModelID, Capabilities: capabilities()}}, Capabilities: capabilities(), DiscoveredAt: r.clock.Now()}, nil
}

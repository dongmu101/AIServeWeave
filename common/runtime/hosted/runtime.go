// Package hosted adapts hosted OpenAI-compatible API providers (OpenAI,
// Gemini, DeepSeek, Kimi, Qwen, Zhipu, MiniMax and any OpenAI-compatible
// endpoint) to the runtime.InferenceRuntime contract.
//
// By default the adapter touches two endpoints: GET {root}/models to prove
// the key works and list models, and POST {root}/chat/completions to serve
// requests. Embeddings, transcription and rerank reach their own endpoints
// only after an operator declares those capabilities. {root} is the
// provider's documented SDK base URL, which already carries the API version
// segment. Redirects are refused, so the key never leaves the declared host.
//
// Package hosted 把托管的 OpenAI 兼容 API 供应商（OpenAI、Gemini、DeepSeek、
// Kimi、Qwen、智谱、MiniMax 以及任意 OpenAI 兼容端点）适配为
// runtime.InferenceRuntime 契约。
//
// 适配器默认只访问两个端点：GET {root}/models 用于证明密钥可用并列出模型，
// POST {root}/chat/completions 用于服务请求。嵌入、转录与重排只有在运维声明了
// 相应能力后才会访问各自的端点。{root} 是供应商文档给出的 SDK base URL，已包含
// API 版本段。重定向一律拒绝，密钥不会离开已声明的主机。
package hosted

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/oaibase"
	"AIServeWeave/common/runtime/openai"
)

// provider holds what differs between hosted OpenAI-compatible providers.
// provider 记录各托管 OpenAI 兼容供应商之间的差异。
type provider struct {
	label string
	// maxCompletionTokens sends the output limit as max_completion_tokens.
	// maxCompletionTokens 以 max_completion_tokens 发送输出上限。
	maxCompletionTokens bool
	// nonChat reports catalogue entries that are not chat models.
	// nonChat 判断目录中不是聊天模型的条目。
	nonChat func(id string) bool
}

var providers = map[runtime.Kind]provider{
	runtime.KindOpenAI:           {label: "OpenAI", maxCompletionTokens: true, nonChat: openAINonChat},
	runtime.KindGemini:           {label: "Gemini", nonChat: geminiNonChat},
	runtime.KindDeepSeek:         {label: "DeepSeek"},
	runtime.KindKimi:             {label: "Kimi"},
	runtime.KindQwen:             {label: "Qwen", nonChat: qwenNonChat},
	runtime.KindZhipu:            {label: "Zhipu"},
	runtime.KindMiniMax:          {label: "MiniMax", maxCompletionTokens: true},
	runtime.KindOpenAICompatible: {label: "OpenAI-compatible"},
}

// Kinds lists every kind this package serves, for registry wiring.
// Kinds 列出本包服务的全部种类，供注册表接线使用。
func Kinds() []runtime.Kind {
	kinds := make([]runtime.Kind, 0, len(providers))
	for kind := range providers {
		kinds = append(kinds, kind)
	}
	return kinds
}

// Runtime is the hosted-provider adapter. It is safe for concurrent use.
// Runtime 是托管供应商适配器，可安全并发使用。
type Runtime struct {
	base     *oaibase.Base
	provider provider
}

var _ runtime.InferenceRuntime = (*Runtime)(nil)

// New builds a hosted Runtime for cfg.Kind. It performs no network I/O.
// New 按 cfg.Kind 构建托管运行时，不进行任何网络 I/O。
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	p, ok := providers[cfg.Kind]
	if !ok {
		return nil, &runtime.RuntimeError{Code: runtime.ErrorInvalidConfig, RuntimeID: cfg.ID, Kind: cfg.Kind, Operation: "create_runtime",
			Message: fmt.Sprintf("kind %q is not a hosted OpenAI-compatible provider", cfg.Kind)}
	}
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	base, err := oaibase.NewWithOptions(cfg, cfg.Kind, deps, oaibase.Options{VersionedBaseURL: true, MaxCompletionTokens: p.maxCompletionTokens, RefuseRedirects: true})
	if err != nil {
		return nil, err
	}
	return &Runtime{base: base, provider: p}, nil
}

// Descriptor reports the instance's stable identity and scheduling summary.
// Descriptor 报告实例的稳定标识与调度摘要。
func (r *Runtime) Descriptor() runtime.Descriptor { return r.base.Descriptor() }

// listModels reads GET {root}/models. With a declared catalogue, an endpoint
// that has no listing route (404/405) is accepted and the declared IDs are
// returned; an authentication failure is never masked.
//
// listModels 读取 GET {root}/models。声明了目录时，没有列表路由的端点（404/405）
// 被接受并返回声明的 ID；认证失败从不被掩盖。
func (r *Runtime) listModels(ctx context.Context) (ids []string, listed bool, err error) {
	declared := r.base.Config().Models
	ids, err = openai.ListModels(ctx, r.base.Client())
	if err != nil {
		var rerr *runtime.RuntimeError
		if errors.As(err, &rerr) && (rerr.StatusCode == http.StatusNotFound || rerr.StatusCode == http.StatusMethodNotAllowed) {
			if len(declared) > 0 {
				return declared, false, nil
			}
			return nil, false, r.base.Errorf(runtime.ErrorUpstream, "list_models", "models endpoint unavailable (HTTP %d); declare models in the runtime configuration", rerr.StatusCode)
		}
		return nil, false, err
	}
	if len(declared) > 0 {
		return declared, true, nil
	}
	return r.chatModels(ids), true, nil
}

// chatModels drops non-chat entries and Gemini's "models/" prefix, keeping
// the provider's order and the first of any duplicates.
// chatModels 去掉非聊天条目与 Gemini 的 "models/" 前缀，保持供应商顺序，重复项
// 保留第一个。
func (r *Runtime) chatModels(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
		if id == "" || seen[id] || (r.provider.nonChat != nil && r.provider.nonChat(strings.ToLower(id))) {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// Probe proves the configured key is accepted by listing models.
// Probe 通过列出模型证明所配置的密钥被接受。
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	if err := r.base.CheckOpen("probe"); err != nil {
		return runtime.ProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()
	ids, listed, err := r.listModels(ctx)
	if err != nil {
		return runtime.ProbeResult{}, r.base.Annotate("probe", err)
	}
	evidence := fmt.Sprintf("GET models listed %d chat model(s)", len(ids))
	if !listed {
		evidence = fmt.Sprintf("models listing unavailable; %d declared model(s)", len(ids))
	}
	return runtime.ProbeResult{Kind: r.base.Descriptor().Kind, IdentityVerified: listed, Evidence: r.provider.label + ": " + evidence, ProbedAt: r.base.Clock().Now()}, nil
}

// Health lists models, which costs no inference and checks the key too.
// Health 列出模型：不消耗推理，同时检查密钥。
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	clock := r.base.Clock()
	if err := r.base.CheckOpen("health"); err != nil {
		return runtime.HealthReport{State: runtime.StateClosed, CheckedAt: clock.Now()}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()
	start := clock.Now()
	_, _, err := r.listModels(ctx)
	checkedAt := clock.Now()
	if err != nil {
		return runtime.HealthReport{State: runtime.StateUnhealthy, Latency: checkedAt.Sub(start), CheckedAt: checkedAt, ErrorSummary: oaibase.ErrorSummary(err)}, err
	}
	return runtime.HealthReport{State: runtime.StateHealthy, Latency: checkedAt.Sub(start), CheckedAt: checkedAt}, nil
}

// profile is what every hosted provider documents for its chat completions
// endpoint. Vision and structured output depend on the model, so they stay
// unknown until declared through capability overrides.
//
// profile 是每个托管供应商为其 chat completions 端点给出文档的能力。视觉与结构化
// 输出取决于具体模型，因此在通过能力覆盖声明之前保持未知。
func (r *Runtime) profile() runtime.CapabilitySet {
	set := runtime.CapabilitySet{}
	for _, c := range []runtime.Capability{runtime.CapabilityChat, runtime.CapabilityChatStream, runtime.CapabilityTools} {
		set[c] = runtime.CapabilityEvidence{Capability: c, Level: runtime.SupportSupported, Source: runtime.SourceRuntimeProfile,
			Detail: r.provider.label + " documented OpenAI-compatible chat completions with function calling"}
	}
	return set
}

// Discover publishes the chat models and the provider's capability profile.
// Discover 发布聊天模型及供应商的能力档案。
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	if err := r.base.CheckOpen("discover"); err != nil {
		return runtime.Discovery{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()
	ids, listed, err := r.listModels(ctx)
	if err != nil {
		return runtime.Discovery{}, r.base.Annotate("discover", err)
	}
	caps := runtime.Merge(r.profile(), r.base.OverrideCapabilities())
	warnings := oaibase.ConflictWarnings(caps)
	if !listed {
		warnings = append(warnings, "models endpoint unavailable; publishing the declared catalogue")
	}
	if len(ids) == 0 {
		warnings = append(warnings, "no chat models found; declare models in the runtime configuration")
	}
	models := make([]runtime.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, runtime.Model{ID: id, Capabilities: runtime.Intersect(caps, nil)})
	}
	discovery := runtime.Discovery{Models: models, Capabilities: caps, Warnings: warnings, DiscoveredAt: r.base.Clock().Now()}
	r.base.PublishDiscovery(discovery)
	return discovery, nil
}

// ListModels reports the published chat models.
// ListModels 报告已发布的聊天模型。
func (r *Runtime) ListModels(ctx context.Context) ([]runtime.Model, error) {
	discovery, err := r.Discover(ctx)
	if err != nil {
		return nil, err
	}
	return discovery.Models, nil
}

// Chat sends a non-streaming chat completion through the capability gate.
// Chat 经能力门禁发送非流式聊天补全。
func (r *Runtime) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	return r.base.Chat(ctx, req)
}

// ChatStream sends a streaming chat completion through the capability gate.
// ChatStream 经能力门禁发送流式聊天补全。
func (r *Runtime) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	return r.base.ChatStream(ctx, req)
}

// Embed is gated on an operator-declared embeddings capability.
// Embed 受运维声明的 embeddings 能力门禁约束。
func (r *Runtime) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	return r.base.Embed(ctx, req)
}

// Transcribe is gated on an operator-declared transcription capability.
// Transcribe 受运维声明的转录能力门禁约束。
func (r *Runtime) Transcribe(ctx context.Context, req runtime.AudioTranscriptionRequest, audio io.Reader) (runtime.AudioTranscriptionResponse, error) {
	return r.base.Transcribe(ctx, req, audio)
}

// Rerank is gated on an operator-declared rerank capability.
// Rerank 受运维声明的重排能力门禁约束。
func (r *Runtime) Rerank(ctx context.Context, req runtime.RerankRequest) (runtime.RerankResponse, error) {
	return r.base.Rerank(ctx, req)
}

// Close makes every later call fail with ErrorClosed. It is idempotent.
// Close 使之后的每次调用都以 ErrorClosed 失败，可重复调用。
func (r *Runtime) Close() error { return r.base.Close() }

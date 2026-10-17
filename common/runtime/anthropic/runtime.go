// Package anthropic adapts the Anthropic Messages API, and endpoints that
// implement it, to the runtime.InferenceRuntime contract by translating
// protocol-neutral chat requests into Messages documents. Every Gateway
// front door that dispatches through Chat (OpenAI Chat Completions,
// Responses and Anthropic Messages) can therefore use it.
//
// The adapter touches two endpoints: GET /v1/models and POST /v1/messages.
//
// Package anthropic 把 Anthropic Messages API 以及实现该 API 的端点适配为
// runtime.InferenceRuntime 契约：把协议无关的聊天请求翻译成 Messages 文档。
// 因此 Gateway 所有经 Chat 分派的前门（OpenAI Chat Completions、Responses 与
// Anthropic Messages）都能使用它。
//
// 适配器只访问两个端点：GET /v1/models 与 POST /v1/messages。
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/oaibase"
)

// apiVersion is the anthropic-version header every request carries.
// apiVersion 是每个请求携带的 anthropic-version 请求头取值。
const apiVersion = "2023-06-01"

// maxModelPages bounds catalogue pagination.
// maxModelPages 限制模型目录的翻页次数。
const maxModelPages = 16

// Runtime is the Anthropic Messages adapter. It is safe for concurrent use.
// Runtime 是 Anthropic Messages 适配器，可安全并发使用。
type Runtime struct {
	base     *oaibase.Base
	official bool
}

var _ runtime.InferenceRuntime = (*Runtime)(nil)

// New builds an Anthropic or Anthropic-compatible Runtime. The official API
// takes the key in x-api-key; compatible endpoints differ, so they receive
// it both there and as a Bearer token. A base URL ending in /v1 is accepted
// as well as one without it.
//
// New 构建 Anthropic 或 Anthropic 兼容运行时。官方 API 从 x-api-key 读取密钥；
// 兼容端点做法不一，因此同时以该请求头和 Bearer 令牌两种方式发送。base URL
// 末尾带不带 /v1 都可以。
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	if cfg.Kind != runtime.KindAnthropic && cfg.Kind != runtime.KindAnthropicCompatible {
		return nil, &runtime.RuntimeError{Code: runtime.ErrorInvalidConfig, RuntimeID: cfg.ID, Kind: cfg.Kind, Operation: "create_runtime",
			Message: fmt.Sprintf("kind %q is not an Anthropic Messages provider", cfg.Kind)}
	}
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Canonical keys, so an operator's "Anthropic-Version" or "authorization"
	// replaces the default instead of racing it in map iteration order.
	// 使用规范化的键名，运维写的 "Anthropic-Version" 或 "authorization" 会替换
	// 默认值，而不是与它按 map 遍历顺序随机竞争。
	headers := make(map[string]string, len(cfg.Headers)+2)
	for key, value := range cfg.Headers {
		headers[http.CanonicalHeaderKey(key)] = value
	}
	if _, set := headers["Anthropic-Version"]; !set {
		headers["Anthropic-Version"] = apiVersion
	}
	official := cfg.Kind == runtime.KindAnthropic
	if _, set := headers["Authorization"]; !set && !official && cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + cfg.APIKey
	}
	cfg.Headers = headers
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, &runtime.RuntimeError{Code: runtime.ErrorInvalidConfig, RuntimeID: cfg.ID, Kind: cfg.Kind, Operation: "create_runtime", Message: "invalid base_url"}
	}
	versioned := strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), "/v1")
	base, err := oaibase.NewWithOptions(cfg, cfg.Kind, deps, oaibase.Options{VersionedBaseURL: versioned, AuthHeader: "x-api-key", RefuseRedirects: true})
	if err != nil {
		return nil, err
	}
	return &Runtime{base: base, official: official}, nil
}

// Descriptor reports the instance's stable identity and scheduling summary.
// Descriptor 报告实例的稳定标识与调度摘要。
func (r *Runtime) Descriptor() runtime.Descriptor { return r.base.Descriptor() }

type modelsPage struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// listModels pages through GET /v1/models. With a declared catalogue, an
// endpoint without that route (404/405) is accepted.
// listModels 翻页读取 GET /v1/models。声明了目录时，没有该路由的端点（404/405）
// 被接受。
func (r *Runtime) listModels(ctx context.Context) (ids []string, listed bool, err error) {
	declared := r.base.Config().Models
	after := ""
	seen := map[string]bool{}
	for page := 0; page < maxModelPages; page++ {
		path := "/v1/models?limit=1000"
		if after != "" {
			path += "&after_id=" + url.QueryEscape(after)
		}
		var resp modelsPage
		if err := r.base.Client().Do(ctx, "list_models", http.MethodGet, path, nil, &resp); err != nil {
			var rerr *runtime.RuntimeError
			if errors.As(err, &rerr) && (rerr.StatusCode == http.StatusNotFound || rerr.StatusCode == http.StatusMethodNotAllowed) {
				if len(declared) > 0 {
					return declared, false, nil
				}
				return nil, false, r.base.Errorf(runtime.ErrorUpstream, "list_models", "models endpoint unavailable (HTTP %d); declare models in the runtime configuration", rerr.StatusCode)
			}
			return nil, false, err
		}
		for _, m := range resp.Data {
			if m.ID != "" && !seen[m.ID] {
				seen[m.ID] = true
				ids = append(ids, m.ID)
			}
		}
		if !resp.HasMore || resp.LastID == "" || resp.LastID == after {
			break
		}
		after = resp.LastID
	}
	if len(declared) > 0 {
		return declared, true, nil
	}
	return ids, true, nil
}

// profile is what the Messages API documents: chat, streaming and tool use
// everywhere, image input on the official API only.
// profile 是 Messages API 文档给出的能力：处处支持聊天、流式与工具调用，图片
// 输入仅在官方 API 上支持。
func (r *Runtime) profile() runtime.CapabilitySet {
	set := runtime.CapabilitySet{}
	caps := []runtime.Capability{runtime.CapabilityChat, runtime.CapabilityChatStream, runtime.CapabilityTools}
	if r.official {
		caps = append(caps, runtime.CapabilityVision)
	}
	for _, c := range caps {
		set[c] = runtime.CapabilityEvidence{Capability: c, Level: runtime.SupportSupported, Source: runtime.SourceRuntimeProfile, Detail: "Anthropic Messages API"}
	}
	return set
}

// Probe proves the key is accepted by listing models.
// Probe 通过列出模型证明密钥被接受。
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
	evidence := fmt.Sprintf("GET /v1/models listed %d model(s)", len(ids))
	if !listed {
		evidence = fmt.Sprintf("models listing unavailable; %d declared model(s)", len(ids))
	}
	return runtime.ProbeResult{Kind: r.base.Descriptor().Kind, IdentityVerified: listed, Evidence: evidence, ProbedAt: r.base.Clock().Now()}, nil
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

// Discover publishes the models and the Messages capability profile.
// Discover 发布模型与 Messages 能力档案。
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
		warnings = append(warnings, "no models found; declare models in the runtime configuration")
	}
	models := make([]runtime.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, runtime.Model{ID: id, Capabilities: runtime.Intersect(caps, nil)})
	}
	discovery := runtime.Discovery{Models: models, Capabilities: caps, Warnings: warnings, DiscoveredAt: r.base.Clock().Now()}
	r.base.PublishDiscovery(discovery)
	return discovery, nil
}

// ListModels reports the published models.
// ListModels 报告已发布的模型。
func (r *Runtime) ListModels(ctx context.Context) ([]runtime.Model, error) {
	discovery, err := r.Discover(ctx)
	if err != nil {
		return nil, err
	}
	return discovery.Models, nil
}

func (r *Runtime) requestError(operation string, err error) error {
	return r.base.Errorf(runtime.ErrorInvalidConfig, operation, "%s", err.Error())
}

// Chat sends a non-streaming Messages request through the capability gate.
// Chat 经能力门禁发送非流式 Messages 请求。
func (r *Runtime) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	release, err := r.base.BeginRequest("chat", req.Model, oaibase.ChatCapabilities(req)...)
	if err != nil {
		return runtime.ChatResponse{}, err
	}
	defer release()
	body, err := buildRequest(req, false)
	if err != nil {
		return runtime.ChatResponse{}, r.requestError("chat", err)
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().RequestTimeout)
	defer cancel()
	var resp messagesResponse
	if err := r.base.Client().Do(ctx, "chat", http.MethodPost, "/v1/messages", body, &resp); err != nil {
		return runtime.ChatResponse{}, err
	}
	return toChatResponse(resp, r.base.Clock().Now()), nil
}

// ChatStream sends a streaming Messages request through the capability gate.
// The stream holds a concurrency slot until closed.
// ChatStream 经能力门禁发送流式 Messages 请求。流在关闭前一直占用一个并发槽位。
func (r *Runtime) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	release, err := r.base.BeginRequest("chat_stream", req.Model, append(oaibase.ChatCapabilities(req), runtime.CapabilityChatStream)...)
	if err != nil {
		return nil, err
	}
	body, err := buildRequest(req, true)
	if err != nil {
		release()
		return nil, r.requestError("chat_stream", err)
	}
	cfg := r.base.Config()
	ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	resp, err := r.base.Client().DoStream(ctx, "chat_stream", http.MethodPost, "/v1/messages", body)
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	return startStream(r.base, resp, cancel, release, cfg.StreamIdleTimeout, cfg.APIKey), nil
}

// Embed is refused: Messages has no embeddings endpoint.
// Embed 被拒绝：Messages 没有嵌入端点。
func (r *Runtime) Embed(context.Context, runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	return runtime.EmbeddingResponse{}, r.unsupported("embed")
}

// Transcribe is refused: Messages has no transcription endpoint.
// Transcribe 被拒绝：Messages 没有转录端点。
func (r *Runtime) Transcribe(context.Context, runtime.AudioTranscriptionRequest, io.Reader) (runtime.AudioTranscriptionResponse, error) {
	return runtime.AudioTranscriptionResponse{}, r.unsupported("transcribe")
}

// Rerank is refused: Messages has no rerank endpoint.
// Rerank 被拒绝：Messages 没有重排端点。
func (r *Runtime) Rerank(context.Context, runtime.RerankRequest) (runtime.RerankResponse, error) {
	return runtime.RerankResponse{}, r.unsupported("rerank")
}

func (r *Runtime) unsupported(operation string) error {
	if err := r.base.CheckOpen(operation); err != nil {
		return err
	}
	return &runtime.RuntimeError{Code: runtime.ErrorCapability, RuntimeID: r.base.Config().ID, Kind: r.base.Descriptor().Kind, Operation: operation,
		Message: "not supported by Anthropic Messages", Cause: runtime.ErrCapabilityUnsupported}
}

// Close makes every later call fail with ErrorClosed. It is idempotent.
// Close 使之后的每次调用都以 ErrorClosed 失败，可重复调用。
func (r *Runtime) Close() error { return r.base.Close() }

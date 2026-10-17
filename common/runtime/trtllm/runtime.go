// Package trtllm adapts an already-running NVIDIA TensorRT-LLM `trtllm-serve`
// OpenAI-compatible server to the runtime.InferenceRuntime contract.
//
// The adapter touches six endpoints and no others: /version, /v1/models and
// /server_info to establish identity and role, /health for liveness, and
// /v1/chat/completions and /v1/embeddings to serve requests. trtllm-serve
// also exposes administrative and diagnostic routes — memory release and
// resume, weight updates, profiling, KV-cache event draining, clock offset
// setting — which change or stop a server other tenants are using. This
// adapter never calls them, because discovering an endpoint is not a reason
// to operate it.
//
// SMG reaches TensorRT-LLM over its prototype gRPC protocol instead. That
// path moves tokenization into the caller, which the Agent does not do; the
// OpenAI-compatible server keeps the Agent on the same request path as every
// other local backend.
//
// Package trtllm 把一个已在运行的 NVIDIA TensorRT-LLM `trtllm-serve` OpenAI 兼容
// 服务适配为 runtime.InferenceRuntime 契约。
//
// 适配器只触碰六个端点：/version、/v1/models 与 /server_info 用于确认身份与角色，
// /health 用于存活检查，/v1/chat/completions 与 /v1/embeddings 用于处理请求。
// trtllm-serve 还暴露管理与诊断路由——显存释放与恢复、权重更新、性能剖析、KV
// cache 事件抽取、时钟偏移设置——它们会改变或停掉其他租户正在使用的服务器。本适配器
// 从不调用它们，因为发现一个端点不构成操作它的理由。
//
// SMG 改走 TensorRT-LLM 的原型 gRPC 协议接入。那条路径要求调用方自己做分词，而
// Agent 不做分词；OpenAI 兼容服务让 Agent 与其他本地后端保持同一条请求路径。
package trtllm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/oaibase"
	"AIServeWeave/common/runtime/openai"
)

const (
	versionPath    = "/version"
	healthPath     = "/health"
	modelsPath     = "/v1/models"
	serverInfoPath = "/server_info"

	// ownedBy is the owned_by default of trtllm-serve's ModelCard
	// (tensorrt_llm/serve/openai_protocol.py). vLLM reports "vllm" and
	// SGLang "sglang" in the same field, which is what lets this adapter
	// verify identity where a /version route alone could not: vLLM serves
	// /version too.
	//
	// ownedBy 是 trtllm-serve 的 ModelCard 中 owned_by 的默认值
	// （tensorrt_llm/serve/openai_protocol.py）。vLLM 在同一字段填 "vllm"、SGLang 填
	// "sglang"，正因如此本适配器能核实身份，而单靠 /version 路由做不到：vLLM 也提供
	// /version。
	ownedBy = "tensorrt_llm"
)

// Runtime is the TensorRT-LLM adapter. It is safe for concurrent use; the
// capability snapshot published by Discover is replaced atomically and only
// read on the request path.
//
// Runtime 是 TensorRT-LLM 适配器，可并发使用；Discover 发布的能力快照以原子方式
// 替换，请求路径上只读。
type Runtime struct {
	base *oaibase.Base
}

// Compile-time proof that the adapter satisfies the inference contract.
var _ runtime.InferenceRuntime = (*Runtime)(nil)

// New builds a TensorRT-LLM Runtime for a normalized, validated Config. It
// performs no network I/O; Manager owns Probe, Discover and scheduling.
//
// New has the signature of runtime.Factory, so it can be registered with
// runtime.Registry.Register(runtime.KindTRTLLM, trtllm.New).
//
// New 为一份已规范化、已校验的 Config 构造 TensorRT-LLM Runtime，不做任何网络 I/O；
// Probe、Discover 与调度归 Manager 负责。它符合 runtime.Factory 签名，可以用
// runtime.Registry.Register(runtime.KindTRTLLM, trtllm.New) 注册。
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	base, err := oaibase.New(cfg, runtime.KindTRTLLM, deps)
	if err != nil {
		return nil, err
	}
	return &Runtime{base: base}, nil
}

// Descriptor reports the instance's stable identity and scheduling summary.
// Descriptor 报告实例的稳定身份与调度摘要。
func (r *Runtime) Descriptor() runtime.Descriptor { return r.base.Descriptor() }

// Chat sends a non-streaming chat completion, gated on the capabilities the
// last Discover confirmed.
//
// Chat 发送一次非流式聊天补全，受上一次 Discover 确认的能力门禁。
func (r *Runtime) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	return r.base.Chat(ctx, req)
}

// ChatStream sends a streaming chat completion. The returned stream holds
// the instance's concurrency slot until it is closed, so callers must close
// it — including after Recv returns io.EOF.
//
// ChatStream 发送一次流式聊天补全。返回的流在关闭前一直占用实例的并发额度，因此
// 调用方必须关闭它——包括 Recv 返回 io.EOF 之后。
func (r *Runtime) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	return r.base.ChatStream(ctx, req)
}

// Embed requests embeddings. trtllm-serve only serves /v1/embeddings when
// started as `trtllm-serve embeddings` with an encoder model, which no
// endpoint reports, so the capability stays unknown until an operator
// declares it through Config.CapabilityOverrides.
//
// Embed 请求嵌入。trtllm-serve 只有以 `trtllm-serve embeddings` 加编码器模型启动时
// 才提供 /v1/embeddings，而没有端点报告这一点，因此该能力保持 unknown，直到运维经
// Config.CapabilityOverrides 声明。
func (r *Runtime) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	return r.base.Embed(ctx, req)
}

// Transcribe runs an audio transcription or translation. trtllm-serve has no
// transcription route, so the shared gate rejects every call unless an
// operator declares the capability.
//
// Transcribe 执行音频转录或翻译。trtllm-serve 没有转录路由，因此除非运维声明该
// 能力，共享门禁会拒绝每一次调用。
func (r *Runtime) Transcribe(ctx context.Context, req runtime.AudioTranscriptionRequest, audio io.Reader) (runtime.AudioTranscriptionResponse, error) {
	return r.base.Transcribe(ctx, req, audio)
}

// Rerank scores documents against a query. No endpoint reports
// CapabilityRerank, so the shared gate rejects every call unless an operator
// declares it.
//
// Rerank 依据查询为文档打分。没有端点报告 CapabilityRerank，因此除非运维声明，
// 共享门禁会拒绝每一次调用。
func (r *Runtime) Rerank(ctx context.Context, req runtime.RerankRequest) (runtime.RerankResponse, error) {
	return r.base.Rerank(ctx, req)
}

// Close releases the instance's concurrency budget and makes every
// subsequent call fail with ErrorClosed. It is idempotent.
//
// Close 释放实例的并发额度，此后每次调用都以 ErrorClosed 失败。可重复调用。
func (r *Runtime) Close() error { return r.base.Close() }

// Probe verifies the endpoint is trtllm-serve: GET /version must answer and
// GET /v1/models must list at least one model whose owned_by is
// "tensorrt_llm". A server that reports another owner, or none, is not
// adopted — /version alone would also accept a vLLM server, and both
// default to port 8000.
//
// Probe 核实端点是 trtllm-serve：GET /version 必须应答，且 GET /v1/models 至少列出
// 一个 owned_by 为 "tensorrt_llm" 的模型。报告其他 owner 或没有 owner 的服务器
// 不会被采纳——单靠 /version 也会接受 vLLM，而两者的默认端口都是 8000。
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	if err := r.base.CheckOpen("probe"); err != nil {
		return runtime.ProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()

	version, err := r.fetchVersion(ctx, "probe")
	if err != nil {
		return runtime.ProbeResult{}, r.base.ProbeMismatch(err, "probe", versionPath)
	}
	cards, err := openai.ListModelCards(ctx, r.base.Client())
	if err != nil {
		return runtime.ProbeResult{}, r.base.ProbeMismatch(err, "probe", modelsPath)
	}
	if !ownedByTRTLLM(cards) {
		return runtime.ProbeResult{}, r.base.Errorf(runtime.ErrorProbeMismatch, "probe",
			"%s listed no model owned by %q, so this is not a trtllm-serve server", modelsPath, ownedBy)
	}

	return runtime.ProbeResult{
		Kind:             runtime.KindTRTLLM,
		Version:          version,
		IdentityVerified: true,
		Evidence: fmt.Sprintf("GET %s reported version %s; GET %s listed %d model(s) owned by %q",
			versionPath, versionOrUnknown(version), modelsPath, len(cards), ownedBy),
		ProbedAt: r.base.Clock().Now(),
	}, nil
}

// Health calls GET /health, which answers 200 with an empty body while the
// executor is alive and 503 once it has hit a fatal error. It runs no
// inference, so a saturated server still answers.
//
// Health 调用 GET /health：执行器存活时返回空 body 的 200，遇到致命错误后返回 503。
// 它不执行推理，因此已满载的服务器照样应答。
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	clock := r.base.Clock()
	if err := r.base.CheckOpen("health"); err != nil {
		return runtime.HealthReport{State: runtime.StateClosed, CheckedAt: clock.Now()}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()

	start := clock.Now()
	err := r.base.Client().Do(ctx, "health", http.MethodGet, healthPath, nil, nil)
	checkedAt := clock.Now()
	if err != nil {
		return runtime.HealthReport{
			State:        runtime.StateUnhealthy,
			Latency:      checkedAt.Sub(start),
			CheckedAt:    checkedAt,
			ErrorSummary: oaibase.ErrorSummary(err),
		}, err
	}
	return runtime.HealthReport{
		State:     runtime.StateHealthy,
		Latency:   checkedAt.Sub(start),
		CheckedAt: checkedAt,
	}, nil
}

// Discover reports the served model and the capabilities its role implies.
//
// trtllm-serve runs in one of several roles, and only the main LLM role
// serves chat: `trtllm-serve embeddings` and the multimodal-encoder role
// answer /version and /v1/models too, but not /v1/chat/completions. The
// main role is the only one that registers /server_info, so that route
// answering is the endpoint evidence for chat, chat_stream and completions.
// Everything else — tools, structured output, reasoning, vision — depends
// on launch options (--tool_parser, guided_decoding_backend, the model's
// chat template) that no endpoint reports, and stays unknown until declared
// through capability overrides.
//
// Discover 报告所服务的模型及其角色所蕴含的能力。
//
// trtllm-serve 以几种角色之一运行，只有主 LLM 角色提供聊天：`trtllm-serve
// embeddings` 与多模态编码器角色同样应答 /version 和 /v1/models，却不提供
// /v1/chat/completions。只有主角色注册 /server_info，因此该路由应答就是 chat、
// chat_stream 与 completions 的端点证据。其余能力——工具、结构化输出、推理、
// 视觉——取决于没有任何端点报告的启动参数（--tool_parser、guided_decoding_backend、
// 模型的 chat template），在经能力覆盖声明之前保持 unknown。
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	if err := r.base.CheckOpen("discover"); err != nil {
		return runtime.Discovery{}, err
	}

	version, err := r.fetchVersion(ctx, "discover")
	if err != nil {
		return runtime.Discovery{}, err
	}
	ids, err := openai.ListModels(ctx, r.base.Client())
	if err != nil {
		return runtime.Discovery{}, err
	}

	var warnings []string
	if version == "" {
		warnings = append(warnings, fmt.Sprintf("%s did not report a version", versionPath))
	}

	observed := runtime.CapabilitySet{}
	switch err := r.base.Client().Do(ctx, "discover", http.MethodGet, serverInfoPath, nil, nil); {
	case err == nil:
		observed = endpointCapabilities()
	case isNotFound(err):
		warnings = append(warnings, fmt.Sprintf(
			"%s is not served, so this trtllm-serve instance is not in the main LLM role (an embeddings or multimodal-encoder server); chat stays unknown until declared through capability overrides",
			serverInfoPath))
	default:
		return runtime.Discovery{}, err
	}

	runtimeCaps := runtime.Merge(observed, r.base.OverrideCapabilities())
	warnings = append(warnings, oaibase.ConflictWarnings(runtimeCaps)...)

	models := make([]runtime.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, runtime.Model{
			ID:           id,
			Capabilities: runtime.Intersect(runtimeCaps, nil),
		})
	}

	discovery := runtime.Discovery{
		Version:      version,
		Models:       models,
		Capabilities: runtimeCaps,
		Warnings:     warnings,
		DiscoveredAt: r.base.Clock().Now(),
	}
	r.base.PublishDiscovery(discovery)
	return discovery, nil
}

// ListModels reports the models the server currently serves, with the same
// capability resolution Discover applies.
//
// ListModels 报告服务器当前提供的模型，能力解析方式与 Discover 相同。
func (r *Runtime) ListModels(ctx context.Context) ([]runtime.Model, error) {
	discovery, err := r.Discover(ctx)
	if err != nil {
		return nil, err
	}
	return discovery.Models, nil
}

// endpointCapabilities is the conclusion drawn from /server_info answering:
// the server is in the main LLM role, which registers the chat and
// completions routes. It says nothing about optional features.
//
// endpointCapabilities 是从 /server_info 应答得出的结论：服务器处于主 LLM 角色，
// 该角色注册聊天与补全路由。它不说明任何可选特性。
func endpointCapabilities() runtime.CapabilitySet {
	const detail = "GET " + serverInfoPath + " answered, which trtllm-serve registers only in the main LLM role alongside the chat and completions routes; this says nothing about optional features"
	set := make(runtime.CapabilitySet, 3)
	for _, capability := range []runtime.Capability{
		runtime.CapabilityChat,
		runtime.CapabilityChatStream,
		runtime.CapabilityCompletions,
	} {
		set[capability] = runtime.CapabilityEvidence{
			Capability: capability,
			Level:      runtime.SupportSupported,
			Source:     runtime.SourceEndpoint,
			Detail:     detail,
		}
	}
	return set
}

// ownedByTRTLLM reports whether any listed model carries trtllm-serve's
// owned_by value. / ownedByTRTLLM 报告是否有任何列出的模型带有 trtllm-serve 的 owned_by 值。
func ownedByTRTLLM(cards []openai.ModelCard) bool {
	for _, c := range cards {
		if c.OwnedBy == ownedBy {
			return true
		}
	}
	return false
}

// isNotFound reports whether err is an HTTP 404 from the backend.
// isNotFound 报告 err 是否为后端返回的 HTTP 404。
func isNotFound(err error) bool {
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) {
		return false
	}
	return rerr.StatusCode == http.StatusNotFound
}

type versionResponse struct {
	Version string `json:"version"`
}

// fetchVersion reads GET /version. A missing version field is not an error:
// it yields an empty string, which callers treat as "version unknown".
//
// fetchVersion 读取 GET /version。缺少 version 字段不算错误：返回空字符串，调用方
// 视为「版本未知」。
func (r *Runtime) fetchVersion(ctx context.Context, operation string) (string, error) {
	var resp versionResponse
	if err := r.base.Client().Do(ctx, operation, http.MethodGet, versionPath, nil, &resp); err != nil {
		return "", err
	}
	return resp.Version, nil
}

func versionOrUnknown(version string) string {
	if version == "" {
		return "(absent)"
	}
	return version
}

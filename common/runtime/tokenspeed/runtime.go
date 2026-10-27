// Package tokenspeed adapts an already-running `tokenspeed serve` deployment
// to the runtime.InferenceRuntime contract.
//
// `tokenspeed serve` does not implement HTTP itself: it starts the TokenSpeed
// engine behind a gRPC servicer and puts an SMG gateway in front of it, and
// the port clients reach (8000 by default) is SMG's. That decides two things
// about this adapter. First, the only identity it can verify is "an SMG
// gateway answers here": SMG fronts vLLM, SGLang and TensorRT-LLM gRPC
// workers just as well, and TokenSpeed's own version is reported only by a
// side control server on a separate, by default random, port. So the kind
// comes from configuration and every ProbeResult reports
// IdentityVerified=false, the same honesty the SGLang adapter applies.
// Second, liveness is read from SMG's GET /readiness, not GET /health: SMG
// answers /health with 200 even while draining or before any worker is
// healthy, whereas /readiness answers 503 in exactly those states.
//
// The adapter touches four endpoints and no others: /readiness and
// /v1/models to establish identity and liveness, and /v1/chat/completions
// and /v1/embeddings to serve requests. SMG's worker-management and
// load-reporting routes, and TokenSpeed's control server with its weight
// update routes, are never called.
//
// Package tokenspeed 把一个已在运行的 `tokenspeed serve` 部署适配为
// runtime.InferenceRuntime 契约。
//
// `tokenspeed serve` 自己不实现 HTTP：它把 TokenSpeed 引擎放在 gRPC servicer 后面，
// 前面再起一个 SMG 网关，客户端访问的端口（默认 8000）属于 SMG。这决定了本适配器的
// 两点。第一，它能核实的身份只有「这里应答的是一个 SMG 网关」：SMG 同样可以代理
// vLLM、SGLang 与 TensorRT-LLM 的 gRPC worker，而 TokenSpeed 自己的版本只由另一个
// 端口（默认随机）上的旁路控制服务器报告。因此种类取自配置，每个 ProbeResult 都报告
// IdentityVerified=false，与 SGLang 适配器同样如实。第二，存活性读 SMG 的
// GET /readiness 而不是 GET /health：SMG 在排空期间、或尚无健康 worker 时 /health
// 照样返回 200，而 /readiness 恰好在这些状态下返回 503。
//
// 适配器只触碰四个端点：/readiness 与 /v1/models 用于确认身份与存活性，
// /v1/chat/completions 与 /v1/embeddings 用于处理请求。SMG 的 worker 管理与负载
// 上报路由，以及 TokenSpeed 控制服务器及其权重更新路由，从不调用。
package tokenspeed

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/oaibase"
	"AIServeWeave/common/runtime/openai"
)

const (
	readinessPath = "/readiness"
	modelsPath    = "/v1/models"

	// readyStatus is the status SMG's /readiness reports once the gateway
	// can route (model_gateway/src/health.rs).
	//
	// readyStatus 是 SMG 的 /readiness 在网关可以路由后报告的 status
	// （model_gateway/src/health.rs）。
	readyStatus = "ready"
)

// Runtime is the TokenSpeed adapter. It is safe for concurrent use; the
// capability snapshot published by Discover is replaced atomically and only
// read on the request path.
//
// Runtime 是 TokenSpeed 适配器，可并发使用；Discover 发布的能力快照以原子方式
// 替换，请求路径上只读。
type Runtime struct {
	base *oaibase.Base
}

// Compile-time proof that the adapter satisfies the inference contract.
var _ runtime.InferenceRuntime = (*Runtime)(nil)

// New builds a TokenSpeed Runtime for a normalized, validated Config. It
// performs no network I/O; Manager owns Probe, Discover and scheduling.
//
// New has the signature of runtime.Factory, so it can be registered with
// runtime.Registry.Register(runtime.KindTokenSpeed, tokenspeed.New).
//
// New 为一份已规范化、已校验的 Config 构造 TokenSpeed Runtime，不做任何网络 I/O；
// Probe、Discover 与调度归 Manager 负责。它符合 runtime.Factory 签名，可以用
// runtime.Registry.Register(runtime.KindTokenSpeed, tokenspeed.New) 注册。
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	base, err := oaibase.New(cfg, runtime.KindTokenSpeed, deps)
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

// Embed requests embeddings. SMG registers /v1/embeddings whatever engine
// sits behind it, so the route existing says nothing about the loaded model;
// the capability stays unknown until an operator declares it through
// Config.CapabilityOverrides.
//
// Embed 请求嵌入。无论背后是什么引擎，SMG 都注册 /v1/embeddings，因此路由存在并不
// 说明所加载的模型；该能力保持 unknown，直到运维经 Config.CapabilityOverrides 声明。
func (r *Runtime) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	return r.base.Embed(ctx, req)
}

// Transcribe runs an audio transcription or translation. No endpoint reports
// the capability, so the shared gate rejects every call unless an operator
// declares it.
//
// Transcribe 执行音频转录或翻译。没有端点报告该能力，因此除非运维声明，共享门禁会
// 拒绝每一次调用。
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

// Probe checks that an SMG gateway answers: GET /readiness must report
// status "ready" and GET /v1/models must answer. A /readiness that is absent
// or does not decode as SMG's JSON is a mismatch; a 503 (not ready yet, or
// draining) passes through as an upstream failure so Manager retries rather
// than dropping the instance.
//
// Probe 检查这里应答的是一个 SMG 网关：GET /readiness 必须报告 status "ready"，且
// GET /v1/models 必须应答。/readiness 不存在、或无法按 SMG 的 JSON 解码，视为类型
// 不匹配；503（尚未就绪或正在排空）作为上游失败原样传出，让 Manager 重试而不是丢弃
// 该实例。
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	if err := r.base.CheckOpen("probe"); err != nil {
		return runtime.ProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()

	ready, err := r.readiness(ctx, "probe")
	if err != nil {
		return runtime.ProbeResult{}, r.base.ProbeMismatch(err, "probe", readinessPath)
	}
	if ready.Status != readyStatus {
		return runtime.ProbeResult{}, r.base.Errorf(runtime.ErrorProbeMismatch, "probe",
			"%s did not report an SMG readiness status", readinessPath)
	}
	models, err := openai.ListModels(ctx, r.base.Client())
	if err != nil {
		return runtime.ProbeResult{}, r.base.ProbeMismatch(err, "probe", modelsPath)
	}

	return runtime.ProbeResult{
		Kind:             runtime.KindTokenSpeed,
		Version:          "", // SMG serves no version route; see the package comment
		IdentityVerified: false,
		Evidence: fmt.Sprintf("GET %s answered as an SMG gateway with %d healthy worker(s); GET %s listed %d model(s); runtime kind asserted by configuration, since the SMG front end cannot tell TokenSpeed from the other engines it fronts",
			readinessPath, ready.HealthyWorkers, modelsPath, len(models)),
		ProbedAt: r.base.Clock().Now(),
	}, nil
}

// Health calls GET /readiness, which runs no inference and answers 503 while
// the gateway has no healthy worker or is draining — both states in which
// the Gateway must stop sending new work here.
//
// Health 调用 GET /readiness：它不执行推理，在网关没有健康 worker 或正在排空时返回
// 503——这两种状态下 Gateway 都必须停止向这里派发新请求。
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	clock := r.base.Clock()
	if err := r.base.CheckOpen("health"); err != nil {
		return runtime.HealthReport{State: runtime.StateClosed, CheckedAt: clock.Now()}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()

	start := clock.Now()
	ready, err := r.readiness(ctx, "health")
	if err == nil && ready.Status != readyStatus {
		err = r.base.Errorf(runtime.ErrorUpstream, "health", "%s reported status %q", readinessPath, ready.Status)
	}
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

// Discover reports the served models. SMG answering /readiness is the
// endpoint evidence for chat, chat_stream and completions, which the gateway
// registers for every worker. Tools and structured output depend on launch
// options SMG does not report (TokenSpeed's grammar backend defaults to none,
// and tool parsing is per model), so they, like embeddings, stay unknown
// until declared through capability overrides.
//
// Discover 报告所服务的模型。SMG 应答 /readiness 是 chat、chat_stream 与
// completions 的端点证据，网关为每个 worker 都注册这些路由。工具与结构化输出取决于
// SMG 不报告的启动参数（TokenSpeed 的 grammar backend 默认为 none，工具解析因模型
// 而异），因此它们与嵌入一样，在经能力覆盖声明之前保持 unknown。
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	if err := r.base.CheckOpen("discover"); err != nil {
		return runtime.Discovery{}, err
	}
	ids, err := openai.ListModels(ctx, r.base.Client())
	if err != nil {
		return runtime.Discovery{}, err
	}

	runtimeCaps := runtime.Merge(endpointCapabilities(), r.base.OverrideCapabilities())
	warnings := []string{
		"the SMG front end serves no version route, so no version is reported; TokenSpeed's version is only available from its control server",
	}
	warnings = append(warnings, oaibase.ConflictWarnings(runtimeCaps)...)

	models := make([]runtime.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, runtime.Model{
			ID:           id,
			Capabilities: runtime.Intersect(runtimeCaps, nil),
		})
	}

	discovery := runtime.Discovery{
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

// endpointCapabilities is the conclusion drawn from an SMG gateway answering:
// it registers the chat and completions routes for every worker. It says
// nothing about optional features.
//
// endpointCapabilities 是从 SMG 网关应答得出的结论：它为每个 worker 注册聊天与补全
// 路由。它不说明任何可选特性。
func endpointCapabilities() runtime.CapabilitySet {
	const detail = "GET " + readinessPath + " answered as an SMG gateway, which registers the chat and completions routes for every worker; this says nothing about optional features"
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

// readinessResponse models the two fields this adapter reads from SMG's
// /readiness body; the reason and worker totals are ignored.
//
// readinessResponse 只建模本适配器从 SMG 的 /readiness 响应中读取的两个字段；原因与
// worker 总数忽略。
type readinessResponse struct {
	Status         string `json:"status"`
	HealthyWorkers int    `json:"healthy_workers"`
}

// readiness reads GET /readiness. / readiness 读取 GET /readiness。
func (r *Runtime) readiness(ctx context.Context, operation string) (readinessResponse, error) {
	var resp readinessResponse
	if err := r.base.Client().Do(ctx, operation, http.MethodGet, readinessPath, nil, &resp); err != nil {
		return readinessResponse{}, err
	}
	return resp, nil
}

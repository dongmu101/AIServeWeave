# Agent Runtime 接入规划

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**目标：** 在 Agent 中接入用户已经启动的 vLLM、SGLang、Ollama 和 ComfyUI，统一完成探测、健康检查、能力发现和请求代理。

**架构：** 四种后端共用运行时管理接口；vLLM、SGLang、Ollama 通过 OpenAI-compatible 专用接口处理同步及 SSE 推理，ComfyUI 通过独立工作流接口处理提交、事件、取消和产物。`Registry` 负责构造实例，`Manager` 负责实例状态与周期健康检查，各适配器只处理自身协议差异。

**技术栈：** Go 1.26、`net/http`、`net/url`、`encoding/json`、`log/slog`、SSE、`context`、`httptest`，以及仅用于 ComfyUI 的 `github.com/coder/websocket`。首期不引入其他第三方依赖。

## 全局约束

- 首期只支持 External Runtime：后端由用户安装并启动，Agent 不负责安装、启动、停止、升级、模型下载或进程托管。
- 运行时类型必须由配置明确指定，不通过端口或模糊响应自动猜测。
- vLLM、SGLang、Ollama 共用 OpenAI-compatible 传输层，但各自保留独立适配器和能力修正规则。
- ComfyUI 是异步工作流运行时，不强行实现 LLM 同步推理接口。
- 能力不确定时返回 `unknown`，不能根据模型名称猜测，也不能静默丢弃请求参数。
- 所有网络调用都接受 `context.Context`；所有响应体、流和后台协程都必须可关闭。
- API Key、自定义鉴权头和完整 Prompt 不得写入日志或错误文本。
- 默认测试不依赖 GPU、外部网络或真实运行时。

---

## 当前状态

阶段 1、1b、2、3、4、5、6 和 7 已落地：公共接口、核心类型（含完整 `ChatRequest` 字段集）、能力合并与门禁、错误模型（含哨兵错误）、`Config` 校验、`deps.go` 协作者接口、`limiter.go`、`runtimetest` fakes、`openai` 共享客户端（普通 Chat、Embedding、SSE 流式 Chat）、Registry 和 Manager（含健康状态机、抖动调度、原子替换、不可变 Snapshot），以及 Ollama、vLLM、SGLang 三个推理适配器均可编译可测试；Ollama 已用真实后端（0.32.14）验证，另两个仅有模拟后端测试。三者的请求路径、并发限流、能力门禁和版本区间应用逻辑下沉为共享的 `internal/oaibase`、`internal/capprofile`，各适配器只保留自身的探测/健康/发现协议。ComfyUI 工作流适配器亦已落地，是唯一引入第三方依赖（`github.com/coder/websocket`）的部分，且该依赖只出现在 `wsdial.go` 一个文件里。剩余工作是阶段 8 的集成、指标与真实后端契约测试。本文件描述目标设计和实施顺序，勾选状态以实施计划中的 checkbox 为准。

| 文件 | 状态 | 说明 |
| --- | --- | --- |
| `runtime.go` | 已实现 | 三个接口已固定，尚无适配器编译期断言 |
| `types.go` | 已实现 | 类型骨架、`ChatRequest` 完整字段集（含 `Tool`/`ResponseFormat`）齐备 |
| `config.go` | 已实现 | `Normalize`/`Validate`/`LogValue` |
| `deps.go` | 已实现 | `Dependencies`、`Clock`、`WSDialer`/`WSConn`、`Metrics` 及子接口；生产 `Clock` 用 `NewSystemClock` |
| `capability.go` | 已实现 | 能力常量、`Merge`/`Resolve`/`Require`/`Intersect` |
| `errors.go` | 已实现 | `RuntimeError`、状态码映射、`Redact`、哨兵错误、`ErrorBackpressure` |
| `stream.go` | 已实现 | `Stream[T]` 与 `ChanStream[T]`，含 `Committed()` |
| `limiter.go` | 已实现 | 单实例并发上限 |
| `internal/runtimetest/` | 已实现 | fake `Clock`/`Runtime`/`WSDialer`/`WSConn` |
| `registry.go` | 已实现 | 只读 `Registry`，`Create` 校验配置和依赖后再构造 |
| `manager.go` | 已实现 | `Manager`：Add/Replace/Remove/Get/Snapshot/Close，健康状态机与抖动调度 |
| `openai/*.go` | 已实现 | `client.go`/`models.go`/`chat.go`/`embedding.go`/`sse.go`/`stream.go`/`errors.go` 均已实现并测试 |
| `internal/oaibase/` | 已实现 | Ollama、vLLM 共用的请求路径、并发限流、能力快照与门禁 |
| `internal/capprofile/` | 已实现 | `Table.Resolve`/`ParseVersion`/`Compare`，版本区间能力表的通用应用逻辑 |
| `ollama/` | 已实现 | 原生发现 + OpenAI-compatible 推理；已用真实后端（0.32.14）验证 |
| `vllm/` | 已实现 | `/version`+`/v1/models` 探测、`/health`、保守能力表；未接真实后端 |
| `sglang/` | 已实现 | `/v1/models` 探测（身份不强验证）、`/health` 缺失降级、可选 `/get_server_info`；未接真实后端 |
| `workflow/comfyui/` | 已实现 | `client.go`/`events.go`/`runtime.go`/`wsdial.go`：HTTP 客户端、单连接事件复用器、WorkflowRuntime、安全取消、产物流式下载；未接真实后端 |

已有文件布局：

```text
runtime/
├── runtime.go
├── types.go
├── capability.go
├── errors.go
├── errors_test.go
├── stream.go
├── stream_test.go
├── registry.go
├── manager.go
├── internal/
│   ├── runtimetest/
│   ├── oaibase/
│   └── capprofile/
├── openai/
├── vllm/
├── sglang/
├── ollama/
└── workflow/comfyui/
```

## 范围

### 首期包含

- 校验运行时配置并创建客户端。
- 验证指定地址是否可访问、是否满足该运行时的最低协议要求。
- 周期健康检查，维护 `unknown`、`healthy`、`unhealthy`、`closed` 状态。
- 发现后端版本、模型、节点类型及可验证的能力。
- 代理 OpenAI Chat Completions、Embeddings 的普通和流式请求。
- 为后续 Responses API 预留能力枚举和窄接口扩展点；只有完成契约测试后才标记为支持。
- 提交 ComfyUI API Format 工作流，订阅进度，查询历史，取消安全范围内的任务并下载产物。
- 归一化错误、超时和事件，同时保留排障所需的后端信息。
- 为 Registry、Tunnel 和上层状态上报提供只读快照。

### 首期不包含

- 自动安装或拉起 vLLM、SGLang、Ollama、ComfyUI。
- Docker、Python、CUDA、模型和自定义节点的生命周期管理。
- 模型下载、删除、加载、卸载或 LoRA 动态管理。
- 逻辑模型路由、跨节点调度、配额、计费和租户鉴权。
- Agent Tunnel 的具体传输协议。
- ComfyUI 工作流模板管理、Job 持久化和对象存储。
- 将 Agent 变成可访问任意地址的通用 HTTP 代理。

## 设计原则

1. **管理统一，调用分型。** 生命周期和状态统一；LLM 推理与 ComfyUI 工作流保持不同接口。
2. **显式配置优先。** `kind`、地址、鉴权、超时和能力覆盖均来自受信配置。
3. **能力以证据为准。** 端点探测、模型元数据和配置覆盖分别记录来源；未知不等于不支持。
4. **流式链路端到端传递。** 不在内存聚合 SSE 或大文件，取消信号必须传到上游。
5. **适配器保持窄职责。** 共享包负责 HTTP/OpenAI/SSE；运行时包只负责端点与语义差异。
6. **安全失败。** 类型不匹配、协议异常、能力未知或取消不安全时返回明确错误。

## 总体架构

```text
Runtime Config
      │
      ▼
┌──────────────┐     kind -> Factory      ┌────────────────────────┐
│   Registry   │─────────────────────────►│ vLLM / SGLang / Ollama │
└──────┬───────┘                          │ InferenceRuntime       │
       │                                  └────────────┬───────────┘
       │                                               │
       │                                  shared OpenAI HTTP + SSE
       │                                               │
       │                                  ┌────────────▼───────────┐
       │                                  │ User-started backend   │
       │                                  └────────────────────────┘
       │
       │                                  ┌────────────────────────┐
       └─────────────────────────────────►│ ComfyUI WorkflowRuntime│
                                          └────────────┬───────────┘
                                                       │ HTTP + WebSocket
                                          ┌────────────▼───────────┐
                                          │ User-started ComfyUI   │
                                          └────────────────────────┘

Manager
  ├── owns Runtime instances
  ├── schedules health/discovery refresh
  ├── applies failure/recovery thresholds
  └── publishes immutable snapshots
```

调用链：

```text
加载配置
  → Registry 按 kind 创建 Runtime
  → Probe 验证最低协议
  → Discover 获取版本、模型和能力证据
  → Manager 注册实例并启动周期 Health
  → 上层按接口类型和能力选择实例
  → Runtime 执行类型化请求代理
  → 返回普通结果、SSE 事件或 Workflow 事件
```

## 公共接口草案

**实现文件：**

- `common/runtime/runtime.go`：`Runtime`、`InferenceRuntime`、`WorkflowRuntime`。
- `common/runtime/stream.go`：`Stream[T]` 及通用关闭语义。

接口名称和签名在第一阶段通过编译期断言及契约测试固定。后续适配器不得绕过这些接口向 `Manager` 塞入后端私有状态。

```go
type Runtime interface {
	Descriptor() Descriptor
	Probe(ctx context.Context) (ProbeResult, error)
	Health(ctx context.Context) (HealthReport, error)
	Discover(ctx context.Context) (Discovery, error)
	Close() error
}

type InferenceRuntime interface {
	Runtime
	ListModels(ctx context.Context) ([]Model, error)
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest) (Stream[ChatEvent], error)
	Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
}

type WorkflowRuntime interface {
	Runtime
	Submit(ctx context.Context, req WorkflowRequest) (WorkflowRun, error)
	Subscribe(ctx context.Context, runID string) (Stream[WorkflowEvent], error)
	Status(ctx context.Context, runID string) (WorkflowStatus, error)
	Cancel(ctx context.Context, runID string) error
	OpenArtifact(ctx context.Context, ref ArtifactRef) (Artifact, error)
}

type Stream[T any] interface {
	Recv() (T, error)
	// Committed reports whether at least one item has already reached the
	// consumer; once true the caller must not transparently retry.
	Committed() bool
	Close() error
}
```

接口约束：

- `Probe` 用于首次接入验证，失败时不得注册为可用实例。
- `Health` 必须轻量，不触发模型推理或工作流执行。
- `Discover` 可以比 `Health` 重，按较低频率执行并返回不可变快照。
- `Close` 必须幂等，关闭空闲连接、WebSocket 和 Manager 启动的协程。
- `ChatStream` 和 `Subscribe` 返回成功后，终止错误由 `Recv` 返回。
- `Recv` 在正常结束时返回 `io.EOF`；调用 `Close` 或取消 Context 后必须及时退出。
- `WorkflowRun.ID` 是后端 `prompt_id` 的本地封装；公开 Job ID 由上层生成。

## 核心类型

### `common/runtime/types.go`

该文件保存 Runtime 包对外共享的数据结构：

- 标识与配置：`Kind`、`Config`、`TLSConfig`、`Descriptor`。
- 状态与探测：`State`、`ProbeResult`、`HealthReport`。
- 发现结果：`Discovery`、`Model`。
- 推理请求：`ChatRequest`、`ChatResponse`、`ChatEvent`、`EmbeddingRequest`、`EmbeddingResponse`。
- 工作流请求：`WorkflowRequest`、`WorkflowRun`、`WorkflowEvent`、`WorkflowStatus`、`ArtifactRef`、`Artifact`。

```go
type Kind string

const (
	KindVLLM    Kind = "vllm"
	KindSGLang  Kind = "sglang"
	KindOllama  Kind = "ollama"
	KindComfyUI Kind = "comfyui"
)

type Config struct {
	ID                  string
	Kind                Kind
	BaseURL             string
	APIKey              string
	Headers             map[string]string
	ProbeTimeout        time.Duration
	RequestTimeout      time.Duration
	StreamIdleTimeout   time.Duration
	HealthInterval      time.Duration
	DiscoveryInterval   time.Duration
	MaxConcurrent       int
	TLS                 TLSConfig
	CapabilityOverrides map[Capability]SupportLevel
	Exclusive           bool
}

type TLSConfig struct {
	CAFile             string
	ServerName         string
	InsecureSkipVerify bool
}

// Descriptor 只包含可安全用于状态快照的稳定配置，不得包含 API Key、
// 自定义 Header 值或其他凭据。
type Descriptor struct {
	ID            string
	Kind          Kind
	BaseURL       string
	MaxConcurrent int
	Exclusive     bool
}

type ProbeResult struct {
	Kind             Kind
	Version          string
	IdentityVerified bool
	Evidence         string
	ProbedAt         time.Time
}

type HealthReport struct {
	State        State
	Latency      time.Duration
	CheckedAt    time.Time
	ErrorSummary string
}

type Discovery struct {
	Version      string
	Models       []Model
	NodeTypes    []string
	Capabilities CapabilitySet
	Warnings     []string
	DiscoveredAt time.Time
}

type Model struct {
	ID           string
	Capabilities CapabilitySet
}

type State string

const (
	StateRegistering State = "registering"
	StateUnknown     State = "unknown"
	StateHealthy     State = "healthy"
	StateUnhealthy   State = "unhealthy"
	StateClosed      State = "closed"
)
```

`Descriptor` 保存稳定配置摘要：`ID` 和 `Kind` 标识实例，`BaseURL` 表示规范化后的后端地址，`MaxConcurrent` 和 `Exclusive` 提供调度约束。它不得包含 `APIKey`、自定义 Header 值或 TLS 凭据。`ProbeResult` 保存运行时类型、版本、身份验证强度和安全证据摘要；`HealthReport` 保存状态、延迟、检测时间和脱敏错误摘要；`Discovery` 保存版本、模型、节点类型、运行时能力、模型能力、降级告警及证据来源。所有时间字段由适配器写入 UTC 时间。

`ChatRequest` 等类型只表达 Runtime 层需要的协议中立字段。`openai` 子包定义线上 JSON DTO 并负责二者转换，从而避免把后端私有字段扩散到 Manager，也避免包循环依赖。

当前 `ChatRequest` 只有 `Model` 和 `Messages`，不足以承载「请求、流与事件」章节要求的采样参数、工具调用和扩展字段。阶段 2 补齐为：

```go
type ChatRequest struct {
	Model          string
	Messages       []ChatMessage
	Temperature    *float64
	TopP           *float64
	MaxTokens      *int
	Stop           []string
	Seed           *int64
	Tools          []Tool
	ToolChoice     string
	ResponseFormat *ResponseFormat
	// Extra 转发后端私有参数。键与已建模字段冲突时，
	// 转换层返回 ErrorInvalidConfig，不做静默覆盖。
	Extra map[string]json.RawMessage
}
```

采样参数使用指针，用于区分「调用方未设置」与「显式设为 0」；后者必须原样传给后端，不能被默认值吞掉。可选字段为 nil 时不出现在线上 JSON 中。需要 `Tools` 的请求必须先通过 `CapabilityTools` 门禁；需要 `ResponseFormat` 的请求先通过 `CapabilityStructuredOutput`。

`ChatRequest` 不含 `Stream` 字段：流式与否由调用 `Chat` 还是 `ChatStream` 决定，`openai` 转换层负责设置线上 `stream` 与 `stream_options`。

### `common/runtime/capability.go`

该文件保存能力名称、三态值、证据来源和合并规则：

```go
type Capability string

type CapabilitySource string

type SupportLevel string

const (
	SupportUnknown     SupportLevel = "unknown"
	SupportSupported   SupportLevel = "supported"
	SupportUnsupported SupportLevel = "unsupported"
)

type CapabilityEvidence struct {
	Capability Capability
	Level      SupportLevel
	Source     CapabilitySource
	Detail     string
}

type CapabilitySet map[Capability]CapabilityEvidence
```

建议首期能力集合：

```text
chat
chat_stream
completions
embeddings
responses
vision
tools
parallel_tool_calls
structured_output
reasoning
workflow_execution
workflow_events
workflow_cancel
artifact_read
```

这些名称必须声明为 `Capability` 常量（`CapabilityChat`、`CapabilityChatStream`、…），禁止在适配器里散落字符串字面量。

每项能力使用三态值，并记录以下来源之一：

- `endpoint`：目标端点实际存在且返回符合契约的响应。
- `model_metadata`：后端模型元数据明确声明。
- `runtime_profile`：已测试运行时版本的保守能力表。
- `config_override`：管理员显式覆盖，优先级最高。

禁止仅通过模型名字中包含 `vision`、`embed`、`tool` 等字样推断能力。

### 合并规则

```go
// Merge 按来源优先级合并多个来源产生的能力集合，后传入的低优先级来源
// 不会覆盖已有的高优先级结论。输入不被修改，返回新集合。
func Merge(sets ...CapabilitySet) CapabilitySet

// Resolve 返回单项能力的最终结论；缺失等价于 SupportUnknown。
func (s CapabilitySet) Resolve(c Capability) CapabilityEvidence

// Require 是调用前门禁：能力为 unsupported 时返回包裹
// ErrCapabilityUnsupported 的错误，为 unknown 时返回包裹
// ErrCapabilityUnknown 的错误，两者错误码均为 ErrorCapability。
func (s CapabilitySet) Require(c Capability) error
```

优先级从高到低固定为 `config_override` > `endpoint` > `model_metadata` > `runtime_profile`。补充规则：

- 同优先级来源冲突时，`unsupported` 胜过 `supported`，`supported` 胜过 `unknown`；即同级冲突向保守方向收敛，并在 `Discovery.Warnings` 记录一条冲突说明。
- 模型级能力与运行时级能力分别合并后再取交集：运行时不支持则模型一定不支持；运行时 `unknown` 时以模型结论为准。
- `config_override` 只能在 `Config.CapabilityOverrides` 中出现，`Detail` 固定记录来源为配置，便于排障时区分「后端真的支持」和「管理员声明支持」。

### 门禁位置

能力门禁只在两处生效，避免重复校验和判断分歧：

1. 适配器的 `Chat`/`ChatStream`/`Embed`/`Submit` 入口，对本实例最近一次 `Discover` 的结果调用 `Require`。
2. 上层调度器选择实例前，对 `Snapshot.Discovery.Capabilities` 调用 `Resolve`。

适配器持有的能力快照由 `Discover` 原子替换，请求路径只读，不在请求路径上触发探测。

## 文件职责规划

| 文件 | 职责 |
| --- | --- |
| `runtime.go` | `Runtime`、`InferenceRuntime`、`WorkflowRuntime` 接口及编译期约束 |
| `types.go` | Kind、Config、Descriptor、Probe、Health、Discovery、模型和请求结果类型 |
| `config.go` | `Config.Normalize`、`Validate`、`LogValue` 及 URL/Header 规则 |
| `deps.go` | `Dependencies`、`Clock`、`WSDialer`、`WSConn`、`Metrics` 协作者接口 |
| `capability.go` | 能力常量、三态、证据来源、合并优先级和 `Require` 门禁 |
| `errors.go` | 错误分类、哨兵错误、上游状态码映射、脱敏和 `errors.Is/As` 支持 |
| `stream.go` | 泛型流接口、`ChanStream`、关闭语义、空闲超时和终止错误 |
| `limiter.go` | 单实例并发上限、在途计数和本地背压错误 |
| `registry.go` | Factory 注册、重复检测、Normalize/Validate 编排、按 Kind 构造实例 |
| `manager.go` | 实例所有权、状态机、周期检查、快照和优雅关闭 |
| `openai/client.go` | 共享 HTTP 客户端、URL 拼接、鉴权头、大小限制和响应解码 |
| `openai/models.go` | `/v1/models` 类型和调用 |
| `openai/chat.go` | Chat Completions 普通请求及响应类型 |
| `openai/embedding.go` | Embeddings 请求及响应类型 |
| `openai/sse.go` | SSE 帧解析；支持多行 `data`、注释、空行和 `[DONE]` |
| `openai/stream.go` | SSE 到 `ChatEvent` 的转换及取消传播 |
| `openai/errors.go` | OpenAI-compatible 错误体解析与公共错误映射 |
| `internal/oaibase/oaibase.go` | Ollama、vLLM（未来还有 SGLang）共用的 `Chat`/`ChatStream`/`Embed`、并发限流获取/释放、能力快照发布与门禁、`ProbeMismatch`/`ErrorClosed` 分类 |
| `internal/capprofile/capprofile.go` | `Table.Resolve` 按版本区间应用能力表、`ParseVersion`、`Compare`；各适配器的 `profile.go` 只放具体版本表 |
| `vllm/runtime.go` | vLLM 探测、版本、健康、模型和能力修正 |
| `vllm/profile.go` | 按已测试版本维护的保守 `runtime_profile` 能力表 |
| `sglang/runtime.go` | SGLang 探测、健康、模型、降级标记和能力修正 |
| `sglang/profile.go` | SGLang `runtime_profile` 能力表 |
| `ollama/runtime.go` | Ollama 原生发现接口与 OpenAI-compatible 推理桥接 |
| `ollama/profile.go` | Ollama `runtime_profile` 能力表 |
| `workflow/comfyui/client.go` | ComfyUI HTTP/WebSocket 客户端、请求和响应类型 |
| `workflow/comfyui/events.go` | 单连接事件复用器、按 `prompt_id` 分发和重连 |
| `workflow/comfyui/runtime.go` | WorkflowRuntime、状态归一化和安全取消 |
| `workflow/comfyui/wsdial.go` | 基于 `github.com/coder/websocket` 的生产 `WSDialer`；本包唯一引用第三方库的文件 |

`profile.go` 以版本区间为键，只声明该版本确定支持或确定不支持的能力，其余保持 `unknown`；新增条目必须附带对应契约测试或官方文档依据，禁止凭猜测扩表。

测试与实现文件同目录放置，优先使用黑盒测试（`package xxx_test`）；只有 SSE 帧解析、URL 拼接和事件归一化这类内部细节使用包内测试。跨包共用的 fake Runtime、fake Clock 和 fake WSConn 放在 `runtime/internal/runtimetest`，避免各测试文件重复实现，也避免测试辅助代码进入公共 API。

## 配置规则

配置示例：

```yaml
runtimes:
  - id: local-ollama
    kind: ollama
    base_url: http://127.0.0.1:11434
    probe_timeout: 3s
    request_timeout: 5m
    stream_idle_timeout: 60s

  - id: gpu-vllm
    kind: vllm
    base_url: http://10.0.0.20:8000
    api_key_ref: secret://runtime/gpu-vllm

  - id: gpu-sglang
    kind: sglang
    base_url: http://10.0.0.21:30000
    api_key_ref: secret://runtime/gpu-sglang

  - id: local-comfyui
    kind: comfyui
    base_url: http://127.0.0.1:8188
    exclusive: false
```

校验要求：

- `id` 非空且在单个 Agent 内唯一。
- `kind` 只能是四个已注册值之一。
- `base_url` 只允许 `http` 或 `https`，拒绝 URL userinfo、query 和 fragment。
- 路径前缀允许存在，但 URL 拼接必须保留前缀，不能用字符串直接相加。
- 自定义 Header 禁止覆盖 `Host`、`Content-Length`、hop-by-hop headers 和 Agent 链路追踪头。
- 生产配置通过 Secret 引用提供密钥；`Config` 的格式化方法必须脱敏。
- YAML 中的 `api_key_ref` 由 Agent 配置层解析，Factory 收到的 `Config.APIKey` 已是解析后的内存值，禁止重新持久化。
- Agent 应限制可访问的地址范围；即使配置来自控制面，也不能把 Runtime 变成任意 URL 代理。

校验入口固定为三个方法，定义在 `runtime/config.go`：

```go
// Normalize 填充零值字段为默认值，并把 BaseURL 规范化为不含尾斜杠的
// scheme://host[/prefix] 形式。返回新值，不修改接收者。
func (c Config) Normalize() Config

// Validate 校验规范化后的配置，聚合报告全部问题而非只返回第一个，
// 错误码为 ErrorInvalidConfig。
func (c Config) Validate() error

// LogValue 实现 slog.LogValuer，输出 ID、Kind、BaseURL、超时和并发上限，
// 并省略 APIKey、Headers 值和 TLS 凭据路径。
func (c Config) LogValue() slog.Value
```

`Normalize` 必须在 `Validate` 之前调用，Registry 的 `Create` 内部按此顺序执行，调用方无需自行拼装。URL 拼接统一走 `url.URL.JoinPath`，保留配置中的路径前缀。

建议默认值：探测和健康检查超时 `3s`，发现超时 `10s`，普通请求超时 `5m`，流空闲超时 `60s`，健康检查间隔 `10s`，发现间隔 `5m`，单实例并发上限 `32`。所有值可按实例覆盖；Context 截止时间始终优先。零值一律视为「未设置」并取默认值；如需真正无限制，必须显式配置为负值并触发一条告警。

## 后端接入矩阵

### vLLM

**实现文件：** `common/runtime/vllm/runtime.go`

| 动作 | 首选端点 | 说明 |
| --- | --- | --- |
| Probe | `GET /version`、`GET /v1/models` | 两者都符合契约才完成类型验证 |
| Health | `GET /health` | 仅检查引擎健康，不执行推理 |
| Discover | `GET /version`、`GET /v1/models` | 模型级高级能力仍需配置或版本能力表 |
| Chat | `POST /v1/chat/completions` | 普通响应和 SSE |
| Embedding | `POST /v1/embeddings` | 仅对明确支持的模型开放 |

vLLM 还可能暴露 Responses、音频、rerank 等端点，但首期不因端点出现在新版文档中就自动上报；每项能力必须有契约测试和明确版本证据。参考 [vLLM OpenAI-Compatible Server](https://docs.vllm.ai/en/latest/serving/online_serving/openai_compatible_server/) 与 [vLLM Security](https://docs.vllm.ai/en/latest/usage/security/)。

### SGLang

**实现文件：** `common/runtime/sglang/runtime.go`

| 动作 | 首选端点 | 说明 |
| --- | --- | --- |
| Probe | `GET /v1/models` | `kind` 来自配置；不能仅凭 OpenAI 响应与 vLLM 互相识别，结果必须标记身份未被强验证 |
| Health | `GET /health`；版本不兼容时退化为 `GET /v1/models` | 退化必须在状态快照中标记 |
| Discover | `GET /v1/models`；可选 `GET /get_server_info` | 私有响应只用于补充版本和负载，不进入公共模型契约 |
| Chat | `POST /v1/chat/completions` | 普通响应和 SSE |
| Embedding | 仅在适配器契约测试通过时启用 | 默认 `unknown` |

**能力证据的不对称说明：** SGLang 没有版本端点，`/get_server_info` 是否报版本随部署而定，因此
`chat`/`chat_stream`/`completions` 由 `SourceEndpoint` 证据给出（`/v1/models` 应答，且 SGLang 的
OpenAI-compatible server 与该路由注册在同一 server 上），而不像 vLLM 那样要求先读到版本；
`tools`、`structured_output`、`embeddings` 等随版本和启动参数变化的能力仍只认版本表和
`CapabilityOverrides`。这条推断及其边界写在 `sglang/runtime.go` 的 `endpointCapabilities` 与
证据 `Detail` 中，便于评审时直接看到依据。

SGLang 同时提供原生 `/generate`，首期不接入，避免同时维护两套生成协议。参考 [SGLang Bench Serving Guide](https://docs.sglang.ai/developer_guide/bench_serving) 与 [SGLang Observability](https://docs.sglang.ai/advanced_features/observability.html)。

### Ollama

**实现文件：** `common/runtime/ollama/runtime.go`

| 动作 | 首选端点 | 说明 |
| --- | --- | --- |
| Probe | `GET /api/version`、`GET /api/tags` | 使用原生 API 确认 Ollama 身份 |
| Health | `GET /api/version` | 不加载模型，不执行推理 |
| Discover | `GET /api/tags`，按需 `POST /api/show` | `/api/show` 用于补充单模型能力，限制并发和刷新频率 |
| Chat | `POST /v1/chat/completions` | 复用共享 OpenAI-compatible 客户端 |
| Embedding | `POST /v1/embeddings` | 对外保持 OpenAI 语义 |

**已知差异（实测 0.32.14）：** 具备 `thinking` 能力的模型在 `/v1/chat/completions` 的响应里，把思考内容放在非标准的 `choices[].message.reasoning`（流式为 `delta.reasoning`）字段，`content` 只保留最终答案。`ChatMessage`/`ChatMessageDelta` 目前没有这个字段，因此思考内容会被丢弃：流式调用只收到最终答案（可接受），而非流式调用若 `max_tokens` 在思考阶段就耗尽，会返回 `content` 为空、`finish_reason` 为 `length` 的结果。这不是 Ollama 专有问题——vLLM 和 SGLang 的 reasoning 模型也走同一形状，因此归属应当是 `runtime.ChatMessage` 增加 reasoning 字段并由 `openai` 层统一解析，而不是在 Ollama 适配器里单独处理；在跨适配器方案确定前，本适配器不私自加字段。

Ollama 原生接口用于身份和模型元数据，OpenAI-compatible 接口用于推理，避免自行转换其原生 NDJSON。不同版本支持的 OpenAI 字段不同，未验证字段保持 `unknown`。参考 [Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)、[List models](https://docs.ollama.com/api/tags) 和 [Get version](https://docs.ollama.com/api/version)。

### ComfyUI

**实现文件：**

- `common/runtime/workflow/comfyui/client.go`：HTTP/WebSocket 协议客户端和线上 DTO。
- `common/runtime/workflow/comfyui/runtime.go`：`WorkflowRuntime` 实现、事件分发和状态归一化。

| 动作 | 端点 | 说明 |
| --- | --- | --- |
| Probe/Health | `GET /system_stats` | 返回设备与运行时信息 |
| Discover | `GET /features`、`GET /object_info`、`GET /models`、`GET /models/{folder}` | 节点定义和模型集合分开保存 |
| Submit | `POST /prompt` | 只接收 API Format 工作流 |
| Events | `GET /ws?clientId=...` | 按 `prompt_id` 复用和分发事件 |
| Status | `GET /queue`、`GET /history/{prompt_id}` | 历史结果作为最终状态依据 |
| Cancel | `POST /queue` 或 `POST /interrupt` | 根据 pending/running 和 exclusive 规则选择 |
| Artifact | `GET /view` | 流式返回并限制大小 |

ComfyUI WebSocket 是实例级事件流。每个 Runtime 只维护一个连接和事件分发器，按 `prompt_id` 路由给订阅者；提交前先确保 WebSocket 已连接，断线后通过 History 对账，不能假设所有事件都会被接收。

取消规则：

- 仍在等待队列的目标任务，可以通过队列操作删除指定 `prompt_id`。
- 正在运行的任务只有在 `exclusive: true` 且确认当前任务就是目标任务时才允许调用 `/interrupt`。
- 共享 ComfyUI 实例或无法确认当前任务时返回 `ErrCancelUnsupported`，不得中断其他调用者的任务。

参考 [ComfyUI Server Routes](https://docs.comfy.org/development/comfyui-server/comms_routes) 与 [ComfyUI WebSocket Messages](https://docs.comfy.org/development/comfyui-server/comms_messages)。

## Registry 与 Manager

### Registry

**实现文件：** `common/runtime/registry.go`

```go
type Factory func(cfg Config, deps Dependencies) (Runtime, error)

type Registry interface {
	Register(kind Kind, factory Factory) error
	Create(cfg Config, deps Dependencies) (Runtime, error)
	Kinds() []Kind
}

// Dependencies 注入全部外部协作者，使适配器不依赖包级全局变量，
// 测试可以逐项替换。零值不可用：Create 必须校验必填字段。
type Dependencies struct {
	HTTPClient *http.Client
	WSDialer   WSDialer // 仅 ComfyUI 使用，其他 Kind 可为 nil
	Clock      Clock
	Logger     *slog.Logger
	Metrics    Metrics
}

// Clock 抽象时间，使 Manager 的周期调度和适配器的超时可在测试中确定化。
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) (<-chan time.Time, func() bool)
}

// WSDialer 是 ComfyUI 需要的最小 WebSocket 拨号面，
// 生产实现包装 github.com/coder/websocket。
type WSDialer interface {
	Dial(ctx context.Context, url string, header http.Header) (WSConn, error)
}

type WSConn interface {
	Read(ctx context.Context) (messageType int, data []byte, err error)
	Close() error
}

// Metrics 只接受本 README「可观测性」章节列出的指标，
// 实现方负责标签基数控制。
type Metrics interface {
	Counter(name string, labels map[string]string) Counter
	Gauge(name string, labels map[string]string) Gauge
	Histogram(name string, labels map[string]string) Histogram
}
```

`Clock`、`WSDialer`、`WSConn`、`Metrics` 及其子接口定义在 `runtime/deps.go`，避免 `types.go` 混杂数据结构与协作者接口。`Metrics` 的三个子接口保持最小面（`Add(float64)`、`Set(float64)`、`Observe(float64)`），Runtime 层不感知具体指标库。

- 默认 Registry 在启动阶段注册四个 Factory，运行阶段只读。
- 重复注册返回 `ErrFactoryAlreadyRegistered`。
- 未知类型返回 `ErrRuntimeKindUnsupported`。
- Factory 只构造客户端，不启动健康检查协程。
- `Dependencies` 注入 `http.Client`、WebSocket dialer、Clock、Logger 和 Metrics，测试不替换全局变量。
- ComfyUI WebSocket 使用 `github.com/coder/websocket`；适配器只依赖内部定义的窄 Dial/Conn 接口，生产实现再包装该库，便于测试连接、断线和取消。

### Manager

**实现文件：** `common/runtime/manager.go`

```go
type Manager interface {
	Add(ctx context.Context, cfg Config) error
	Replace(ctx context.Context, cfg Config) error
	Remove(ctx context.Context, id string) error
	Get(id string) (Runtime, bool)
	Snapshot() []Snapshot
	Close(ctx context.Context) error
}

// Snapshot 是单个实例的不可变状态视图。切片和 CapabilitySet 在返回前
// 深拷贝，调用方的修改不会影响 Manager 内部状态。
type Snapshot struct {
	Descriptor  Descriptor
	State       State
	Probe       ProbeResult
	Health      HealthReport
	Discovery   Discovery
	Inflight    int    // 当前在途请求数
	Degraded    []string // 端点降级说明，例如 SGLang 缺少 /health
	UpdatedAt   time.Time
}
```

`Add` 内部串行执行「创建 → Probe → Discover → 注册 → 启动调度」；Probe 失败时实例不进入实例表并被立即 `Close`，不留 `registering` 僵尸条目。`Get` 返回接口值，调用方通过类型断言取得 `InferenceRuntime` 或 `WorkflowRuntime`；Manager 不提供按能力选择实例的调度方法，那属于上层调度器。

Manager 使用互斥锁保护实例表，但任何网络调用都不能持锁执行：先在锁内取出实例引用，再释放锁发起请求。读取方获得不可变 `Snapshot`，不能拿到 Manager 内部可修改对象。

状态转换：

```text
registering
    │ Probe + Discover 成功
    ▼
 healthy ──连续 3 次 Health 失败──► unhealthy
    ▲                                  │
    └────连续 2 次 Health 成功─────────┘

任意状态 ── Remove/Close ──► closed
```

- 健康检查默认每 `10s` 执行一次，并加入最多 `10%` 抖动，避免所有实例同时请求。
- Discover 默认每 `5m` 刷新；健康恢复后立即刷新一次。
- 同一实例最多一个 Health 和一个 Discover 在途；慢请求不会堆积。
- 配置替换采用“新实例 Probe 成功后原子替换，最后关闭旧实例”。
- `Manager.Close` 先停止调度，再取消在途检查，最后关闭全部实例并汇总错误。

## 请求、流与事件

### LLM 普通请求

```text
调用方 Context
  → 校验请求能力和模型
  → 构造目标 URL 与鉴权头
  → 发起 HTTP 请求
  → 限制错误体/响应体大小
  → 解码 OpenAI-compatible 响应
  → 返回统一响应或 RuntimeError
```

核心 OpenAI 字段使用明确 Go 类型。后端扩展参数通过 `Extra map[string]json.RawMessage` 保留，但禁止覆盖已建模字段；这样既能转发 vLLM/SGLang 扩展，又不会让同一字段产生两种值。

### SSE

- 支持 `event`、`data`、`id`、`retry` 字段和注释行。
- 连续多行 `data` 使用换行拼接；空行结束一个事件。
- `[DONE]` 转换为正常结束，不作为 JSON 解码错误。
- 单行和单事件均设置大小上限，防止无界内存增长。
- 收到首个对外事件后标记 `Committed=true`；上层不得再透明重试。
- Context 取消、流空闲超时或 `Close` 必须关闭响应体，使读取协程退出。

### ComfyUI 事件

首期归一化以下事件：

```text
status                → queue_changed
execution_start       → started
execution_cached      → cached
executing             → node_started / completed
progress              → progress
executed              → node_output
execution_success     → succeeded
execution_error       → failed
execution_interrupted → cancelled
```

未知事件保留类型和受大小限制的原始 JSON，不能导致 WebSocket 连接退出。二进制预览帧首期忽略并计数，正式产物以 History 和 `/view` 为准。

## 错误模型

**实现文件：**

- `common/runtime/errors.go`：公共错误类型和错误分类。
- `common/runtime/openai/errors.go`：OpenAI-compatible 错误响应解码。

```go
type ErrorCode string

const (
	ErrorInvalidConfig       ErrorCode = "invalid_config"
	ErrorConnection          ErrorCode = "connection_failed"
	ErrorProbeMismatch       ErrorCode = "probe_mismatch"
	ErrorUnauthorized        ErrorCode = "unauthorized"
	ErrorCapability          ErrorCode = "capability_unsupported"
	ErrorRateLimited         ErrorCode = "rate_limited"
	ErrorTimeout             ErrorCode = "timeout"
	ErrorUpstream            ErrorCode = "upstream_error"
	ErrorProtocol            ErrorCode = "protocol_error"
	ErrorResponseTooLarge    ErrorCode = "response_too_large"
	ErrorCancelUnsupported   ErrorCode = "cancel_unsupported"
	ErrorClosed              ErrorCode = "runtime_closed"
)
```

除错误码外，还需要一组哨兵错误，供 Registry、Manager 和 ComfyUI 取消路径使用；本文其他章节已按名称引用它们，但目前尚未定义：

```go
var (
	ErrFactoryAlreadyRegistered = errors.New("runtime: factory already registered")
	ErrRuntimeKindUnsupported   = errors.New("runtime: runtime kind unsupported")
	ErrRuntimeIDDuplicated      = errors.New("runtime: runtime id duplicated")
	ErrRuntimeNotFound          = errors.New("runtime: runtime not found")
	ErrCancelUnsupported        = errors.New("runtime: cancel unsupported")
	ErrCapabilityUnknown        = errors.New("runtime: capability unknown")
	ErrCapabilityUnsupported    = errors.New("runtime: capability unsupported")
	ErrConcurrencyLimit         = errors.New("runtime: concurrency limit reached")
	ErrRuntimeClosed            = errors.New("runtime: runtime closed")
)
```

哨兵错误只作为 `RuntimeError.Cause` 出现，不单独返回给调用方，这样 `errors.Is` 既能匹配错误码也能匹配具体原因。对应关系：`ErrCancelUnsupported` → `ErrorCancelUnsupported`，能力类 → `ErrorCapability`，`ErrConcurrencyLimit` → 本地背压（不占用上游错误码），配置与注册类 → `ErrorInvalidConfig`，`ErrRuntimeClosed` → `ErrorClosed`。

`RuntimeError` 包含 `Code`、`RuntimeID`、`Kind`、`Operation`、可选 `StatusCode`、可安全暴露的 `Message`、是否可重试和底层 `Cause`。要求：

- 支持 `errors.Is`、`errors.As` 和 `Unwrap`。
- 401/403 映射为未授权，429 映射为限流，5xx 映射为上游错误。
- 连接拒绝、DNS 和 TLS 错误归类为连接失败；Context deadline 归类为超时。
- 错误响应体最多读取固定字节数，并对 Authorization、Cookie、API Key 和用户配置的 Secret 值脱敏。
- 保留上游请求 ID 响应头，但不回传任意上游头。

## 超时、重试与背压

- Runtime 层不自动重试推理请求；是否换节点由上层调度器决定。
- Probe、Health 和幂等 Discover 允许 Manager 按下一检测周期重试，不做紧密循环。
- 普通请求只有在未收到响应头时才具备重试资格。
- 流式请求一旦产生事件，不得透明重试。
- ComfyUI `POST /prompt` 超时后结果不确定，先通过提交时的 `client_id` 和上层幂等键对账，不能直接重复提交。
- 每个 Runtime 使用独立并发上限（`limiter.go`）；达到上限立即返回包裹 `ErrConcurrencyLimit` 的错误，不排队、不阻塞调用方。
- 并发额度在推理请求和工作流提交上计数；`Probe`、`Health`、`Discover` 不占用额度，否则后端繁忙时会失去健康可见性。
- 流式请求在整个流生命周期内持有额度，直到 `Recv` 返回终止错误或调用 `Close`；额度释放必须走 `defer`，不能依赖正常路径。
- 大响应、Artifact 和流式数据边读边传，禁止 `io.ReadAll` 无上限读取；统一使用 `io.LimitReader` 加显式上限，超限返回 `ErrorResponseTooLarge`。
- 建议上限：普通响应体 `8MB`，错误体 `64KB`，单条 SSE 行 `1MB`，单个 WebSocket 事件 `1MB`，Artifact 无内存上限但必须流式且受总字节数配置约束。

## 安全要求

- Runtime 只能访问已校验的配置地址及其同源相对路径。
- 禁止调用 vLLM/SGLang 的 profiling、cache reset、动态加载和其他管理端点。
- 禁止代理调用方提供的任意 URL、Host 或 Authorization。
- 默认不记录请求体、Prompt、图片内容和工作流完整 JSON。
- 日志只记录 Runtime ID、Kind、操作、状态码、延迟、请求 ID 和错误分类。
- TLS 支持自定义 CA；跳过证书校验只能作为显式开发配置，并产生告警。
- ComfyUI 工作流大小、节点数、输入文件和产物大小都必须有上限；模板和节点白名单由上层负责。

## 可观测性

建议指标：

```text
runtime_health_status{runtime_id,kind}
runtime_probe_total{runtime_id,kind,result}
runtime_discovery_duration_seconds{runtime_id,kind}
runtime_requests_total{runtime_id,kind,operation,result}
runtime_request_duration_seconds{runtime_id,kind,operation}
runtime_stream_first_event_seconds{runtime_id,kind,operation}
runtime_stream_active{runtime_id,kind,operation}
runtime_upstream_errors_total{runtime_id,kind,code,status}
comfyui_queue_remaining{runtime_id}
comfyui_websocket_reconnects_total{runtime_id}
comfyui_events_total{runtime_id,type}
```

Agent 接收到的 `request_id` 必须通过允许的 Header 或请求字段传递给后端，并出现在所有关联日志与指标 exemplar 中。

指标约定：`result` 标签只取 `success`、`client_error`、`upstream_error`、`timeout`、`cancelled`、`backpressure` 六个值，不使用原始状态码作为标签；状态码只出现在 `runtime_upstream_errors_total` 的 `status` 上。`runtime_stream_first_event_seconds` 是 TTFT 的可观测代理，用于后续跨节点调度。标签中禁止出现模型名以外的用户输入，避免基数爆炸。

## 版本兼容矩阵

各适配器的 `runtime_profile` 能力表以下表为基线；每次验证真实后端后更新对应版本，并在提交信息中记录。未在此表出现的版本一律按 `unknown` 处理，不做向前推断。

| 后端 | 最低支持版本 | 已验证版本 | 关键差异 |
| --- | --- | --- | --- |
| vLLM | 待定 | 待填写 | `/version` 字段名随版本变化；Responses 端点仅新版存在 |
| SGLang | 待定 | 待填写 | 早期版本无 `/health`；`/get_server_info` 为私有响应且不稳定 |
| Ollama | 0.1.24 | 0.32.14 | OpenAI-compatible 层支持的字段逐版本增加；`capabilities` 内联进 `/api/tags` 是较新行为，旧版需 `/api/show` 兜底 |
| ComfyUI | 待定 | 待填写 | `/features`、`/models/{folder}` 为较新增接口；事件字段有增删 |

阶段 8 的真实后端契约测试是本表的唯一填写依据；缺失依据时保持「待填写」，不允许用文档推测代替验证。Ollama 一行的「已验证版本」由 `ollama/live_test.go` 对本机 `0.32.14` 实跑 Probe/Health/Discover/Chat/SSE Chat 得出；「最低支持版本」取自 `ollama/profile.go` 的能力下限，属于文档依据而非实测，低于 `0.32.14` 的版本仍未实跑验证。

## 质量门禁

按开源项目标准，以下检查在每个阶段结束时全部通过后才能勾选该阶段：

```bash
gofmt -l ./common/runtime
go vet ./common/runtime/...
go test ./common/runtime/...
go test -race ./common/runtime/...
```

补充要求：

- `gofmt -l` 必须无输出；所有导出标识符具备以标识符开头的完整 doc comment。
- 协程泄漏在每个包的 `TestMain` 中统一断言，不逐个测试手写检查。
- 测试不使用真实 `time.Sleep` 推进时间，一律通过注入的 `Clock` 控制；例外只允许出现在真实后端契约测试中。
- 新增第三方依赖需在阶段说明中列出理由；首期只允许 `github.com/coder/websocket` 一项。
- 表驱动测试的每个用例必须有可读 `name`，失败信息包含期望值与实际值。
- 公共 API 变更同步更新本 README 的接口草案与文件职责表，README 与代码不一致视为缺陷。

## 实施计划

阶段依赖关系，阶段 4 到 6 可在阶段 2、3 完成后并行推进：

```text
阶段 1 (公共契约)
   │
   ├──► 阶段 1b (配置校验 + 能力合并)
   │        │
   │        ▼
   ├──► 阶段 2 (openai 共享客户端) ──┐
   │                                 │
   └──► 阶段 3 (Registry + Manager) ─┤
                                     ▼
                    ┌────────────┬────────────┬────────────┐
                    ▼            ▼            ▼            ▼
                阶段 4        阶段 5       阶段 6       阶段 7
                Ollama        vLLM        SGLang       ComfyUI
                    └────────────┴────────────┴────────────┘
                                     ▼
                              阶段 8 (集成 + 契约测试)
```

阶段 7 只依赖阶段 1、1b、3，不依赖 `openai` 包，可与阶段 2 并行开始。

### 阶段 1：公共契约、错误模型与流

**文件：** `runtime.go`、`types.go`、`capability.go`、`errors.go`、`stream.go` 及对应 `_test.go`。

范围收窄说明：Config 校验与能力合并原计划在本阶段完成，实际推迟到阶段 1b，因为在没有消费者时无法确定校验细节。本阶段实际交付的是类型骨架、错误模型和流实现。

- [x] 先写错误映射和 Stream 关闭语义测试。
- [x] 运行 `go test ./common/runtime -run 'Test(RuntimeError|Stream)'`，确认测试因类型尚未实现而失败。
- [x] 实现本 README 中的公共类型和接口；增加四个适配器的编译期接口断言位置。（`Runtime`/`InferenceRuntime`/`WorkflowRuntime`、错误模型和 `ChanStream` 已实现；适配器编译期断言待各自阶段落地对应类型时加入。）
- [x] 再运行同一命令，要求通过且不存在协程泄漏。
- [x] 运行 `go test -race ./common/runtime`，要求通过。

**交付物：** 上层可以只依赖 `runtime` 包完成实例分类、错误判断和流读取。

### 阶段 1b：配置校验、能力模型与共享支撑

阶段 1 只固定了类型骨架，以下内容被推迟且必须在任何适配器动工前补齐，否则阶段 2 到 7 会各自发明一套校验和能力判断。

**文件：** 创建 `config.go`、`deps.go`、`limiter.go`、`config_test.go`、`capability_test.go`、`limiter_test.go`；修改 `capability.go`、`errors.go`、`types.go`。

- [x] 写 `Config.Normalize`/`Validate` 表驱动测试：缺失 `id`、非法 `kind`、非 http(s) scheme、URL 含 userinfo/query/fragment、路径前缀保留、禁用 Header 覆盖、零值取默认、负值显式无限制。
- [x] 写 `Config.LogValue` 脱敏测试，断言输出不含 `APIKey`、Header 值和 TLS 文件路径。
- [x] 写 `CapabilitySet` 测试：来源优先级、同级冲突向保守收敛并产生 Warning、运行时与模型能力取交集、`Require` 对 unknown 和 unsupported 返回不同 Cause 且错误码同为 `ErrorCapability`。
- [x] 写 `limiter` 测试：额度耗尽返回 `ErrConcurrencyLimit`、释放后可再获取、并发获取与释放在 `-race` 下无竞态、Close 后不再发放额度。
- [x] 运行 `go test ./common/runtime`，确认失败原因来自未实现的方法而非编译错误以外的意外。
- [x] 实现 `config.go`、能力常量与合并门禁、哨兵错误、`deps.go` 协作者接口、`limiter.go`，并补齐 `ChatRequest` 完整字段集。
- [x] 建立 `runtime/internal/runtimetest`：fake Runtime、fake Clock、fake WSDialer/WSConn。
- [x] 运行质量门禁全部命令。

补齐时发现并填的两处文档缺口（本节之前只引用了名字，没给出定义）：

- `Tool`/`ResponseFormat`（`ChatRequest` 用到但正文没有定义）：按 vLLM/SGLang/Ollama 共用的 OpenAI-compatible 工具调用与 `response_format` 形状补的，见 `types.go` 的 `Tool`/`FunctionDefinition`/`ResponseFormat`/`JSONSchemaFormat`。
- `ErrConcurrencyLimit` 对应的 `ErrorCode`：错误模型一节说“不占用上游错误码”，但 `ErrorCode` 常量块里没有一个符合的值；新增了 `ErrorBackpressure`，供 `limiter.go` 使用，并与「可观测性」一节里的 `backpressure` 指标结果标签对上。

`CapabilitySet` 的“同级冲突产生 Warning”目前实现为：冲突胜出的 `CapabilityEvidence.Detail` 写入冲突说明；把它搬进 `Discovery.Warnings` 是调用方（未来的适配器）的责任，`Merge` 本身签名未变，不返回独立的 warnings 列表。

**验收：** 配置校验、能力判断和并发控制在整个包内只有一份实现；适配器无需自行解析 URL 或推断能力。

### 阶段 2：共享 OpenAI-compatible 客户端

**文件：** `openai/client.go`、`models.go`、`chat.go`、`embedding.go`、`sse.go`、`stream.go`、`errors.go` 及对应测试。

- [x] 使用 `httptest.Server` 写 URL 前缀、鉴权脱敏、Context 取消、响应大小和错误体测试。
- [x] 写 `ChatRequest` 与线上 DTO 的双向转换测试：可选字段为 nil 时不出现在 JSON 中、显式 0 值原样传递、`Extra` 键与已建模字段冲突时返回 `ErrorInvalidConfig`。
- [x] 写 SSE 表驱动测试，覆盖 CRLF、多行 data、注释、空事件、`[DONE]`、畸形 JSON、超长行和中途断开。
- [x] 写流生命周期测试：空闲超时触发、首个事件后 `Committed=true`、`Close` 与 Context 取消都关闭响应体并使读取协程退出。
- [x] 运行 `go test ./common/runtime/openai`，确认失败原因分别来自未实现调用和解析逻辑。
- [x] 实现共享 Client、模型列表、Chat、Embedding 和 SSE Stream。
- [x] 运行 `go test -race ./common/runtime/openai`，要求全部通过。
- [x] 运行质量门禁全部命令。

实现说明：

- `Committed` 落地为 `Stream[T]` 接口新增的 `Committed() bool` 方法（`runtime/stream.go`），而不是某个具体事件类型上的字段——这样 ComfyUI 的 `WorkflowEvent` 流将来复用同一套“首次成功 Send 后即不可再透明重试”的语义，不必各自发明一套标记。`ChanStream[T]` 在 `Send` 成功那次调用内原子置位；`openai.chatEventStream` 通过内嵌 `*runtime.ChanStream[runtime.ChatEvent]` 自动获得该方法。
- `Extra` 冲突检测按**字段名集合**判断，不按“这次请求恰好序列化出了哪些 key”判断：`modeledChatFields` 是固定表，即使调用方没设置 `Temperature`，`Extra["temperature"]` 依然会被拒绝——避免把“字段当前未设置”当成绕过建模字段的后门。
- 流式请求固定发送 `stream_options.include_usage=true`，让支持该字段的后端在最后一个 chunk 带上 usage；不支持的后端会忽略这个字段（OpenAI-compatible 服务器对未知请求字段的通行做法是忽略而非报错）。

**交付物：** 三种 LLM Runtime 可组合该客户端，不再重复 HTTP、SSE 和错误处理。

**验收：** 该包不含任何 `Kind` 判断和后端专有端点路径；`grep` 不到 `vllm`、`sglang`、`ollama` 字样。

### 阶段 3：Registry、Manager 和健康状态机

**文件：** `registry.go`、`manager.go`、`registry_test.go`、`manager_test.go`。

- [x] 使用 fake Runtime 和 fake Clock 写注册冲突、未知 Kind、重复 ID、首次 Probe 失败、阈值转换、周期不重叠、原子替换和 Close 测试。
- [x] 写 Snapshot 隔离测试：修改返回值的切片和 `CapabilitySet` 不影响后续 Snapshot 结果。
- [x] 写调度测试：Health 慢于间隔时不堆积、抖动落在 `[interval, interval*1.1]`、健康恢复后立即触发一次 Discover。
- [x] 写 `Manager.Close` 测试：停止调度、取消在途检查、关闭全部实例、汇总多个 Close 错误、二次调用幂等。
- [x] 运行 `go test ./common/runtime -run 'Test(Registry|Manager)'`，确认失败。
- [x] 实现只读 Registry、并发安全 Manager、抖动调度和不可变 Snapshot。
- [x] 运行上述测试及 `go test -race ./common/runtime`，要求通过。
- [x] 运行质量门禁全部命令。

**交付物：** Agent 可以稳定持有多个异构 Runtime，并对上层发布可信状态。

**验收：** 实例替换过程中持续读取 Snapshot 不会看到不可用窗口；测试结束后无残留协程。

实现说明：

- `registry_test.go`、`manager_test.go` 按 README 约定写成黑盒测试（`package runtime_test`），因为它们需要 `internal/runtimetest`，而 `runtimetest` 反过来又 import `runtime` 做接口断言——放进包内测试会直接构成 import cycle。包内细节（SSE、URL 拼接等）仍留在 `package runtime`/`package openai`。
- `Manager.Add`/`Replace` 里 Probe 和 Discover 都在“进入实例表之前”同步执行；Discover 失败和 Probe 失败一样会 `Close` 掉新建的 Runtime 且不注册，不会遗留 `registering` 僵尸条目（状态图上 `registering → healthy` 那条边本身就要求两者都成功）。
- 每个实例一个调度协程，用 `select` 在同一个 goroutine 里轮流处理 Health 定时器、Discover 定时器和取消信号；Health/Discover 网络调用是同步执行的，天然保证“同一实例最多一个在途”，慢请求只是推迟下一次定时器的创建时间，不会排队堆积。
- 取消通过每实例的 `context.CancelFunc` 实现，Health/Discover 的调用 Context 都是这个 cancel context 的子 Context——`Remove`/`Replace`/`Close` 一 cancel，正在进行中的检查立刻收到 `ctx.Done()`，不必等它自然超时。
- `Snapshot.Inflight` 暂时恒为 `0`：README 没有说明上层如何把请求路径的 `Limiter` 获取/释放接回 Manager（`Get` 只返回裸 `Runtime`），这段留给阶段 8 的集成工作，此处不臆造一套接口。
- 按「质量门禁」要求补了 `runtime` 和 `openai` 两个包的 `TestMain`，统一做协程泄漏检测（`runtime.NumGoroutine()` 前后对比，容忍 2s 内自然收敛），不再逐个测试手写检查。

### 阶段 4：Ollama 适配器

**文件：** `ollama/runtime.go`、`ollama/profile.go`、`ollama/runtime_test.go`、`ollama/live_test.go`、`ollama/main_test.go`。

- [x] 用模拟 Ollama 覆盖 `/api/version`、`/api/tags`、`/api/show`、Chat SSE 和 Embedding。
- [x] 覆盖“只有 `/v1/models` 但没有 Ollama 原生端点”的类型不匹配场景。
- [x] 运行 `go test ./common/runtime/ollama`，确认失败。
- [x] 实现原生发现与 OpenAI-compatible 推理组合，限制 `/api/show` 并发；加入 `var _ runtime.InferenceRuntime = (*Runtime)(nil)` 编译期断言。
- [x] 运行包测试和 `go test -race ./common/runtime/...`。
- [x] 运行质量门禁全部命令。

实现说明：

- **模型能力列表按穷举处理。** Ollama 对一个模型返回的 `capabilities` 是完整列表，因此
  `ollama/runtime.go` 把本包能映射的每一项都写成明确的 `supported` 或 `unsupported`，不留
  `unknown`。这是「Embedding 仅对已确认模型上报」的落点：若缺席只记为 `unknown`，
  `Intersect` 会让运行时级的「`/v1/embeddings` 端点存在」透传给每个模型，聊天模型也会被
  当成可做 Embedding。Ollama 的 `insert`（FIM）没有对应的 `runtime.Capability`，直接忽略，
  不硬凑到相近能力上。
- **`/api/show` 兜底 + 按 digest 缓存。** 新版 Ollama 的 `/api/tags` 已内联 `capabilities`，
  此时不再发起任何 `/api/show`；旧版才逐个补齐，并发上限 `showConcurrency = 4`，结果以模型
  digest 为键缓存——digest 随模型内容变化，所以周期性 Discover 不会重复读取未变更的模型，
  这就是「限制刷新频率」的实现方式。单个 `/api/show` 失败只写入 `Discovery.Warnings` 并把
  该模型能力留为 `unknown`，不让一个模型的元数据问题拖垮整次发现。
- **探测失败的分类。** 原生端点返回 4xx（或响应体不含 `version`）判为 `ErrorProbeMismatch`
  ——有人应答，但不是 Ollama，覆盖「只有 `/v1/models`」的场景；连接失败和 5xx 保持原分类，
  否则 Manager 会把一台临时不健康的 Ollama 当成接错了后端而永久丢弃。
- **请求路径门禁读快照。** 能力判断只读最近一次 `Discover` 的原子快照：模型在快照中就用模型
  级交集结果，不在就退回运行时级结论；首次 `Discover` 成功前所有能力均为 `unknown`，请求会被
  拒绝。Manager 在实例可见前必定跑过 `Discover`，因此这条路径不会被正常调用命中。
- **`ChatStream` 持有并发额度直到 `Close`。** 流式请求的 `Limiter` 额度和请求级超时 Context
  都挂在返回的流上，由 `Close` 一次性释放（`sync.Once` 保证幂等）；这也是 `Stream` 文档要求
  调用方必须 `Close` 的原因。
- **`profile.go` 的版本下限偏保守。** 只声明官方 OpenAI 兼容文档明确列出的能力，且下限取得比
  实际首次支持的版本更高：判低了会把「没验证过」说成「支持」，判高了只是退化成 `unknown`，
  可由 `CapabilityOverrides` 补救。`vision`、`reasoning`、`parallel_tool_calls` 一律不进表——
  前两项按模型而定，由模型元数据给结论；第三项在 Ollama 的兼容层没有公开说法。
- **`live_test.go` 是唯一的真实后端契约测试。** 设 `OLLAMA_BASE_URL` 才运行，默认跳过，
  保证 `go test ./...` 不依赖本机是否装了 Ollama。已对 Ollama `0.32.14` 跑通 Probe、Health、
  Discover、Chat 和 SSE Chat（见「后端接入矩阵」的版本记录）。

**验收：** 能发现本地模型；普通及流式 Chat 可取消；Embedding 仅对已确认模型上报。

### 阶段 5：vLLM 适配器

**文件：** `vllm/runtime.go`、`vllm/profile.go`、`vllm/runtime_test.go`、`vllm/main_test.go`。

- [x] 模拟 `/version`、`/health`、`/v1/models`、Chat、Embedding 和 OpenAI 错误。
- [x] 覆盖 API Key、路径前缀、健康失败及版本字段缺失。
- [x] 运行 `go test ./common/runtime/vllm`，确认失败。
- [x] 实现适配器和 `profile.go` 保守能力表；高级能力默认 unknown；加入 `InferenceRuntime` 编译期断言。
- [x] 运行包测试和全目录竞态测试。
- [x] 运行质量门禁全部命令。

实现说明：

- **与 Ollama 适配器抽出了共用基座 `internal/oaibase`。** 阶段 4 完成时 `Chat`/`ChatStream`/
  `Embed`、并发限流、能力快照发布与门禁、`ErrorClosed`/`ProbeMismatch` 分类等逻辑已经在
  `ollama/runtime.go` 里写过一遍；vLLM 的这部分和 Ollama 完全相同（都基于共享的
  `openai` 传输层），照抄一份不算「适配自身协议差异」，所以先把它下沉到
  `internal/oaibase`，Ollama 适配器同步改为调用它。`internal/capprofile` 同理下沉了
  `Table.Resolve`/`ParseVersion`/`Compare`——版本区间怎么应用是通用的，具体版本表才是
  各适配器的知识。这两个包放在 `internal/` 而不是包内测试目录，是因为将来的
  SGLang 适配器同样需要它们，且不应进入公共 API。README「文件职责规划」表尚未列出这两个
  新文件，后续统一在阶段 6 完成后一并补齐，避免和阶段 5/6 的并行推进产生冲突。
- **不访问危险管理端点。** vLLM 除本适配器用到的五个端点外，还暴露 LoRA 适配器加载/卸载、
  `/sleep`、`/wake_up`，部分部署下还有 `/shutdown`。`TestAdapterTouchesOnlyTheFiveAllowedEndpoints`
  记录 Probe/Health/Discover/Chat/Embed 全流程实际请求过的路径集合，断言恰好是这五个，
  不是「没测试到就不算」。
- **`/version` 响应体缺 `version` 字段不算探测失败。** vLLM 该响应的字段名随版本变化过；
  只要路由本身应答（区别于 SGLang 等根本没有 `/version` 的后端），身份即视为已验证，
  版本记为空字符串。空版本会让能力表整体不适用，`Discover` 把这一点写进
  `Discovery.Warnings`，而不是静默地假设某个默认能力集合。
- **Embedding 默认 `unknown`，而非「vLLM 支持就上报支持」。** vLLM 一个进程只服务一个模型，
  `/v1/embeddings` 存在与否不说明加载的是不是 pooling/embedding 模型，因此
  `vllm/profile.go` 不声明这项能力，必须由运维通过 `Config.CapabilityOverrides` 按实例声明；
  `TestEmbedIsRefusedUntilDeclared` 同时断言未声明时请求不会打到后端。
- **`profile.go` 的版本下限同样偏保守**，取自 vLLM OpenAI-Compatible Server 文档明确写出
  该能力组合的版本，而非该能力最早出现的版本；判低了只退化为 `unknown`，可由
  `CapabilityOverrides` 补救。
- 本机没有可连的真实 vLLM 部署，阶段 5 未新增 `live_test.go`；版本兼容矩阵 vLLM 行保持
  「待填写」，等阶段 8 有真实后端时再补真实验证记录，不用文档推测代替。

**验收：** vLLM 身份、健康和模型可独立报告；不访问危险管理端点。

### 阶段 6：SGLang 适配器

**文件：** `sglang/runtime.go`、`sglang/profile.go`、`sglang/runtime_test.go`、`sglang/main_test.go`；补充 `internal/oaibase/oaibase_test.go`。

- [x] 模拟 `/health`、`/v1/models`、可选 `/get_server_info`、Chat SSE 和错误体。
- [x] 覆盖 `/health` 不存在时的降级、OpenAI 响应无法证明运行时身份及私有字段变化。
- [x] 运行 `go test ./common/runtime/sglang`，确认失败。
- [x] 实现显式 Kind 驱动的适配器，不接入 `/generate`；降级信息写入 `Snapshot.Degraded` 与 `Discovery.Warnings`；加入 `InferenceRuntime` 编译期断言。
- [x] 运行包测试和全目录竞态测试。
- [x] 运行质量门禁全部命令。

实现说明：

- **降级信息的落点是 `Discovery.Warnings`，`Snapshot.Degraded` 自动跟随。** `manager.go` 已经把
  `Discovery.Warnings` 复制进 `managedInstance.degraded` 并由 `Snapshot.Degraded` 暴露，所以
  适配器不需要新接口：`/health` 缺失时，每次 `Discover` 都会带上一条降级说明，且这条说明写清了
  退化后的检查「只能证明 HTTP 服务在，不能证明推理引擎在」——这正是运维需要知道的那半句。
- **`/health` 缺失在 Probe 阶段就探明，运行中掉线也能自愈。** Probe 额外打一次 `/health`，
  404 即记入 `ProbeResult.Evidence` 并切到降级路径；若实例中途被换成没有该路由的构建，
  `Health` 自己也会在收到 404 时切换，不会把「路由没了」误报成「实例死了」。降级标志不自动
  复位——一个消失过的路由不应在没有重新 Probe 的情况下悄悄变回健康信号。
- **5xx 不算降级。** `/health` 返回 500 说明路由在、引擎不健康，这是健康检查该报告的事；
  若把它也当成降级，就会把引擎的真实健康信号换成一个更弱的信号，故障反而被掩盖。
- **身份永远标记为未强验证。** `IdentityVerified` 恒为 `false`，`Evidence` 明写「kind 来自配置，
  OpenAI-compatible 响应无法区分 SGLang 与 vLLM」，让配置写错时看起来「已确认」的情况不存在。
- **核心能力用端点证据，不用版本表。** 这是本适配器与 vLLM 的一处有意不对称：vLLM 有文档化的
  `/version`，读不到版本本身就是异常，所以能力全留 `unknown`；SGLang 没有版本端点，
  「版本未知」是常态，若照搬 vLLM 的做法，每个实例开箱即不可用，必须先配 overrides。
  因此 `chat`/`chat_stream`/`completions` 由 `SourceEndpoint` 证据给出（依据：`/v1/models`
  应答，而 SGLang 的 OpenAI-compatible server 与该路由注册在同一个 server 上），
  `Detail` 里写明这条推断及其边界；真正随版本和启动参数变化的 `tools`、`structured_output`、
  `embeddings` 仍然只认版本表和 overrides。
- **`/get_server_info` 只取 `version`，且失败绝不影响 `Discover`。** 私有响应不是契约：
  路由不存在、返回 5xx、字段改名、`version` 变成对象、响应体根本不是 JSON——五种情况都只写
  一条 warning，模型列表照常发布（`TestDiscoverSurvivesPrivateEndpointChanges` 逐一覆盖）。
  该响应里的负载字段暂不上报，因为 `Discovery` 没有承载它的字段，`Snapshot.Inflight` 的
  接线本身也还留在阶段 8，此处不臆造一套接口。
- **不接入 `/generate`。** `TestAdapterNeverCallsTheNativeGenerateEndpoint` 记录
  Probe/Health/Discover/Chat/ChatStream 全流程实际请求过的路径集合并断言其精确取值，
  而不是只断言「没调用 /generate」。
- **补了 `internal/oaibase` 的直接测试。** 该包此前只被三个适配器间接覆盖；新增的黑盒测试
  针对 `ChatCapabilities`、`ErrorSummary`、`ConflictWarnings` 这三个纯逻辑导出函数，
  并补上与其他包一致的协程泄漏 `TestMain`。
- 本机没有可连的真实 SGLang 部署，阶段 6 未新增 `live_test.go`；版本兼容矩阵 SGLang 行保持
  「待填写」。

**验收：** SGLang 端点差异不会泄漏到共享 OpenAI 包，降级状态在 Discovery 中可见。
（前者已核对：`openai/`、`internal/oaibase/`、`internal/capprofile/` 三个共享包的非测试代码中
不存在任何 `Kind` 分支或后端专有端点路径，仅包注释里出现后端名称。）

### 阶段 7：ComfyUI 工作流适配器

**文件：** 修改 `go.mod`、`go.sum`；修改 `workflow/comfyui/client.go`、`workflow/comfyui/runtime.go`；创建 `workflow/comfyui/events.go`、`workflow/comfyui/wsdial.go`、`workflow/comfyui/client_test.go`、`workflow/comfyui/runtime_test.go`、`workflow/comfyui/main_test.go`；为重连测试给 `internal/runtimetest` 的假 `Clock` 增加 `PendingTimers`。

- [x] 用 HTTP 测试服务覆盖 system stats、features、object info、models、prompt、queue、history、view。
- [x] 用可控 WebSocket 服务覆盖连接先于提交、事件分发、未知事件、二进制预览、断线重连和 History 对账。
- [x] 写 pending 取消、exclusive running 取消和共享实例拒绝中断测试。
- [x] 运行 `go test ./common/runtime/workflow/comfyui`，确认失败。
- [x] 实现 Client、单连接事件复用器（`events.go`）、WorkflowRuntime 和安全取消；加入 `var _ runtime.WorkflowRuntime = (*Runtime)(nil)` 编译期断言。
- [x] 运行包测试、全目录竞态测试，并检查测试结束后无残留连接或协程。
- [x] 运行质量门禁全部命令。

实现说明：

- **依赖只加了 `github.com/coder/websocket` 一项**（v1.8.15），且只有 `wsdial.go` 一个文件引用它：
  其余代码一律面向 `runtime.WSDialer`/`WSConn` 接口，所以全部事件测试都不需要真实 WebSocket
  服务器。已确认该库的 `Dial` 上下文只约束握手、不绑定连接生命周期，因此 `Submit` 用带
  `RequestTimeout` 的 Context 建连不会在返回后被 `cancel()` 掐断。`go mod tidy` 会顺手删掉
  项目里尚未被引用的 `go-zero`，因此改回手写 `go.mod`，保留原有的 `go-zero // indirect` 行。
- **单连接复用是 ComfyUI 的强制形状。** 它的 WebSocket 是实例级而非任务级，一条连接混着所有
  任务的事件，每帧自带 `prompt_id`。因此每个 Runtime 只维护一条连接，按 `prompt_id` 路由；
  没有 `prompt_id` 的实例级事件（如 `status`）广播给全部订阅者，因为它描述的是所有人共同排的
  那个队列。
- **复用器绝不能被慢订阅者拖住。** 一个不读取的调用方会卡住整条连接、进而卡住所有其他任务，
  所以事件先进每订阅者 128 条的有界缓冲，由一条 pump 协程转投给流；缓冲满了就丢事件并计数，
  而不是阻塞复用器。丢事件是安全的：最终状态永远来自 History，不来自「是否收全了事件」。
  `TestASlowSubscriberDropsEventsInsteadOfStallingTheInstance` 覆盖这条。
- **首次重连立即执行，之后才退避。** 最常见的情况是单次断连，等待只会让所有订阅者白等；
  连续失败才走 500ms 起步、30s 封顶的退避，退避走注入的 `Clock`，
  `TestRepeatedReconnectFailuresBackOffInsteadOfSpinning` 用假时钟断言「退避没走完就不会再拨号」。
  重连期间丢失的事件不做补发，由 `Status` 读 History 对账。
- **取消的三条边界。** pending 任务按 id 从队列删除，无歧义；running 任务只有在
  `exclusive: true` 且队列确认「当前正在跑的就是它、且只有它一个」时才调 `/interrupt`——该路由
  不接受任何 id，在共享实例上会掐掉别人的任务。其余情况（共享实例、多个 running、已结束、
  查无此任务）一律返回 `ErrCancelUnsupported`，测试同时断言此时没有发出任何取消动作。
- **`/object_info` 用流式解析，只取键。** 该响应内嵌每个节点的完整输入 schema，动辄数十 MB；
  `decodeTopLevelKeys` 用 `json.Decoder` 逐 token 跳过值，不把整份文档读进内存，并有数量上限。
- **`Submit` 支持幂等键。** ComfyUI 自身没有幂等语义，重复提交会真实消耗 GPU 并产出第二份结果，
  所以 `IdempotencyKey` 到 `prompt_id` 的映射记在实例内存里，重试返回原任务；不带键则不去重。
  这与 README 风险表「禁止自动重提」一致：适配器自己永远不会重发提交。
- **`Status` 对「查无此任务」报 `failed` 而不是 `pending`。** ComfyUI 的 history 存在内存里，
  服务重启后任务凭空消失；报 pending 会让调用方永远等一个不存在的任务，因此返回 failed 并在
  `ErrorSummary` 里说明可能是服务重启。
- **新增 `Runtime.Artifacts`，是适配器扩展而非接口方法。** `WorkflowRuntime.OpenArtifact` 收
  `ArtifactRef` 却没有任何枚举入口，断线重连或 Agent 重启后调用方将无法找回自己的产物。
  该方法从 History 的 outputs 里提取产物引用，识别方式是「数组元素带 filename 字段」，
  而不是硬编码 `images`/`gifs` 这几个 key，这样自定义节点的输出也能被发现。是否提升为接口方法
  留给上层决定，目前通过类型断言使用。
- **两处测试先失败后修复的真实缺陷**：`json.Unmarshal(null, &map)` 不报错，导致 `null` 一度
  通过工作流模板校验（已改为先检查首字节是 `{`）；产物枚举按节点 id 字典序排列，
  `"12"` 排在 `"9"` 前面，已在文档和测试中明确这是稳定顺序而非执行顺序。

**验收：** 一个固定 API Format 工作流可以提交、观察进度、获取最终状态和流式下载产物；共享实例不会误取消其他任务。

### 阶段 8：集成、指标和契约测试

**文件：** 修改 `manager.go`、`service/aiServeWeaveAgent/main.go`、`service/aiServeWeaveAgent/README.md`；创建 `service/aiServeWeaveAgent/config.go`、`service/aiServeWeaveAgent/config_test.go` 和各适配器的 `_integration_test.go`。

- [ ] 注册四个默认 Factory，并从 Agent 配置加载多个 Runtime；`api_key_ref` 在配置层解析为内存值后传入。
- [ ] 加入指标和结构化日志，写 Secret 脱敏测试，断言日志中不出现 API Key、Authorization、Cookie 和完整 Prompt。
- [ ] 写跨适配器契约测试：同一组测试用例分别跑在三个 `InferenceRuntime` 实现上，验证接口语义一致（取消行为、能力门禁错误码、流终止错误）。
- [ ] 提供由环境变量启用的真实后端契约测试（如 `RUNTIME_E2E_VLLM_URL`）；未配置时明确 `t.Skip` 并说明所需变量。
- [ ] 运行 `go test -race ./common/runtime/...`。
- [ ] 运行 `go test ./service/aiServeWeaveAgent/...`，验证上层装配没有破坏编译。
- [ ] 分别连接真实 Ollama、vLLM、SGLang、ComfyUI 执行首期验收链路，并记录运行时版本。
- [ ] 用实测结果填写「版本兼容矩阵」，同步更新各 `profile.go` 与本 README 的接口草案。

**交付物：** Agent 可通过配置接入四种已启动后端，并向上层提供稳定、一致且可观测的运行时能力。

## 首期完成标准

- 四种 Runtime 均能从同一 Registry 创建，并由同一 Manager 管理。
- 配置错误、可判断的类型不匹配和鉴权失败在注册阶段给出明确错误；SGLang 仅能确认 OpenAI-compatible 契约时明确上报身份未强验证。
- 健康状态按阈值转换，慢检查不重叠，实例替换不产生不可用窗口。
- vLLM、SGLang、Ollama 的普通 Chat、SSE Chat 和模型列表通过契约测试。
- 支持的 Embedding 请求通过；未知或不支持能力在调用前被拒绝。
- SSE 能处理标准边界、取消、断流和超限，不聚合完整输出。
- ComfyUI 可以探测节点和模型、提交工作流、接收事件、查询历史并下载产物。
- ComfyUI 取消不会影响不属于当前调用的任务。
- 单实例并发达到上限时返回可识别的本地背压错误，不排队也不阻塞调用方。
- 「版本兼容矩阵」中四个后端的「已验证版本」均已由真实后端契约测试填写。
- 「质量门禁」全部命令通过，`gofmt -l` 无输出，`go vet` 无告警。
- 所有单元测试和 `go test -race ./common/runtime/...` 通过。
- 日志和错误中不存在 API Key、Authorization、Cookie 或完整 Prompt。
- 默认测试在没有 GPU 和外部服务的环境中可重复执行，且测试结束后无残留协程与连接。

## 风险与待决问题

| 风险 | 影响 | 缓解 | 状态 |
| --- | --- | --- | --- |
| SGLang 无法通过响应强验证身份 | 配置写错 `kind` 时，vLLM 与 SGLang 会互相误认 | `IdentityVerified=false` 并在 Snapshot 中显式暴露，由用户确认配置 | 已接受 |
| ComfyUI 事件可能丢失 | 进度事件不完整，最终状态误判 | 提交前建连，断线后强制 History 对账，最终状态只信 History | 已缓解 |
| ComfyUI `POST /prompt` 超时结果不确定 | 重复提交导致重复出图和资源浪费 | 以 `client_id` 加上层幂等键对账，禁止自动重提 | 需上层配合 |
| 后端版本升级改变字段或端点 | 能力表失效，Discover 静默降级 | 版本兼容矩阵 + `unknown` 默认值 + Warnings 上报 | 持续 |
| 共享 ComfyUI 实例的取消语义 | 可能中断其他调用者任务 | 非 `exclusive` 时对 running 任务直接拒绝取消 | 已接受 |
| `Extra` 透传后端私有参数 | 可能触达后端危险参数 | 与已建模字段冲突即报错；私有参数白名单由上层负责 | 待定 |

待决问题，需要在对应阶段前确认：

1. 各后端的最低支持版本，决定 `profile.go` 的下界与是否需要兼容分支（阶段 5 到 7 前）。
2. Agent 是否已有统一的 Metrics 与 Logger 抽象；若有则 `deps.go` 直接复用而非自定义接口（阶段 1b 前）。
3. `api_key_ref` 的 Secret 解析由哪一层实现，Runtime 只接收明文内存值这一约定是否与现有配置层一致（阶段 8 前）。
4. 地址白名单（禁止把 Runtime 变成任意 URL 代理）由 Runtime 层校验还是由控制面下发时保证（阶段 1b 前）。

## 后续演进

首期稳定后再独立规划：

1. OpenAI Responses、Completions、音频和 rerank 等更多能力。
2. Agent Tunnel 上的普通、SSE、WebSocket 和 Artifact 多路复用。
3. ComfyUI 工作流模板、输入绑定、Job 持久化和对象存储。
4. 运行时自动发现与配置建议，但仍由用户确认 Kind 和地址。
5. Managed Runtime：容器部署、模型分发、升级、排空和回滚。
6. 基于 GPU、队列、TTFT、吞吐和历史错误的跨节点调度。

这些能力不得提前塞入首期接口；确需扩展时优先新增窄接口，避免扩大基础 `Runtime`。

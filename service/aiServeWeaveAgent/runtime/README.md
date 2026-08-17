# Agent Runtime 接入规划

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**目标：** 在 Agent 中接入用户已经启动的 vLLM、SGLang、Ollama 和 ComfyUI，统一完成探测、健康检查、能力发现和请求代理。

**架构：** 四种后端共用运行时管理接口；vLLM、SGLang、Ollama 通过 OpenAI-compatible 专用接口处理同步及 SSE 推理，ComfyUI 通过独立工作流接口处理提交、事件、取消和产物。`Registry` 负责构造实例，`Manager` 负责实例状态与周期健康检查，各适配器只处理自身协议差异。

**技术栈：** Go 1.26、`net/http`、`encoding/json`、SSE、`context`、`httptest`，以及仅用于 ComfyUI 的 `github.com/coder/websocket`。

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

`runtime` 目录目前只有包和目录骨架，尚未实现公共接口或任何运行时能力。本文件描述目标设计和实施顺序，不代表对应功能已经完成。

已有文件布局：

```text
runtime/
├── runtime.go
├── types.go
├── capability.go
├── errors.go
├── stream.go
├── registry.go
├── manager.go
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

- `service/aiServeWeaveAgent/runtime/runtime.go`：`Runtime`、`InferenceRuntime`、`WorkflowRuntime`。
- `service/aiServeWeaveAgent/runtime/stream.go`：`Stream[T]` 及通用关闭语义。

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

### `service/aiServeWeaveAgent/runtime/types.go`

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

### `service/aiServeWeaveAgent/runtime/capability.go`

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

每项能力使用三态值，并记录以下来源之一：

- `endpoint`：目标端点实际存在且返回符合契约的响应。
- `model_metadata`：后端模型元数据明确声明。
- `runtime_profile`：已测试运行时版本的保守能力表。
- `config_override`：管理员显式覆盖，优先级最高。

禁止仅通过模型名字中包含 `vision`、`embed`、`tool` 等字样推断能力。

## 文件职责规划

| 文件 | 职责 |
| --- | --- |
| `runtime.go` | `Runtime`、`InferenceRuntime`、`WorkflowRuntime` 接口及编译期约束 |
| `types.go` | Kind、Config、Descriptor、Probe、Health、Discovery、模型和请求结果类型 |
| `capability.go` | 三态能力、证据来源、合并和覆盖规则 |
| `errors.go` | 错误分类、上游状态码映射、脱敏和 `errors.Is/As` 支持 |
| `stream.go` | 泛型流接口、关闭语义、空闲超时和终止错误 |
| `registry.go` | Factory 注册、重复检测、按 Kind 构造实例 |
| `manager.go` | 实例所有权、状态机、周期检查、快照和优雅关闭 |
| `openai/client.go` | 共享 HTTP 客户端、URL 拼接、鉴权头、大小限制和响应解码 |
| `openai/models.go` | `/v1/models` 类型和调用 |
| `openai/chat.go` | Chat Completions 普通请求及响应类型 |
| `openai/embedding.go` | Embeddings 请求及响应类型 |
| `openai/sse.go` | SSE 帧解析；支持多行 `data`、注释、空行和 `[DONE]` |
| `openai/stream.go` | SSE 到 `ChatEvent` 的转换及取消传播 |
| `openai/errors.go` | OpenAI-compatible 错误体解析与公共错误映射 |
| `vllm/runtime.go` | vLLM 探测、版本、健康、模型和能力修正 |
| `sglang/runtime.go` | SGLang 探测、健康、模型和能力修正 |
| `ollama/runtime.go` | Ollama 原生发现接口与 OpenAI-compatible 推理桥接 |
| `workflow/comfyui/client.go` | ComfyUI HTTP/WebSocket 客户端、请求和响应类型 |
| `workflow/comfyui/runtime.go` | WorkflowRuntime、事件复用、状态归一化和安全取消 |

测试与实现文件同目录放置，优先使用黑盒测试；只有协议解析细节使用包内测试。

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

建议默认值：探测和健康检查超时 `3s`，发现超时 `10s`，普通请求超时 `5m`，流空闲超时 `60s`。所有值可按实例覆盖；Context 截止时间始终优先。

## 后端接入矩阵

### vLLM

**实现文件：** `service/aiServeWeaveAgent/runtime/vllm/runtime.go`

| 动作 | 首选端点 | 说明 |
| --- | --- | --- |
| Probe | `GET /version`、`GET /v1/models` | 两者都符合契约才完成类型验证 |
| Health | `GET /health` | 仅检查引擎健康，不执行推理 |
| Discover | `GET /version`、`GET /v1/models` | 模型级高级能力仍需配置或版本能力表 |
| Chat | `POST /v1/chat/completions` | 普通响应和 SSE |
| Embedding | `POST /v1/embeddings` | 仅对明确支持的模型开放 |

vLLM 还可能暴露 Responses、音频、rerank 等端点，但首期不因端点出现在新版文档中就自动上报；每项能力必须有契约测试和明确版本证据。参考 [vLLM OpenAI-Compatible Server](https://docs.vllm.ai/en/latest/serving/online_serving/openai_compatible_server/) 与 [vLLM Security](https://docs.vllm.ai/en/latest/usage/security/)。

### SGLang

**实现文件：** `service/aiServeWeaveAgent/runtime/sglang/runtime.go`

| 动作 | 首选端点 | 说明 |
| --- | --- | --- |
| Probe | `GET /v1/models` | `kind` 来自配置；不能仅凭 OpenAI 响应与 vLLM 互相识别，结果必须标记身份未被强验证 |
| Health | `GET /health`；版本不兼容时退化为 `GET /v1/models` | 退化必须在状态快照中标记 |
| Discover | `GET /v1/models`；可选 `GET /get_server_info` | 私有响应只用于补充版本和负载，不进入公共模型契约 |
| Chat | `POST /v1/chat/completions` | 普通响应和 SSE |
| Embedding | 仅在适配器契约测试通过时启用 | 默认 `unknown` |

SGLang 同时提供原生 `/generate`，首期不接入，避免同时维护两套生成协议。参考 [SGLang Bench Serving Guide](https://docs.sglang.ai/developer_guide/bench_serving) 与 [SGLang Observability](https://docs.sglang.ai/advanced_features/observability.html)。

### Ollama

**实现文件：** `service/aiServeWeaveAgent/runtime/ollama/runtime.go`

| 动作 | 首选端点 | 说明 |
| --- | --- | --- |
| Probe | `GET /api/version`、`GET /api/tags` | 使用原生 API 确认 Ollama 身份 |
| Health | `GET /api/version` | 不加载模型，不执行推理 |
| Discover | `GET /api/tags`，按需 `POST /api/show` | `/api/show` 用于补充单模型能力，限制并发和刷新频率 |
| Chat | `POST /v1/chat/completions` | 复用共享 OpenAI-compatible 客户端 |
| Embedding | `POST /v1/embeddings` | 对外保持 OpenAI 语义 |

Ollama 原生接口用于身份和模型元数据，OpenAI-compatible 接口用于推理，避免自行转换其原生 NDJSON。不同版本支持的 OpenAI 字段不同，未验证字段保持 `unknown`。参考 [Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)、[List models](https://docs.ollama.com/api/tags) 和 [Get version](https://docs.ollama.com/api/version)。

### ComfyUI

**实现文件：**

- `service/aiServeWeaveAgent/runtime/workflow/comfyui/client.go`：HTTP/WebSocket 协议客户端和线上 DTO。
- `service/aiServeWeaveAgent/runtime/workflow/comfyui/runtime.go`：`WorkflowRuntime` 实现、事件分发和状态归一化。

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

**实现文件：** `service/aiServeWeaveAgent/runtime/registry.go`

```go
type Factory func(cfg Config, deps Dependencies) (Runtime, error)

type Registry interface {
	Register(kind Kind, factory Factory) error
	Create(cfg Config, deps Dependencies) (Runtime, error)
	Kinds() []Kind
}
```

- 默认 Registry 在启动阶段注册四个 Factory，运行阶段只读。
- 重复注册返回 `ErrFactoryAlreadyRegistered`。
- 未知类型返回 `ErrRuntimeKindUnsupported`。
- Factory 只构造客户端，不启动健康检查协程。
- `Dependencies` 注入 `http.Client`、WebSocket dialer、Clock、Logger 和 Metrics，测试不替换全局变量。
- ComfyUI WebSocket 使用 `github.com/coder/websocket`；适配器只依赖内部定义的窄 Dial/Conn 接口，生产实现再包装该库，便于测试连接、断线和取消。

### Manager

**实现文件：** `service/aiServeWeaveAgent/runtime/manager.go`

Manager 使用互斥锁保护实例表，但任何网络调用都不能持锁执行。读取方获得不可变 `Snapshot`，不能拿到 Manager 内部可修改对象。

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

- `service/aiServeWeaveAgent/runtime/errors.go`：公共错误类型和错误分类。
- `service/aiServeWeaveAgent/runtime/openai/errors.go`：OpenAI-compatible 错误响应解码。

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
- 每个 Runtime 使用独立并发上限；达到上限返回可识别的本地背压错误，不无限排队。
- 大响应、Artifact 和流式数据边读边传，禁止 `io.ReadAll` 无上限读取。

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

## 实施计划

### 阶段 1：公共契约和能力模型

**文件：** `runtime.go`、`types.go`、`capability.go`、`errors.go`、`stream.go` 及对应 `_test.go`。

- [x] 先写 Kind、Config 校验、能力三态合并、错误映射和 Stream 关闭语义测试。
- [x] 运行 `go test ./service/aiServeWeaveAgent/runtime -run 'Test(Config|Capability|RuntimeError|Stream)'`，确认测试因类型尚未实现而失败。
- [x] 实现本 README 中的公共类型和接口；增加四个适配器的编译期接口断言位置。（`Runtime`/`InferenceRuntime`/`WorkflowRuntime`、错误模型和 `ChanStream` 已实现；适配器编译期断言待各自阶段落地对应类型时加入。）
- [x] 再运行同一命令，要求通过且不存在协程泄漏。
- [x] 运行 `go test -race ./service/aiServeWeaveAgent/runtime`，要求通过。

尚未覆盖：Config 校验逻辑（`base_url`/`kind`/Header 规则）和 `CapabilitySet` 三态合并规则，留待接入 Registry（阶段 3）读取真实配置时一并实现和测试，避免在没有消费者的情况下预先猜测校验细节。

**交付物：** 上层可以只依赖 `runtime` 包完成实例分类、能力判断、错误判断和流读取。

### 阶段 2：共享 OpenAI-compatible 客户端

**文件：** `openai/client.go`、`models.go`、`chat.go`、`embedding.go`、`sse.go`、`stream.go`、`errors.go` 及对应测试。

- [ ] 使用 `httptest.Server` 写 URL 前缀、鉴权脱敏、Context 取消、响应大小和错误体测试。
- [ ] 写 SSE 表驱动测试，覆盖 CRLF、多行 data、注释、空事件、`[DONE]`、畸形 JSON、超长行和中途断开。
- [ ] 运行 `go test ./service/aiServeWeaveAgent/runtime/openai`，确认失败原因分别来自未实现调用和解析逻辑。
- [ ] 实现共享 Client、模型列表、Chat、Embedding 和 SSE Stream。
- [ ] 运行 `go test -race ./service/aiServeWeaveAgent/runtime/openai`，要求全部通过。

**交付物：** 三种 LLM Runtime 可组合该客户端，不再重复 HTTP、SSE 和错误处理。

### 阶段 3：Registry、Manager 和健康状态机

**文件：** `registry.go`、`manager.go`、`registry_test.go`、`manager_test.go`。

- [ ] 使用 fake Runtime 和 fake Clock 写注册冲突、未知 Kind、首次 Probe 失败、阈值转换、周期不重叠、原子替换和 Close 测试。
- [ ] 运行 `go test ./service/aiServeWeaveAgent/runtime -run 'Test(Registry|Manager)'`，确认失败。
- [ ] 实现只读 Registry、并发安全 Manager、抖动调度和不可变 Snapshot。
- [ ] 运行上述测试及 `go test -race ./service/aiServeWeaveAgent/runtime`，要求通过。

**交付物：** Agent 可以稳定持有多个异构 Runtime，并对上层发布可信状态。

### 阶段 4：Ollama 适配器

**文件：** `ollama/runtime.go`、`ollama/runtime_test.go`。

- [ ] 用模拟 Ollama 覆盖 `/api/version`、`/api/tags`、`/api/show`、Chat SSE 和 Embedding。
- [ ] 覆盖“只有 `/v1/models` 但没有 Ollama 原生端点”的类型不匹配场景。
- [ ] 运行 `go test ./service/aiServeWeaveAgent/runtime/ollama`，确认失败。
- [ ] 实现原生发现与 OpenAI-compatible 推理组合，限制 `/api/show` 并发。
- [ ] 运行包测试和 `go test -race ./service/aiServeWeaveAgent/runtime/...`。

**验收：** 能发现本地模型；普通及流式 Chat 可取消；Embedding 仅对已确认模型上报。

### 阶段 5：vLLM 适配器

**文件：** `vllm/runtime.go`、`vllm/runtime_test.go`。

- [ ] 模拟 `/version`、`/health`、`/v1/models`、Chat、Embedding 和 OpenAI 错误。
- [ ] 覆盖 API Key、路径前缀、健康失败及版本字段缺失。
- [ ] 运行 `go test ./service/aiServeWeaveAgent/runtime/vllm`，确认失败。
- [ ] 实现适配器和保守能力表；高级能力默认 unknown。
- [ ] 运行包测试和全目录竞态测试。

**验收：** vLLM 身份、健康和模型可独立报告；不访问危险管理端点。

### 阶段 6：SGLang 适配器

**文件：** `sglang/runtime.go`、`sglang/runtime_test.go`。

- [ ] 模拟 `/health`、`/v1/models`、可选 `/get_server_info`、Chat SSE 和错误体。
- [ ] 覆盖 `/health` 不存在时的降级、OpenAI 响应无法证明运行时身份及私有字段变化。
- [ ] 运行 `go test ./service/aiServeWeaveAgent/runtime/sglang`，确认失败。
- [ ] 实现显式 Kind 驱动的适配器，不接入 `/generate`。
- [ ] 运行包测试和全目录竞态测试。

**验收：** SGLang 端点差异不会泄漏到共享 OpenAI 包，降级状态在 Discovery 中可见。

### 阶段 7：ComfyUI 工作流适配器

**文件：** 修改 `go.mod`；创建 `go.sum`（若依赖解析后产生）；修改 `workflow/comfyui/client.go`、`workflow/comfyui/runtime.go`；创建对应 `_test.go`。

- [ ] 用 HTTP 测试服务覆盖 system stats、features、object info、models、prompt、queue、history、view。
- [ ] 用可控 WebSocket 服务覆盖连接先于提交、事件分发、未知事件、二进制预览、断线重连和 History 对账。
- [ ] 写 pending 取消、exclusive running 取消和共享实例拒绝中断测试。
- [ ] 运行 `go test ./service/aiServeWeaveAgent/runtime/workflow/comfyui`，确认失败。
- [ ] 实现 Client、单连接事件复用器、WorkflowRuntime 和安全取消。
- [ ] 运行包测试、全目录竞态测试，并检查测试结束后无残留连接或协程。

**验收：** 一个固定 API Format 工作流可以提交、观察进度、获取最终状态和流式下载产物；共享实例不会误取消其他任务。

### 阶段 8：集成、指标和契约测试

**文件：** 修改 `manager.go`、`service/aiServeWeaveAgent/main.go`、`service/aiServeWeaveAgent/README.md`；创建 `service/aiServeWeaveAgent/config.go`、`service/aiServeWeaveAgent/config_test.go` 和各适配器的 `_integration_test.go`。

- [ ] 注册四个默认 Factory，并从 Agent 配置加载多个 Runtime。
- [ ] 加入指标和结构化日志，写 Secret 脱敏测试。
- [ ] 提供由环境变量启用的真实后端契约测试；未配置时明确 Skip。
- [ ] 运行 `go test -race ./service/aiServeWeaveAgent/runtime/...`。
- [ ] 运行 `go test ./service/aiServeWeaveAgent/...`，验证上层装配没有破坏编译。
- [ ] 分别连接真实 Ollama、vLLM、SGLang、ComfyUI 执行首期验收链路，并记录运行时版本。

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
- 所有单元测试和 `go test -race ./service/aiServeWeaveAgent/runtime/...` 通过。
- 日志和错误中不存在 API Key、Authorization、Cookie 或完整 Prompt。
- 默认测试在没有 GPU 和外部服务的环境中可重复执行。

## 后续演进

首期稳定后再独立规划：

1. OpenAI Responses、Completions、音频和 rerank 等更多能力。
2. Agent Tunnel 上的普通、SSE、WebSocket 和 Artifact 多路复用。
3. ComfyUI 工作流模板、输入绑定、Job 持久化和对象存储。
4. 运行时自动发现与配置建议，但仍由用户确认 Kind 和地址。
5. Managed Runtime：容器部署、模型分发、升级、排空和回滚。
6. 基于 GPU、队列、TTFT、吞吐和历史错误的跨节点调度。

这些能力不得提前塞入首期接口；确需扩展时优先新增窄接口，避免扩大基础 `Runtime`。

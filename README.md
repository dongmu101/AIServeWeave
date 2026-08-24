# AIServeWeave

AIServeWeave 是一个分布式 AI 推理节点管理平台，为本地 Mac、内网设备和 NVIDIA GPU 服务器上的推理服务提供统一接入、管理、调度和对外 API。

项目目标是构建一套“AI 推理控制平面 + 兼容 API 网关”，让应用只对接一个稳定入口，而底层可以运行 Ollama、vLLM、ComfyUI 或其他 AI 推理服务。

## 项目目标

- 集中管理本地设备、内网设备和 GPU 服务器等推理节点
- 统一接入 Ollama、vLLM、ComfyUI 和其他推理后端
- 管理 ComfyUI 工作流、异步生成任务及图片、视频、音频等产物
- 对外提供 OpenAI、Anthropic 等兼容 API
- 支持普通响应和 SSE 流式响应
- 根据模型能力、节点状态、负载和策略自动调度请求
- 支持 API Key、租户、配额、限流、审计和用量统计
- 让没有公网入口的节点通过 Agent 主动连接平台

## 整体架构

AIServeWeave 分为控制面、数据面和节点面。项目初期采用模块化单体，避免过早拆分微服务；流量和团队规模增长后，再按模块独立部署。

```text
                         ┌─────────────────────┐
                         │  管理控制台 Console │
                         └──────────┬──────────┘
                                    │ Admin API
                         ┌──────────▼──────────┐
                         │  AIServeWeave       │
                         │                     │
                         │  控制面             │
                         │  - 用户与租户       │
                         │  - 节点与模型管理   │
                         │  - 路由与调度策略   │
                         │  - API Key 与配额   │
                         │                     │
Client ─ OpenAI/Claude ─►│  API Gateway        │
                         │  - 协议转换         │
                         │  - 鉴权与限流       │
                         │  - 调度与重试       │
                         │  - SSE 流转发       │
                         └──────┬────────┬─────┘
                                │        │
                      直接访问模式        │ 反向隧道
                                │        │
                  ┌─────────────▼─┐  ┌──▼───────────────┐
                  │ GPU Server    │  │ Mac / 内网设备   │
                  │ Agent         │  │ Agent            │
                  │               │  │                  │
                  │ vLLM / ComfyUI│  │ Ollama / MLX     │
                  │ TensorRT-LLM  │  │ ComfyUI          │
                  └───────────────┘  └──────────────────┘
```

### 控制面

负责低频管理操作：

- 用户、租户、权限和 API Key
- 节点注册、审批、禁用和维护
- 模型目录和实际部署管理
- 逻辑模型到实际部署的映射
- 调度、路由、配额和限流策略
- 用量、告警和审计记录

### 数据面

负责高频推理流量：

- 接收不同格式的公开 AI API
- 将请求转换成内部统一协议
- 选择满足要求的健康节点
- 转发普通请求和流式请求
- 收集 token、延迟和错误信息
- 执行超时、熔断和有限重试

### 节点面

由部署在算力机器上的 `aiserveweave-agent` 组成：

- 发现和连接本机推理服务
- 上报系统资源、GPU、模型和服务能力
- 维护节点心跳和状态
- 执行健康检查
- 为 NAT 后或没有公网入口的节点建立反向通道
- 运行 ComfyUI 工作流并回传进度和生成产物

## 核心组件

### aiserveweave-registry

注册与发现中心负责维护控制面的节点、后端、模型和能力状态：

- 接收 Agent 注册并签发或校验节点身份
- 维护节点心跳、健康状态和上下线事件
- 保存 Backend、Deployment、模型和能力信息
- 提供节点、模型和推理服务发现接口
- 管理节点标签、维护状态和租户可见范围
- 向 Gateway 提供可用 Deployment 快照或变更事件

如果后续加入用户、权限、部署策略和管理 API，可以将其扩展为 `aiserveweave-control-plane`，Registry 保持为内部模块。

### aiserveweave-gateway

统一 AI API 网关负责数据面流量：

- 提供 OpenAI、Anthropic 和工作流兼容 API
- 完成 API Key 鉴权、配额和限流
- 将外部协议转换成内部统一协议
- 根据 Registry 状态和路由策略选择 Deployment
- 通过 Direct 或 Tunnel 模式转发请求
- 处理 SSE 流式响应、超时、熔断和有限重试
- 记录请求用量、时延、状态和错误

### aiserveweave-agent

Agent 是部署在每台算力机器上的轻量 Go 程序。

主要职责：

- 使用一次性注册令牌加入平台
- 生成并安全保存节点身份
- 与 Registry 和 Tunnel 服务建立 mTLS 长连接
- 上报 CPU、内存、GPU、显存和操作系统信息
- 探测 Ollama、vLLM、ComfyUI 等本地后端
- 上报后端模型和能力列表
- 接收健康检查、配置同步等管理指令
- 代理 Gateway 无法直接访问的推理流量
- 代理 ComfyUI WebSocket 事件和生成文件传输

节点接入支持两种模式：

| 模式 | 适用场景 | 请求路径 |
| --- | --- | --- |
| Direct | 有内网或公网可达地址的 GPU 服务器 | Gateway 直接调用节点推理服务 |
| Tunnel | 家庭 Mac、办公网或 NAT 后节点 | Agent 主动连接 AIServeWeave，请求通过隧道转发 |

MVP 可以使用 gRPC 双向流实现 Tunnel。流量规模增大后，可将 Tunnel Hub 独立部署，并评估 HTTP/2 或 QUIC 多路复用。

### aiserveweave-console

管理控制台建议包含：

- 节点列表、在线状态和节点详情
- CPU、内存、GPU、显存和当前负载
- 节点后端、模型和能力
- ComfyUI 工作流模板、节点依赖、任务队列和生成产物
- 逻辑模型与实际部署映射
- 路由、权重和优先级策略
- 用户、租户、API Key 和配额
- 请求日志、Token 用量和错误记录
- 延迟、吞吐、成功率和告警
- 管理操作审计日志

## 协议兼容

外部协议只存在于系统边界。所有请求先转换成内部统一请求，再进入调度器；选定节点后，再由后端适配器转换成目标服务协议。

```text
OpenAI Request ────┐
Anthropic Request ─┼─► Protocol Adapter ─► Canonical Request
Ollama Request ────┘                         │
                                             ▼
                                         Scheduler
                                             │
                                             ▼
                                      Backend Adapter
                                 ┌────────┬──────────┐
                                 ▼        ▼          ▼
                               vLLM     Ollama     Other
```

内部请求可以抽象为：

```go
type InferRequest struct {
	RequestID string
	TenantID  string
	Model     string
	Messages  []Message
	Tools     []Tool
	Sampling  SamplingOptions
	Stream    bool
	Metadata  map[string]string
}
```

流式和非流式结果统一成事件：

```go
type InferEvent struct {
	Type  EventType
	Delta *ContentDelta
	Usage *Usage
	Error *InferError
}
```

第一阶段优先支持：

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/embeddings`
- `GET /v1/models`

后续增加：

- Anthropic `POST /v1/messages`
- Ollama 原生 API
- 音频转录和翻译
- rerank
- OpenAI-compatible 图像生成
- ComfyUI 工作流和异步任务 API

vLLM 已提供 Chat Completions、Responses、Embeddings 和音频等多种 OpenAI-compatible API；Ollama 也提供部分 OpenAI API 兼容能力。因此，第一版以 OpenAI 协议作为主要对外协议和后端协议，可以减少适配成本。

- [vLLM OpenAI-Compatible Server](https://docs.vllm.ai/en/latest/serving/openai_compatible_server/)
- [Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)

兼容不代表所有参数和行为完全相同。AIServeWeave 必须维护每个实际部署的能力矩阵：

```text
DeploymentCapability
├── chat
├── responses
├── embeddings
├── image_generation
├── workflow_execution
├── vision
├── audio
├── video
├── tools
├── parallel_tool_calls
├── structured_output
├── reasoning
├── max_context_length
└── streaming
```

如果目标部署不支持请求所需能力，调度器应选择其他部署或返回明确错误，不能静默丢弃参数。

## ComfyUI 接入与部署

ComfyUI 是基于节点图的生成式 AI 推理引擎，可生成图片、视频、音频等内容。它的执行方式是提交整个工作流并异步排队，不应强行套用 LLM 的同步请求模型。

AIServeWeave 将 ComfyUI 视为一种独立 Backend，并同时支持：

- 节点上自托管的 ComfyUI
- 通过 Direct 模式访问的 ComfyUI 服务器
- 通过 Agent Tunnel 访问的本地或内网 ComfyUI
- 可选的 Comfy Cloud Backend

接入分为两级：

1. External：用户已经启动 ComfyUI，Agent 只负责探测、注册和代理。
2. Managed：平台下发声明式部署配置，由 Agent 负责安装或启动、健康检查、停止和升级。

MVP 优先实现 External，确认工作流链路稳定后再实现 Managed，避免第一版同时承担 Python、CUDA、模型和自定义节点的复杂依赖管理。

### ComfyUI 部署模式

```text
                       ┌─────────────────────┐
Client ───────────────►│ AIServeWeave Job API   │
                       └──────────┬──────────┘
                                  │ 选择 Workflow + Deployment
                       ┌──────────▼──────────┐
                       │ ComfyUI Job Manager │
                       └──────┬────────┬─────┘
                              │        │
                         Direct        │ Tunnel
                              │        │
                  ┌───────────▼──┐  ┌──▼──────────────┐
                  │ GPU ComfyUI  │  │ Mac / 内网 Agent│
                  │ :8188        │  │ → ComfyUI :8188 │
                  └──────────────┘  └─────────────────┘
```

Agent 对 ComfyUI 的接入职责：

- 通过 `/system_stats` 获取设备和显存信息
- 通过 `/object_info` 获取可用节点类型及其输入输出定义
- 通过 `/models` 和 `/models/{folder}` 获取模型分类并同步可用模型
- 通过 `/prompt` 提交 API Format 工作流
- 通过 `/ws` 接收排队、执行节点、采样进度和错误事件
- 通过 `/history/{prompt_id}` 查询最终执行结果
- 通过 `/view` 拉取图片、视频、音频等生成产物
- 通过 `/queue` 和 `/interrupt` 管理排队或执行中的任务

这些接口来自 ComfyUI 本地 Server API。工作流必须使用 ComfyUI 导出的 API Format，而不是只包含前端布局信息的普通工作流文件。

- [ComfyUI Server 路由](https://docs.comfy.org/development/comfyui-server/comms_routes)
- [ComfyUI WebSocket 消息](https://docs.comfy.org/development/comfyui-server/comms_messages)
- [ComfyUI Cloud API](https://docs.comfy.org/development/cloud/overview)

### 托管部署

Managed 模式使用声明式部署规格，控制面保存期望状态，Agent 负责将本机实际状态收敛到期望状态：

```yaml
kind: ComfyUIDeployment
metadata:
  name: comfy-gpu-01
spec:
  runtime: docker
  image: ghcr.io/example/comfyui:<pinned-version>
  listenAddress: 127.0.0.1
  port: 8188
  gpuDevices: ["0"]
  modelPaths:
    checkpoints: /models/checkpoints
    loras: /models/loras
    vae: /models/vae
  storage:
    input: /data/input
    output: /data/output
  customNodes:
    policy: allowlist
  resources:
    memoryLimit: 32Gi
  healthCheck:
    interval: 15s
    timeout: 5s
```

首个 Managed 实现建议只支持 Linux NVIDIA GPU 上的 Docker/容器运行时；Mac 上先接入用户已经安装和启动的 ComfyUI。后续再评估 macOS 原生 Python 环境或 ComfyUI Desktop 的生命周期管理。

部署控制应包含：

- 创建、启动、停止、重启和删除 ComfyUI 实例
- 固定镜像或版本，不自动追踪 `latest`
- GPU、端口、模型目录和输入输出目录配置
- 环境变量只引用平台 Secret，不在部署规格中保存明文
- 启动后依次检查 `/system_stats`、`/object_info` 和 WebSocket
- 上报 `pending`、`installing`、`starting`、`ready`、`degraded`、`stopped` 和 `failed` 状态
- 升级前检查正在运行的 Job，默认等待排空后再滚动重启
- 自定义节点采用允许列表并固定版本，安装动作写入审计日志

模型文件通常很大，MVP 不负责自动下载。Managed 模式先挂载用户准备好的共享模型目录；后续再增加带校验和、断点续传、磁盘配额和来源白名单的模型分发能力。

### 工作流模板

平台不应允许普通 API 调用者随意修改整个节点图。推荐由管理员注册受控的工作流模板，并只暴露经过声明的输入：

```text
WorkflowTemplate: flux-text-to-image
├── workflow_api_json
├── required_node_types
├── required_models
├── inputs
│   ├── prompt       → node 6 / text
│   ├── negative     → node 7 / text
│   ├── width        → node 5 / width
│   ├── height       → node 5 / height
│   ├── steps        → node 3 / steps
│   └── seed         → node 3 / seed
└── outputs
    └── images       ← node 9
```

调度前需要校验目标 Deployment 是否拥有模板要求的模型和节点类型。自定义节点必须记录名称和版本；缺少节点、模型不匹配或工作流校验失败时，应在进入队列前返回明确错误。

### ComfyUI 任务 API

AIServeWeave 对外提供统一的异步 Job API：

```text
POST   /v1/workflows/{workflow_id}/runs     提交工作流
GET    /v1/jobs/{job_id}                    查询任务状态
GET    /v1/jobs/{job_id}/events             获取 SSE 进度事件
POST   /v1/jobs/{job_id}/cancel             取消任务
GET    /v1/jobs/{job_id}/artifacts          获取产物列表
GET    /v1/artifacts/{artifact_id}           下载生成产物
```

统一任务状态：

```text
queued → running → succeeded
                 ├→ failed
                 ├→ cancelled
                 └→ timed_out
```

ComfyUI 的 `prompt_id` 是后端任务 ID，不能直接作为公开 ID。AIServeWeave 应生成自己的 `job_id`，保存二者映射，并使用租户权限保护状态、事件和产物。

对于简单的文生图场景，可以把 OpenAI-compatible `POST /v1/images/generations` 映射到管理员指定的 ComfyUI 工作流模板。复杂工作流仍使用 AIServeWeave Workflow API，以免丢失 ComfyUI 的图结构、视频输出和自定义参数能力。

### 文件与产物

ComfyUI 支持输入文件和较大的生成产物，文件流不应直接存入 PostgreSQL：

- 输入图片先上传到 AIServeWeave，再由 Agent 上传到目标 ComfyUI
- 生成完成后，Agent 从 ComfyUI 拉取产物并上传到对象存储
- PostgreSQL 只保存文件元数据、哈希、大小、租户和存储位置
- MVP 可使用本地文件存储，生产环境建议使用 S3-compatible 对象存储
- 下载接口使用短期签名 URL 或经过鉴权的流式代理
- 为输入文件、预览图和最终产物设置大小、格式和保留期限限制

### ComfyUI 调度

ComfyUI 调度除通用节点状态外，还应考虑：

- 工作流所需 checkpoint、LoRA、VAE 和 ControlNet 是否存在
- Core Node 和 Custom Node 是否可用、版本是否兼容
- 节点当前队列长度和预计等待时间
- GPU 显存、分辨率、批量大小和历史 OOM 情况
- 工作流类型，例如图片、视频、音频或 3D
- 租户是否有权使用指定工作流和模型

ComfyUI 作业一旦开始执行，不应自动迁移到其他节点。只有仍处于 AIServeWeave 队列且尚未提交到 ComfyUI 的任务，才可以重新调度。

## 模型与部署抽象

客户端使用逻辑模型名，而不是直接指定节点上的真实模型：

```text
客户端请求：model = "qwen-coder"

qwen-coder
├── mac-mini-01 / Ollama / qwen3-coder:30b
├── gpu-server-01 / vLLM / Qwen/Qwen3-Coder
└── gpu-server-02 / vLLM / Qwen/Qwen3-Coder-FP8
```

核心对象：

| 对象 | 含义 |
| --- | --- |
| Node | Mac、工作站或 GPU 服务器 |
| Backend | 节点上的推理服务，如 Ollama、vLLM 或 ComfyUI |
| Deployment | 某个 Backend 中运行的实际模型实例 |
| Model | 暴露给客户端的逻辑模型 |
| Route | Model 到一个或多个 Deployment 的选择规则 |

这种抽象允许在不影响客户端的情况下更换底层模型、量化版本、节点或推理框架。

## 调度流程

```text
接收请求
  → 验证租户、API Key 和配额
  → 解析逻辑模型
  → 找到候选部署
  → 过滤离线、维护和熔断节点
  → 过滤能力不匹配节点
  → 检查租户和节点访问权限
  → 按优先级、负载、延迟和成本评分
  → 选择目标部署
  → 转发请求
  → 记录结果和用量
```

初期建议实现以下策略：

- 加权轮询
- 最少正在执行请求
- 节点优先级
- 节点标签路由，例如 `region=local`
- 租户绑定节点或节点池
- 会话亲和，以便复用 KV Cache
- 连续失败熔断和自动恢复

流式请求只有在返回第一个 token 之前可以安全重试。一旦已经向客户端发送内容，不应自动切换节点，否则可能产生重复或不连续的输出。

## 数据模型

初期可以使用 PostgreSQL 保存控制面数据：

```text
tenants
users
api_keys

nodes
node_credentials
node_heartbeats
node_labels

backends
models
deployments
deployment_capabilities
deployment_revisions
deployment_status

routes
route_targets

workflow_templates
workflow_versions
workflow_requirements
jobs
job_events
artifacts

inference_requests
usage_records
audit_logs
```

主要关系：

- 一个 Node 可以运行多个 Backend
- 一个 Backend 可以运行多个 Deployment
- Managed Deployment 使用 Revision 保存声明式配置，并分别记录期望状态和实际状态
- 一个 Model 可以通过 Route 指向多个 Deployment
- Route Target 保存权重、优先级和匹配条件
- 一个 Workflow Template 可以有多个不可变版本
- Job 保存公开任务 ID、ComfyUI `prompt_id` 和实际 Deployment 的映射
- Artifact 保存输入文件、预览图和最终生成文件的元数据

请求明细和时序指标不应无限写入 PostgreSQL。MVP 可以只保存请求摘要；后续将指标发送到 Prometheus，将高容量日志发送到 ClickHouse 或 Loki。

## 安全设计

安全能力应从第一版开始建设：

- 用户 API Key 只保存不可逆哈希
- Agent 使用短期注册令牌换取节点证书
- Agent 与 Registry、Gateway 和 Tunnel 服务之间使用 mTLS
- 推理后端密钥加密保存
- 用户、租户、模型和节点权限隔离
- 所有管理操作写入审计日志
- 限制 Agent 可以访问和代理的目标地址
- 校验后端 URL，防止 SSRF
- 限制请求体大小、上下文长度、超时和并发数
- 校验 ComfyUI 工作流节点和公开输入，禁止未经授权的任意节点执行
- 隔离 ComfyUI 自定义节点及其外部网络访问权限
- 对上传文件执行类型、大小、文件名和恶意内容检查
- 日志对 Authorization、Cookie 和密钥进行脱敏
- 默认不保存 prompt 和生成内容，只记录必要元数据

## 可观测性

建议从一开始接入 OpenTelemetry，并提供 Prometheus 指标：

- 节点在线数量
- 节点心跳延迟
- 模型部署健康状态
- 请求量和并发数
- 首 token 延迟（TTFT）
- 总响应时间
- 输入和输出 token
- 每秒输出 token
- 后端错误率和超时率
- 调度选择结果
- Tunnel 吞吐和连接状态
- ComfyUI 队列长度、任务等待时间和执行时间
- ComfyUI 工作流成功率、节点错误和 GPU OOM 次数
- Artifact 上传、下载、大小和存储使用量

每个请求应携带同一个 `request_id`，贯穿 Gateway、Scheduler、Tunnel、Agent 和推理后端。

## 代码结构

项目按服务分目录，每个服务一个顶层包，服务内部再按职责分子包。当前实际结构：

```text
AIServeWeave/
├── api/
│   └── proto/tunnel/v1/        # Agent/Gateway/Registry 共享的 gRPC 契约
├── common/                     # 跨服务共享代码
│   ├── runtime/                # 推理后端抽象：能力探测、配额、流式转换
│   │   ├── internal/           # 包内私有工具与测试辅助
│   │   ├── ollama/  openai/  sglang/  vllm/
│   │   └── workflow/comfyui/
│   └── tunnelwire/             # 隧道 proto 编解码，Agent 与 Gateway 共用
├── deploy/
│   └── docker-compose.yaml
└── service/
    ├── aiServeWeaveAgent/      # 节点面，已有主要实现
    │   ├── tunnel/             # 主动出站隧道，见该目录 README
    │   └── workflow/           # ComfyUI 工作流清单、绑定与校验
    ├── aiServeWeaveGateway/    # 数据面，隧道服务端、调度器与 OpenAI 前门已落地
    │   ├── tunnelserver/       # 隧道终结：节点表、槽池、九个 Operation 的分发
    │   └── e2e/                # 真实 mTLS 下 Agent 与 Gateway 的联调测试
    ├── aiServeWeaveRegistry/   # 控制面注册中心，仅骨架
    ├── aiServeWeaveControlPlane/  # 尚未开始
    └── aiServeWeaveConsole/       # 尚未开始
```

后续随功能推进补齐的目录（当前尚不存在）：`api/openapi/`、`migrations/`、`configs/`、`web/`、`docs/`、`deploy/kubernetes/`。

调度器、鉴权、用量统计等模块目前尚无归属目录，落地时按所属服务放进 `service/<服务名>/` 下的子包；确实被多个服务共用的再上提到 `common/`。

`common/` 下已有两个包，都是因为 Gateway 与 Agent 必须按同一套规则解释同一份数据才上提的：

- `common/runtime` —— 推理语义的类型与接口（`Stream`、`RuntimeError`、九个 Operation 的请求响应类型）。Agent 用它实现后端适配器，Gateway 用它表达调度器和 API 层看到的请求，两边共用一份定义而不是各自复述。
- `common/tunnelwire` —— 这些类型与 `api/proto/tunnel/v1` 之间的双向编解码。隧道两端都要做这次转换，放在一个包里意味着「凭据不过隧道」「nil 与显式零值不等价」这两条不变量只有一处实现、一处测试。

模块路径使用短名 `module AIServeWeave`，包内互相引用一律以此为前缀，例如 `AIServeWeave/api/proto/tunnel/v1`。

## 开发路线

### 第一阶段：最小闭环

- Registry、Gateway 和 Agent 建立安全连接
- 节点注册、心跳和上下线状态
- Agent 手动配置 Ollama 或 vLLM 地址
- Agent 手动配置并探测 ComfyUI 地址
- 同步模型列表和基础能力
- OpenAI Chat Completions API
- SSE 流式转发
- 逻辑模型到多个节点的路由
- API Key 鉴权
- 基础请求和错误日志
- 提交一个固定的 ComfyUI 文生图工作流
- 查询 ComfyUI Job 状态并下载生成图片

完成后的最小链路：

```text
OpenAI SDK → AIServeWeave → Mac Ollama / Server vLLM

Workflow API → AIServeWeave → Agent → ComfyUI → Artifact
```

### 第二阶段：可用平台

- Web 管理控制台
- 模型别名和节点标签
- 配额、并发限制和速率限制
- Responses 和 Embeddings API
- 健康检查、熔断和恢复
- 自动发现 Ollama 和 vLLM
- ComfyUI 工作流模板和版本管理
- Linux NVIDIA GPU 上的 ComfyUI Managed Docker 部署
- ComfyUI SSE 任务进度、取消和错误展示
- 输入文件上传和 S3-compatible 产物存储
- Token、延迟和吞吐统计
- Prometheus 和 OpenTelemetry
- Docker Compose 部署

### 第三阶段：生产能力

- 多租户 RBAC
- Anthropic Messages API
- OpenAI-compatible Image Generation 到 ComfyUI 模板的映射
- ComfyUI 自定义节点、模型依赖和版本兼容检查
- ComfyUI 安全升级、任务排空和模型分发
- 图片、视频、音频和 3D 等多类型产物管理
- GPU 指标和资源感知调度
- 请求排队和背压
- Agent 自动升级
- Kubernetes 部署
- Registry 和 Gateway 高可用
- 独立 Tunnel Gateway
- Redis、NATS 或其他事件基础设施
- 审计、告警和计费

## MVP 优先验证

项目首先应验证以下完整链路：

```text
OpenAI 流式请求
  → Gateway 协议解析
  → Scheduler 选择部署
  → NAT 后的 Mac Agent
  → Ollama
  → SSE 流式返回客户端
```

这条链路同时覆盖协议转换、模型路由、反向连接和流式传输，是 AIServeWeave 最关键的技术闭环。闭环稳定后，再开发完整控制台、更多协议和复杂调度策略。

ComfyUI 接入应同时验证一条异步生成链路：

```text
上传输入文件或提交模板参数
  → 创建 AIServeWeave Job
  → 根据模型、节点类型和显存选择 ComfyUI Deployment
  → Agent 提交 API Format 工作流
  → WebSocket 进度转换为 AIServeWeave Job Event
  → 拉取生成文件并保存为 Artifact
  → 客户端查询或下载结果
```

## 当前状态

节点与节点到 Gateway 的链路已经打通，MVP 优先验证链路（OpenAI 流式请求 → Gateway → Scheduler → Agent → Ollama → SSE 返回）已经用真实机器跑通一次；Registry 的 `NodeIdentity` 与 `GatewayDirectory` 也已落地，第一阶段的两个缺口都已补上。第二阶段已开始：调度器的健康过滤与熔断先落地了。已完成的部分：

1. Registry、Gateway 与 Agent 之间的 protobuf 协议（`api/proto/tunnel/v1`，三边共用）。
2. `common/runtime`：推理后端抽象——实例管理、健康状态机、能力发现与并发限流，以及 vLLM、SGLang、Ollama、ComfyUI 四个适配器。
3. `common/tunnelwire`：`runtime` 类型与隧道 proto 的双向编解码，隧道两端共用一份。
4. Agent 的隧道客户端：节点身份与证书轮换、Control 流与心跳、槽池与九个 Operation 的分发、多副本连接表与名册处理、隧道指标与压测。
5. Gateway 的隧道服务端：节点表、槽池、九个 Operation 的分发，以及把隧道对面呈现为一个 `runtime.InferenceRuntime` 的 `NodeRuntime`。
6. 三副本端到端联调：真实 TCP、真实 mTLS，每个副本独立完成推理，请求路径上无副本间转发。
7. Gateway 的调度器（`service/aiServeWeaveGateway/scheduler`）：按模型与能力选节点，处理背压与重试语义——流式请求只在返回第一个 token 之前重试。
8. Gateway 的 OpenAI 前门（`service/aiServeWeaveGateway/httpapi`）：`POST /v1/chat/completions`（含 SSE 流式）、`POST /v1/embeddings`、`GET /v1/models`，静态 API Key 鉴权。`POST /v1/responses` 未做——`common/runtime` 和隧道协议都没有对应的类型/Operation，需要先扩协议，留给后续。
9. 真实端到端：本机 Ollama + 真实 mTLS 隧道 + Gateway HTTP 前门，非流式与 SSE 流式 Chat 都跑通，流式 TTFT 实测在百毫秒量级，由 Ollama 推理时延主导，隧道与前门本身开销可忽略。
10. Registry 的 `NodeIdentity` 服务（`service/aiServeWeaveRegistry`）：自建 CA、一次性 bootstrap token 的铸造与校验、节点证书签发与续期。用 Agent 现有的 `tunnel.IdentityManager` 当客户端直接对着真实 Registry 跑通了完整流程（不是自造假客户端）。
11. Registry 的 `GatewayDirectory` 服务与 Gateway 侧的 `registryclient`：Gateway 副本向 Registry 报到、收到名册变化即转发给 `tunnelserver.Server.SetRoster`，断线按全抖动退避重连，优雅关闭前先广播 `DRAINING`。此前 `SetRoster` 一直是等着调用方的手工注入点，现在有了真正的调用方。
12. Gateway 调度器的健康检查、熔断与恢复（第二阶段第一项）：`candidates()` 现在会排除 Agent 上报为 `unhealthy`/`closed` 的 runtime 实例，并按 `(node_id, runtime_id)` 维护一个熔断器——`connection_failed`/`timeout`/`upstream_error` 连续失败达到阈值后该候选被临时排除，冷却后自动探测恢复；`backpressure`/`rate_limited` 明确不计入，这两个是"忙"不是"坏"。阈值是未经真实流量验证的初始默认值，详见 [service/aiServeWeaveGateway/README.md「健康过滤与熔断」](service/aiServeWeaveGateway/README.md#健康过滤与熔断)。
13. Agent 自动发现本机 Ollama/vLLM（第二阶段第二项，`service/aiServeWeaveAgent/localdiscovery`）：启动即探测 `127.0.0.1` 上 Ollama（11434）与 vLLM（8000）的默认端口，答上的直接注册进 `runtime.Manager`，之后每 30 秒（`-auto-discover-interval` 可调）重新扫一遍还没发现的候选，好让"先起 Agent 再起 Ollama"这种顺序也能用。刻意只探测本机回环地址，不做局域网扫描或 mDNS；已经手动用 `-ollama-url` 配置过的地址会被天然跳过（按地址去重，不是按 ID）；发现后的健康跟踪完全交给 `runtime.Manager` 已有的探测循环，发现器自己不留状态、不做摘除。`-auto-discover=false` 可以整体关掉。

    隧道两侧的进度与设计见 [service/aiServeWeaveAgent/tunnel/README.md](service/aiServeWeaveAgent/tunnel/README.md)（阶段 7 有详细实测数据）；Registry 的存储布局、`-mint-token` 用法与已知限制见 [service/aiServeWeaveRegistry/README.md](service/aiServeWeaveRegistry/README.md)。

下一步建议先实现：

1. Gateway 侧的隧道指标：Agent 侧的 13 个隧道指标已有实现可参照，服务端侧还没有对应的一份。
2. `node_id` 冲突检测与运维口径：Registry 目前对非空 `node_id` 直接采信，不检测跨节点冲突，上线前需要定下这个口径（隧道 README「待决问题 3」）。
3. 故障注入剩余场景与滚动升级演练已在单机完成（拔网线、kill 全部副本、证书过期、后端假死、逐副本滚动升级，见 `service/aiServeWeaveAgent/tunnel/README.md` 阶段 7）；24h 长稳测试工具已落地并在本机运行中，完成后补数据。
4. 实现 Job 状态持久化、取消任务和生成图片下载。
5. 熔断阈值（`FailureThreshold`/`BaseCooldown`/`MaxCooldown`）需要真实流量数据校准；熔断状态目前也没有指标暴露出来，等 Gateway 侧隧道指标落地时应该一并加上。

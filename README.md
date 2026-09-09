# AIServeWeave

AIServeWeave 是一个分布式 AI 推理节点管理平台，为本地 Mac、内网设备和 NVIDIA GPU 服务器上的推理服务提供统一接入、管理、调度和对外 API。

项目目标是构建一套“AI 推理控制平面 + 兼容 API 网关”，让应用只对接一个稳定入口，而底层可以运行 Ollama、vLLM、ComfyUI 或其他 AI 推理服务。

本文描述项目定位、能力规划与目标架构，包含尚未实现的设计，不作为功能可用性声明。开发进度、优先级、依赖和验收统一见 [STATUS.md](STATUS.md)。部署操作见 [deploy/README.md](deploy/README.md)，具体接口与运行限制见各服务 README。

## 当前能力与规划边界

当前实现支持 OpenAI Chat Completions、Responses（含 SSE，不支持 `store` / `previous_response_id`）、Embeddings、Models，以及经 Agent Tunnel 执行的受控工作流 Job API。控制面已提供租户、用户、API Key、审计、配额与 MySQL Job 历史；Console 已接入这些管理页面及只读机群、模板目录、Job 取消与产物预览/下载。模型路由已支持控制面版本发布、Gateway 热切换/生效查询与 Console 编辑回滚（P02）；文件配置模式保留。接口限制以服务 README 为准。

下文的架构图与职责列表包含目标能力：Anthropic/Ollama 原生 API、Managed 部署、对象存储、资源采集、模板/部署配置发布与告警仍属规划；Direct 的可交付范围待 A04 核实。Registry 已有令牌管理与节点禁用，控制面/Console 已接入节点审批、禁用/启用、维护与平台运维会话（P01）。历史记录可查不保证文件在原节点离线或 Gateway 重启后仍可下载。

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

AIServeWeave 分为控制面、数据面和节点面，采用单仓库、按服务组织的模块化架构。ControlPlane、Registry、Gateway 与 Agent 各自承担明确职责；逻辑分层不要求所有模块同进程，也不要求提前将每项能力拆成独立服务。

```text
Console ── Admin / Operator API ──► ControlPlane ──► 关系数据库
                                      │              配置、Job、审计
                               配置同步 / 内部 API
                                      │
Client ── 兼容 API / Workflow API ──► Gateway ──► 对象存储
                                      │              输入与产物
                          ┌───────────┴───────────┐
                       Direct                  Tunnel
                          │                       ▲
                    可达推理后端             Agent 主动出站
                                                  │
                                           本地推理后端

Registry ◄── Agent 身份注册 / 续期
         ◄── Gateway 副本名册订阅
```

配置与任务元数据通过控制面管理；推理事件和文件流由数据面转发。Registry 负责身份与副本发现，节点实时健康与能力由 Agent 经隧道上报 Gateway。

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

Registry 负责节点身份和 Gateway 副本发现：

- 校验一次性注册令牌，签发与续期节点证书
- 维护 Gateway 副本名册，通知可连接的隧道入口及排空状态
- 为节点身份唯一性、凭据失效和服务间认证提供执行边界

节点实时健康、运行时能力与连接状态由 Gateway 管理；租户、模型目录、部署期望状态、路由和管理审计属于 ControlPlane，避免多处维护同一份权威状态。

### aiserveweave-control-plane

ControlPlane 提供租户 Admin API、平台运维 API 和服务间内部 API，负责持久化配置、权限、Job 历史与审计。配置发布应有版本、Gateway 生效确认与回滚路径；运行状态由数据面报告，不能用期望状态代替实际状态。

Console 通过控制面访问管理能力；数据库驱动与 ORM 留在控制面，Gateway 使用内部客户端访问元数据。Job 持久化的可用性与普通推理请求的可用性分别定义。

### aiserveweave-gateway

统一 AI API 网关负责数据面流量：

- 提供 OpenAI、Anthropic 和工作流兼容 API
- 完成 API Key 鉴权、配额和限流
- 将外部协议转换成内部统一协议
- 根据节点健康、能力快照和路由策略选择 Deployment
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

Tunnel 使用 gRPC 双向流传递运行时语义；协议以 `api/proto/tunnel/v1/tunnel.proto` 为唯一来源。Direct 只访问受配置与权限约束的后端地址，不能退化为任意 HTTP 代理。

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

已实现的基础兼容 API（参数与限制见 [Gateway README](service/aiServeWeaveGateway/README.md)）：

- `POST /v1/chat/completions`（含 SSE）
- `POST /v1/responses`（含 SSE）
- `POST /v1/embeddings`
- `GET /v1/models`

规划中的扩展协议范围：

- Anthropic `POST /v1/messages`
- Ollama 原生 API
- 音频转录和翻译
- rerank
- OpenAI-compatible 图像生成

ComfyUI 工作流和异步任务 API 已实现，见下方「ComfyUI 任务 API」。

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

AIServeWeave 将 ComfyUI 视为一种独立 Backend。以下为目标接入范围，目前已实现 External 后端适配与 Agent Tunnel 链路；Direct 待核实，Managed 与 Comfy Cloud 尚未交付：

- 节点上自托管的 ComfyUI
- 通过 Direct 模式访问的 ComfyUI 服务器
- 通过 Agent Tunnel 访问的本地或内网 ComfyUI
- 可选的 Comfy Cloud Backend

接入分为两级：

1. External：用户已经启动 ComfyUI，Agent 只负责探测、注册和代理。
2. Managed：平台下发声明式部署配置，由 Agent 负责安装或启动、健康检查、停止和升级。

External 与 Managed 共用工作流执行语义；Managed 额外承担 Python、CUDA、模型与自定义节点的环境和生命周期管理。

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

### 托管部署（规划）

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

Managed 的基础目标环境为 Linux NVIDIA GPU 与 Docker/容器运行时；macOS 的 External 接入与原生环境生命周期管理是不同的支持范围。

部署控制应包含：

- 创建、启动、停止、重启和删除 ComfyUI 实例
- 固定镜像或版本，不自动追踪 `latest`
- GPU、端口、模型目录和输入输出目录配置
- 环境变量只引用平台 Secret，不在部署规格中保存明文
- 启动后依次检查 `/system_stats`、`/object_info` 和 WebSocket
- 上报 `pending`、`installing`、`starting`、`ready`、`degraded`、`stopped` 和 `failed` 状态
- 升级前检查正在运行的 Job，默认等待排空后再滚动重启
- 自定义节点采用允许列表并固定版本，安装动作写入审计日志

模型文件通常很大，Managed 模式支持挂载用户准备好的共享模型目录。模型分发是独立能力，需要校验和、断点续传、磁盘配额和来源白名单。

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

Job 的公开 ID 与后端 `prompt_id` 分离；公开产物 ID 与后端磁盘路径分离。不属于本租户的 Job/产物与不存在的资源返回相同的 404。

Job 持久化由控制面管理，目标数据库为 MySQL 9.7 / InnoDB。Gateway 保留有界运行态缓存，经内部 API 写入生命周期并恢复未结束的任务。终态应依据后端确认，取消请求被接受不等于任务已经取消。

数据库事务不能与 ComfyUI 提交组成一个本地原子事务。设计必须区分提交意图、后端确认与结果未知，采用幂等更新和状态对账；结果未知时不盲目重新提交。后台同步有批次、频率和并发上限；多副本恢复必须明确执行权，避免重复执行。断线或观测超时不应直接被解释为后端已经停止。

目标任务执行状态（提交确认状态另行建模）：

```text
queued → running → succeeded
                 ├→ failed
                 ├→ cancelled
                 └→ timed_out
```

ComfyUI 的 `prompt_id` 是后端任务 ID，不能直接作为公开 ID。AIServeWeave 应生成自己的 `job_id`，保存二者映射，并使用租户权限保护状态、事件和产物。

对于简单的文生图场景，可以把 OpenAI-compatible `POST /v1/images/generations` 映射到管理员指定的 ComfyUI 工作流模板。复杂工作流仍使用 AIServeWeave Workflow API，以免丢失 ComfyUI 的图结构、视频输出和自定义参数能力。

### 文件与产物

P04 的核心链路已实现，文件流不直接存入关系数据库：

- **输入上传**：客户端在提交工作流的同一次 `multipart/form-data` HTTP 请求里携带文件（`common/workflowtemplate` 的 `InputFile` 输入类型），Gateway 选定一个候选节点后经隧道新增的 `OPERATION_INPUT_UPLOAD` 把字节流式推给它，节点把文件写进本地 ComfyUI 的输入目录，返回的引用被织入提交的图；这次上传与提交固定在同一个候选节点、不做跨节点重试，细节见 Gateway README 数据面约束一节的第十三条。
- **产物持久化**：生成完成后，Gateway 从 ComfyUI 流式拉取产物并写入可插拔的对象存储（`local`/`s3`-compatible/`webdav`，`service/aiServeWeaveGateway/objectstore`），下载接口优先读已持久化的副本、任何失败回退到经隧道向节点的实时代理；关系数据库保存文件元数据、哈希、大小、租户与存储位置，细节见同一节的第十二条。
- **尚未交付**：预览图的单独处理、保留期与清理批次、下载接口的短期签名 URL 形态（当前是经鉴权的流式代理，未提供签名 URL）。

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

路由目标携带真实模型名、节点选择器、优先级与权重；管理配置的期望状态与节点上报的能力快照分别保存。持久化 Backend/Deployment 时，应明确其稳定身份和生命周期，不能直接把瞬时连接对象当作实体。

**节点标签是偏好，不是权限。** Agent 自报标签只用于调度筛选。租户是否可以访问某模型、模板或节点池，必须由受信任的授权配置决定。

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

调度策略的规划范围：

- 加权轮询
- 最少正在执行请求
- 节点优先级
- 节点标签路由，例如 `region=local`
- 租户绑定节点或节点池
- 会话亲和，以便复用 KV Cache
- 连续失败熔断和自动恢复

流式请求只有在返回第一个 token 之前可以安全重试。一旦已经向客户端发送内容，不应自动切换节点，否则可能产生重复或不连续的输出。

## 数据模型

关系数据库由 ControlPlane 的 `Database.Driver` 选择。Job 持久化以 MySQL 9.7 / InnoDB 为目标，保留既有 PostgreSQL 接入；跨引擎兼容范围以迁移与集成测试为准，不假定 JSON 类型、索引或锁行为相同。Gateway 与 Agent 不直接依赖关系数据库。

以下为逻辑数据模型，表示实体职责与关系，不要求每个条目都独立建表。物理表、索引与迁移随相应能力设计；具体完成状态见 [STATUS.md](STATUS.md)。

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
job_artifacts
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
- Artifact 保存输入文件、预览图和最终生成文件的元数据；Job Artifact 保存任务与稳定公开产物 ID 的关联

任务、事件、文件元数据与请求摘要均需定义保留期和有界清理。Prometheus 承担指标采集，历史指标与日志使用适合查询规模的存储；用量账本独立定义去重与结算规则。备份范围应同时覆盖数据库、Registry 身份材料和对象存储，恢复后进行引用一致性检查。

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

指标经 `runtime.Metrics` 抽象记录，`common/metrics` 提供注册表与 Prometheus 导出。指标定义与记录点放在同一模块，服务装配时统一注册。追踪通过请求关联标识连接 Gateway、Scheduler、Tunnel、Agent 和后端；后端不支持传播时明确链路边界。

观测范围包括节点连接与心跳、部署健康、请求量与并发、TTFT、总时长、token 用量、吞吐、错误与重试、ComfyUI 队列及任务时长、GPU OOM、产物传输与存储用量。

指标端点应处于受控网络；节点标识涉及资产信息。标签来源必须受控且基数有界，模型名、请求路径、request ID、Prompt、工作流 JSON 和任意错误文本不能直接成为标签。具体指标、端点配置与实现边界见 [Gateway README](service/aiServeWeaveGateway/README.md) 和 [隧道 README](service/aiServeWeaveAgent/tunnel/README.md)。

历史曲线、告警、请求检索和用量账本具有不同的数据保留与授权需求。Console 通过控制面授权查询，不能把副本的实时指标当作历史统计。可用性、延迟、恢复时间与可接受数据丢失范围应有量化验收口径，数值在容量与故障测试后确定。

## 代码结构

按服务归属组织实现，共享包只承载跨服务必须一致的契约。

| 路径 | 职责 |
| --- | --- |
| `api/proto/tunnel/v1/` | Agent、Gateway、Registry 共用的 gRPC 契约与生成代码 |
| `common/runtime/` | 运行时语义、能力门禁、后端适配与并发控制 |
| `common/tunnelwire/` | runtime 与隧道 proto 的唯一转换边界 |
| `common/apikey/`、`common/quota/` | Key 格式/哈希与租户限制契约 |
| `common/modelroute/` | 模型路由发布契约：别名/目标、CAS 版本化快照与生效状态（P02） |
| `common/workflowtemplate/` | 工作流模板发布契约：内容、版本化快照与生效状态（P03），图仅在此契约与 Gateway 内部流转 |
| `common/nodeview/`、`common/workflowview/` | 允许列表约束的机群、模板和 Job 展示契约，不含图 |
| `common/metrics/` | 指标注册、采集与 Prometheus 导出 |
| `service/aiServeWeaveAgent/` | 本机发现、运行时管理、主动出站隧道 |
| `service/aiServeWeaveGateway/` | 前门、调度、路由、隧道终结、配额与工作流执行入口 |
| `service/aiServeWeaveRegistry/` | 节点身份与副本发现 |
| `service/aiServeWeaveControlPlane/` | 管理与内部 API、数据库存储、权限、配置与审计 |
| `service/aiServeWeaveConsole/` | Next.js 管理界面及受限的服务端转发入口 |
| `deploy/` | 部署配置与操作说明 |

模块路径为 `AIServeWeave`。依赖边界、编码和质量门禁见 [AGENTS.md](AGENTS.md)；隧道状态机见 [隧道设计](service/aiServeWeaveAgent/tunnel/README.md)，控制面凭据与授权设计见 [控制面 README](service/aiServeWeaveControlPlane/README.md)。

## 核心业务链路

```text
OpenAI SDK → Gateway 协议解析 → 能力与路由筛选
           → Agent 隧道 → Ollama / vLLM → SSE 返回

Workflow API → 受控模板绑定 → Job 提交与确认
             → Agent → ComfyUI → 状态同步与产物流式转发
             → 控制面任务历史 → Console 查询和授权下载
```

两条链路分别对应同步/流式推理与异步任务。实时转发、持久历史和文件可用性需要分别定义故障语义，不能以某一环节成功推断整个链路可靠。

开发里程碑、任务顺序、未完成项和验收记录统一维护在 [STATUS.md](STATUS.md)。

# ComfyUI 接入与部署

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

## ComfyUI 部署模式

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

## 托管部署（规划）

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

## 工作流模板

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

## ComfyUI 任务 API

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

## 文件与产物

P04 的核心链路已实现，文件流不直接存入关系数据库：

- **输入上传**：客户端在提交工作流的同一次 `multipart/form-data` HTTP 请求里携带文件（`common/workflowtemplate` 的 `InputFile` 输入类型），Gateway 选定一个候选节点后经隧道新增的 `OPERATION_INPUT_UPLOAD` 把字节流式推给它，节点把文件写进本地 ComfyUI 的输入目录，返回的引用被织入提交的图；这次上传与提交固定在同一个候选节点、不做跨节点重试，细节见 Gateway README 数据面约束一节的第十三条。
- **产物持久化**：生成完成后，Gateway 从 ComfyUI 流式拉取产物并写入可插拔的对象存储（`local`/`s3`-compatible/`webdav`，`service/aiServeWeaveGateway/objectstore`），下载接口优先读已持久化的副本、任何失败回退到经隧道向节点的实时代理；关系数据库保存文件元数据、哈希、大小、租户与存储位置，细节见同一节的第十二条。
- **尚未交付**：预览图的单独处理、保留期与清理批次、下载接口的短期签名 URL 形态（当前是经鉴权的流式代理，未提供签名 URL）。

## ComfyUI 调度

ComfyUI 调度除通用节点状态外，还应考虑：

- 工作流所需 checkpoint、LoRA、VAE 和 ControlNet 是否存在
- Core Node 和 Custom Node 是否可用、版本是否兼容
- 节点当前队列长度和预计等待时间
- GPU 显存、分辨率、批量大小和历史 OOM 情况
- 工作流类型，例如图片、视频、音频或 3D
- 租户是否有权使用指定工作流和模型

ComfyUI 作业一旦开始执行，不应自动迁移到其他节点。只有仍处于 AIServeWeave 队列且尚未提交到 ComfyUI 的任务，才可以重新调度。

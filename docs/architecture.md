# 架构设计

本文描述 AIServeWeave 的分层架构、各服务职责与代码组织，包含尚未实现的目标能力，不作为功能可用性声明。当前实现进度见 [STATUS.md](../STATUS.md)，具体接口与运行限制见各服务 README。

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

模块路径为 `AIServeWeave`。依赖边界、编码和质量门禁见 [AGENTS.md](../AGENTS.md)；隧道状态机见 [隧道设计](../service/aiServeWeaveAgent/tunnel/README.md)，控制面凭据与授权设计见 [控制面 README](../service/aiServeWeaveControlPlane/README.md)。

## 核心业务链路

```text
OpenAI SDK → Gateway 协议解析 → 能力与路由筛选
           → Agent 隧道 → Ollama / vLLM → SSE 返回

Workflow API → 受控模板绑定 → Job 提交与确认
             → Agent → ComfyUI → 状态同步与产物流式转发
             → 控制面任务历史 → Console 查询和授权下载
```

两条链路分别对应同步/流式推理与异步任务。实时转发、持久历史和文件可用性需要分别定义故障语义，不能以某一环节成功推断整个链路可靠。

开发里程碑、任务顺序、未完成项和验收记录统一维护在 [STATUS.md](../STATUS.md)。控制面的 schema 升级、迁移失败处理与备份恢复见 [数据库升级与恢复](../deploy/database-recovery.md)。

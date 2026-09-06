# AIServeWeave 开发状态与待办

更新日期：2026-09-06。

本文记录当前能力、待开发任务和验收目标。未勾选项均尚未完成；优先级用于安排实施顺序，不表示已经启动开发。架构与协议边界见 [README](README.md)，Console 的细分任务与历史验收见 [Console STATUS](service/aiServeWeaveConsole/STATUS.md)。本次更新仅整理文档，不代表重新执行过测试或部署验收。

## 文档分工与里程碑

根 README 维护项目规划与架构，不维护完成标记或排期。根 STATUS 是跨服务任务安排的唯一入口；服务 README 保留接口、配置与实际限制，Console STATUS 保留子任务与验收证据，本文引用其编号。

原 README 的三阶段路线在此统一安排。里程碑是交付门槛，不承诺未经评估的日期；P0 表示下一轮主线或发布前门槛，不要求全部同时开工。

| 里程碑 | 范围与依赖 | 退出条件 |
| --- | --- | --- |
| M0 最小推理闭环 | 已有隧道、发现、OpenAI 与工作流前门；A06 补实机验收 | 保留真实 Ollama/mTLS/流式验证依据，补可复现的真实 ComfyUI 链路验证 |
| M1 可靠 Job | J01–J08；J03 先交付 P07 所需迁移基础 | MySQL 9.7 上受理语义、后台推进、重启恢复、跨副本与租户隔离通过；原节点离线后的文件访问仍依赖 P04 |
| M2 可管理平台 | P01–P06；节点写操作依赖 S01–S03 | 节点、路由与模板变更可审计且可确认生效；历史 Job 与产物可在 Console 使用 |
| M3 可运维发布 | S01–S03、R01–R04、P07–P10、A01–A03 | 发布、升级、备份恢复可重复；容量边界与故障行为有验证依据 |
| M4 按需求扩展 | P2 与 A04/A05 | 各项先明确场景、依赖与验收，再拆分实施；不以功能数量代替稳定性门槛 |

R01/R02 可与 Job 主线并行。P07 与 J03 共用迁移框架；P04 依赖 J03 的元数据关联，上传与对象存储接口可先设计。P08/P09 先明确指标和存储来源再开发图表。P03 发布新模板时须保持历史 Job 引用的版本可追溯。

## 当前已完成

- [x] Registry 节点证书签发与续期、Gateway 副本名册；Agent 主动出站建立 mTLS 隧道、多副本连接与槽池。
- [x] Ollama、vLLM、SGLang、ComfyUI 运行时适配；本机 Ollama/vLLM 自动发现。
- [x] OpenAI Chat Completions、Responses、Embeddings、Models 前门，支持相应流式接口；Responses 不支持持久会话续接。
- [x] 模型别名、节点标签、优先级与权重路由，以及健康过滤、熔断、恢复和受限重试；路由目前由文件配置。
- [x] 工作流模板加载与输入绑定、Job 提交、状态查询、SSE 事件、取消、产物列举与流式下载。
- [x] 控制面的租户、用户登录与创建、API Key 签发/吊销、审计、配额读写；列表游标分页与服务端筛选。
- [x] Gateway 租户请求数、token 数和并发限制，包含内存与 Redis 实现。
- [x] Console 登录与服务端会话、用户/Key/审计/配额页面、只读节点与模型清单、工作流目录和实时 Job 视图。
- [x] Agent/Gateway Prometheus 指标导出，以及包含 Console 的 Docker Compose 部署配置。

当前主要边界：Job 仍保存在单个 Gateway 的有界内存中，重启丢失且不跨副本共享；状态依赖调用方轮询或订阅事件推进。Console 的聚合实时视图不等于持久化历史。模型、节点和模板的只读页面不等于管理与发布能力。

历史验证索引：真实 Ollama + mTLS + Gateway 非流式/SSE、多副本联调、故障注入与滚动升级记录见 [隧道 README](service/aiServeWeaveAgent/tunnel/README.md) 和 Gateway e2e 测试。24h 长稳工具已存在，最终结果需核实归档，不沿用旧文档“正在运行”的描述。

已有关系表为 `tenants`、`users`、`api_keys`、`audit_logs`（PostgreSQL/MySQL 双支持）与 `jobs`、`job_artifacts`（仅 MySQL，J03 新增，见下方 P0）；其他逻辑实体不代表已建表。Responses 经前门转换为 canonical 请求，`store` / `previous_response_id` 被明确拒绝。Agent 的 `workflow/` 为占位包，现有模板目录与绑定属于 Gateway，不能按目录名称重复实现。

## P0：Job 可靠性闭环（下一轮主线）

Job 持久化目标数据库采用 **MySQL 9.7 / InnoDB**。沿用控制面现有 MySQL/GORM 接入，数据库连接与迁移由控制面管理，Gateway 经内部 API 访问，不引入 GORM 或 MySQL 驱动。控制面现有 PostgreSQL 能力保持兼容；新增 schema 的跨数据库支持范围需在迁移设计中明确。

预期调用链：`Gateway → 控制面内部 Job API → MySQL`；`Console → 控制面 Admin API → Job 历史`。内部执行位置和后端运行 ID 不进入租户可见视图。

| 状态 | 编号 | 待开发任务 | 验收目标 |
| --- | --- | --- | --- |
| [x] | J01 | 定义持久化契约与失败语义：创建、提交确认、状态更新、终态、恢复；确定写入失败与推理可用性的关系 | 区分未提交、已确认和提交结果未知；明确何时向客户端确认持久化受理；数据库故障不拖垮普通推理链路——契约见 [ControlPlane README「Job 持久化契约」](service/aiServeWeaveControlPlane/README.md#job-持久化契约j01-设计j03-已建表j04-已实现内部-apij05-已接入持久化)，本项仅完成设计，不含建表与代码 |
| [x] | J02 | 在 Gateway 增加有界后台状态同步 | 调用方不再轮询/SSE 时也能推进任务；限制扫描批次、并发和频率，处理节点消失、超时和优雅停止——实现见 `httpapi/jobsync.go`，详见 [Gateway README「工作流 Job」第九条](service/aiServeWeaveGateway/README.md#工作流-job) |
| [x] | J03 | 建立 `jobs`、`job_artifacts` 表及带版本迁移 | 保存租户、工作流及版本、后端运行映射、状态版本、时间和稳定产物 ID；按租户与时间/状态建立查询索引；迁移可重复执行且有版本记录——实现见 `internal/store/gormstore/jobmigrate.go` 与 `internal/model/job.go`，详见 [ControlPlane README「Job 持久化契约」的「已实现的存储层」小节](service/aiServeWeaveControlPlane/README.md#已实现的存储层j03)；真实 MySQL 上的验证留给 J08 |
| [x] | J04 | 实现控制面内部 Job 读写 API 与 Gateway 客户端 | 服务间鉴权、租户隔离、幂等写入和并发条件更新；重复/乱序事件不能覆盖终态；错误与日志不泄露凭据、Prompt 或工作流 JSON——实现见 `internal/handler`（`/internal/v1/jobs*`，InternalToken 守卫）、`internal/logic/jobs.go` 与 Gateway 侧 `controlplaneclient/jobs.go` 的 `JobsClient`，详见 [ControlPlane README「Job 持久化契约」的「已实现的内部 API」小节](service/aiServeWeaveControlPlane/README.md#已实现的内部-api-与-gateway-客户端j04)；接入 Gateway 提交/同步路径见 J05 |
| [x] | J05 | 处理数据库与 ComfyUI 提交之间的故障窗口 | 覆盖"后端已接收但映射尚未落库"；结果未知时不盲目重提；补写若采用队列须有容量上限与可靠恢复机制，不能仅靠内存重试承诺不丢——实现见 `httpapi/jobpersist.go` 的 `jobPersister`，详见 [ControlPlane README「Job 持久化契约」的「已接入持久化」小节](service/aiServeWeaveControlPlane/README.md#已接入持久化j05)；恢复机制是有界退避重试而非持久队列，如实记录了「进程重启/逐出即丢」的边界，重启后的路由恢复留给 J06 |
| [ ] | J06 | 实现重启恢复与跨 Gateway 副本访问 | 重启后可恢复非终态任务；任一可服务副本可查询、取消和访问产物；保存稳定节点/运行时标识，不序列化连接对象；明确恢复执行权及失联处理 |
| [ ] | J07 | 提供持久化历史查询与 Console Job 管理 | 按租户、时间、状态、工作流分页；支持详情、进度、取消和授权产物访问；明确实时状态与最后观测时间，补齐 Console C26 |
| [ ] | J08 | 在真实 MySQL 9.7 上完成集成与故障恢复验证 | 覆盖迁移、并发更新、重复事件、跨租户拒绝、数据库中断、Gateway 重启、多副本查询和提交结果未知；默认单元测试不依赖真实数据库 |

实施顺序：先完成 J01；J02 可独立交付，J03/J04 建立存储链路，J05/J06 补故障恢复，再完成 J07；J08 随各阶段验证，作为整体验收门禁。

存储约定：

- MySQL 保存任务与产物元数据；图片、视频、音频文件保存在文件系统或对象存储。
- `job_events` 为可选扩展，先确定是否需要历史事件回放；关键状态可记录，高频采样进度不默认逐条永久落库。
- 定义任务、事件与产物元数据的保留期、清理批次和容量限制，避免将内存上限问题转移成数据库无限增长。
- Compose 已有 `mysql:9.7` 服务，但默认控制面仍连接 PostgreSQL；补齐 MySQL Driver/DSN、启动依赖和部署说明，不能仅启用 mysql profile 就宣称完成切换。
- Job 元数据恢复不等于文件可用；原节点离线后的产物访问依赖下述对象存储任务。

## P0：节点身份与开源交付

- [ ] **S01 节点身份唯一性**：检测重复 `node_id`，区分正常续期、重装与身份冒用；补并发注册测试和运维处理说明。
- [ ] **S02 正式注册令牌管理**：提供受控签发、过期、撤销与一次性消费路径，解决 CLI/server 共享 token 文件的并发一致性；明确令牌与节点或租户的授权绑定规则。
- [ ] **S03 身份认证与吊销设计**：明确 Gateway 加入 Registry 名册的认证与授权；建立节点禁用/证书吊销到现有连接及重连的生效路径。
- [ ] **R01 开源治理文件**：由维护者确定许可证并补 LICENSE、CONTRIBUTING、SECURITY，说明贡献流程和私密漏洞报告渠道。
- [ ] **R02 持续集成**：自动执行仓库规定的 Go 格式、vet、build、proto 生成一致性、测试与 race 门禁，以及 Console lint/typecheck/test/build；外部后端测试独立运行。
- [ ] **R03 版本发布**：提供版本化二进制与容器镜像、校验和、变更记录、支持的平台与升级说明，验证全新环境的最小部署链路。
- [ ] **R04 文档一致性**：核对根 README、各服务 README、AGENTS 代码地图和 Console STATUS 中过时的“未实现”描述；已实现与规划能力分开标注。

## P1：平台管理与运维

| 状态 | 编号 | 待开发任务 | 交付边界 |
| --- | --- | --- | --- |
| [ ] | P01 | 节点审批、禁用、维护与运维身份 | 持久化期望状态并下发、展示实际生效结果；平台运维身份与租户角色分开授权，操作可归因审计；对应 Console C22 |
| [ ] | P02 | 模型与路由管理 | 控制面存储、校验、版本发布、Gateway 同步确认及回滚，补 Console C24；保留明确的文件配置迁移方案 |
| [ ] | P03 | 工作流模板版本与发布 | 创建、输入/输出校验、依赖检查、发布与回滚、租户可见范围；记录自定义节点及模型版本，任务关联提交时的模板版本 |
| [ ] | P04 | 输入上传与持久产物存储 | 本地/S3-compatible 存储、授权上传下载、格式与大小限制、保留期和清理；有界流式传输，元数据可与文件对账 |
| [ ] | P05 | 用户与会话生命周期 | 改密、重置密码、禁用、角色调整、会话撤销；定义已有 JWT 与 API Key 的失效语义，补权限测试和管理页面 |
| [ ] | P06 | Key 吊销通知 | 向 Gateway 推送失效，缩短当前缓存窗口；明确断线、漏通知、重连补偿和可验证的生效时限 |
| [ ] | P07 | 数据库升级与恢复 | 用带版本迁移替换 AutoMigrate；覆盖 PostgreSQL/MySQL 真实引擎、已有数据升级、备份恢复和失败处理 |
| [ ] | P08 | 指标、追踪与历史监控 | Registry/控制面接入指标；接入历史时序查询和跨服务 trace，补 Console C27；保持数据面依赖边界与标签基数限制 |
| [ ] | P09 | 请求检索与告警 | 保存脱敏请求元数据、提供分页检索与保留期；告警计算、规则和处理状态由后端负责，对应 Console C28/C29 |
| [ ] | P10 | 稳定性与界面验收 | 完成长稳数据归档、真实流量熔断阈值校准及滚动升级验证；补 Console Q03 的窄屏、键盘、焦点、长文本和大列表验收 |

## 规划补充：架构决策与验收口径

- [ ] **A01 支持范围与可靠性目标**：定义节点数、并发、任务/产物容量、API 兼容矩阵，以及可用性、延迟、RPO/RTO 的目标与测量方法；覆盖控制面、数据库、Redis、Registry 和节点分别故障时的行为。数值经测试确认，不先写未经验证的承诺。
- [ ] **A02 灾难恢复与密钥轮换**：验证数据库、Registry CA/注册状态、对象存储的备份恢复及引用一致性；补服务端证书到期、CA/内部密钥轮换和恢复演练。Job 重启测试不能替代整个平台灾备。
- [ ] **A03 协议与配置升级兼容**：明确 Agent/Gateway/Registry 混合版本支持范围、proto 演进规则、配置版本及数据库迁移顺序，验证滚动升级与回退边界。
- [ ] **A04 Direct 模式边界核实**：对照代码确认可交付的 Direct 路径及与 Tunnel 的差异；定义地址允许列表、后端凭据归属、SSRF 防护与验收。目标架构中的 Direct 箭头不作为实现证明。
- [ ] **A05 调度扩展拆分**：按实际负载评估最少在途请求、延迟/成本评分、租户节点池和会话亲和；受信任授权与 Agent 自报标签分开，依赖 P01/P02 及资源采集，不能把现有加权路由视为全部完成。
- [ ] **A06 ComfyUI 专项观测与实机验收**：补队列长度、任务时长、成功率、OOM、产物传输与存储用量；真实后端验证提交、取消、进度和文件完整性，完成上传/存储后扩展输入文件链路。指标复用 P08，证据回填 M0/M2。

## P2：后续扩展

- [ ] Anthropic Messages、Ollama 原生 API、音频转录/翻译和 rerank，逐项定义能力与兼容边界。
- [ ] OpenAI-compatible 图像生成映射到受控 ComfyUI 模板，以及 Responses 持久会话和更多多模态输入。
- [ ] Linux NVIDIA 上的 ComfyUI Managed Docker 部署、健康检查、排空升级和固定版本的自定义节点管理；先验收 External 链路，再做 Managed。macOS 先支持已启动的 ComfyUI，原生 Python/Desktop 生命周期管理另行评估。
- [ ] 模型分发：校验和、断点续传、磁盘配额与来源白名单。
- [ ] GPU/显存和系统资源采集、资源感知调度，以及有界任务排队；已提交的 ComfyUI 任务不自动跨节点迁移。
- [ ] Agent 自动升级、Kubernetes 部署、Registry 高可用；Gateway 已有多副本连接能力，继续补共享任务状态和故障切换验收。
- [ ] 按租户/模型的持久化用量账本与计费，定义去重和结算规则；Prometheus token 计数不能代替账本。
- [ ] 根据实际规模评估独立 Tunnel Gateway 与事件基础设施，避免在可靠性闭环完成前增加不必要的服务拆分。

## 状态维护与完成标准

任务完成后同时更新本文和所属服务文档；公共接口变化同步更新契约与 API 说明。勾选须有实现、适用测试及可复现的验收依据，不能仅凭依赖安装、数据库建表或配置存在认定功能完成。涉及 Go 代码执行根 AGENTS 的全部质量门禁，涉及 Console 同时执行其专属门禁；真实数据库与真实后端测试按仓库约定独立隔离。

当前缺口的详细依据：

- [Gateway README](service/aiServeWeaveGateway/README.md)：Job 内存边界、状态观测、产物与路由配置。
- [ControlPlane README](service/aiServeWeaveControlPlane/README.md)：历史任务、管理写路径、数据库迁移与监控缺口。
- [Registry README](service/aiServeWeaveRegistry/README.md)：身份冲突、token 存储、单实例与认证边界。
- [Console STATUS](service/aiServeWeaveConsole/STATUS.md)：C22、C24、C26–C29 与 Q03 待交付项。

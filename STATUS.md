# AIServeWeave 开发状态与待办

更新日期：2026-10-27。

本文是跨服务任务安排的唯一入口，只记录交付范围、遗留缺口与细节所在位置。实现细节、接口与验收证据在各服务 STATUS/README 与 [`docs/superpowers/specs/`](docs/superpowers/specs/) 设计文档中，本文不复述；架构与协议边界见 [架构设计](docs/architecture.md)。根 README 只做简介与文档索引，不维护完成标记或排期。

## 里程碑

里程碑是交付门槛，不承诺未经评估的日期。

| 里程碑 | 范围 | 退出条件 | 状态 |
| --- | --- | --- | --- |
| M0 最小推理闭环 | 隧道、发现、OpenAI 与工作流前门；A06 | 真实 Ollama/mTLS/流式验证；可复现的真实 ComfyUI 链路验证 | 真实 ComfyUI 验证未完成 |
| M1 可靠 Job | J01–J08 | MySQL 9.7 上受理语义、后台推进、重启恢复、跨副本与租户隔离通过 | 已完成 |
| M2 可管理平台 | P01–P06、S01–S03 | 节点、路由与模板变更可审计且可确认生效；历史 Job 与产物可在 Console 使用 | 已完成 |
| M3 可运维发布 | R01–R04、P07–P10、A01–A03 | 发布、升级、备份恢复可重复；容量边界与故障行为有验证依据 | 已交付，真实环境验证见下方遗留 |
| M4 按需求扩展 | P2、A04/A05 | 各项先明确场景、依赖与验收再拆分实施 | 进行中 |

## 进行中

### ComfyUI Managed Docker（P2）

目标：Linux NVIDIA 上的 ComfyUI Managed Docker 部署、健康检查、排空升级和固定版本自定义节点管理；先验收 External 链路再做 Managed。macOS 只支持已启动的 ComfyUI。

五个子任务代码均已交付，设计见 [总设计](docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-design.md)、[子任务二](docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-subtask2-design.md)、[子任务四](docs/superpowers/specs/2026-09-19-p2-comfyui-managed-docker-subtask4-design.md)：

1. Agent 本地单容器生命周期管理（`service/aiServeWeaveAgent/comfyuimanaged/`，`-comfyui-managed-*` flag）。
2. 封闭生命周期动作 START/STOP/RESTART 过隧道，Gateway 独立监听器 `-comfyui-managed-addr`；RESTART 在本副本仍有活跃 job 时答 409。
3. ControlPlane 跨副本聚合：`/operator/v1/nodes/:id/comfyui-managed`。
4. 自定义节点允许列表、固定版本安装与对账诊断。
5. Console `/operator/comfyui-managed`。

**未勾选原因**：前置条件“先验收 External 链路”未关闭，ComfyUI 协议、GPU 直通与 Managed 全链路都没在真实硬件上验收过。

**已知限制**：排空检查只看得到本副本的 job，且不会自动等排空完再执行；镜像须自带 `git`，自定义节点目录因镜像而异，不装 pip 依赖；绕过 ControlPlane 直连 Gateway 触发的操作不留审计；对账端点不校验租户边界。

### Agent 自动升级、Kubernetes 部署、Registry 高可用（P2）

- **Gateway 故障切换收尾**：已交付。终态 job 的产物在本副本未命中时回退到控制面查询，见 [Gateway STATUS「工作流 Job」第 18 条](service/aiServeWeaveGateway/STATUS.md#工作流-job)与 [ControlPlane STATUS「已实现的跨副本产物恢复」](service/aiServeWeaveControlPlane/STATUS.md#已实现的跨副本产物恢复gateway-故障切换收尾)。
- **Registry 高可用**：已评估，未实现，见 [评估](docs/superpowers/specs/2026-09-18-p2-registry-ha-evaluation.md)。CA 私钥“只在单进程持有”的安全声明与多活冲突，需要维护者裁决：放宽这条声明，或把目标降为缩短故障切换时间。
- **Agent 自动升级**：见 [设计](docs/superpowers/specs/2026-09-19-p2-agent-auto-upgrade-design.md)。子任务一（契约与 CHECK）、子任务二（整份清单 Ed25519 签名、下载、排空、`syscall.Exec` 自替换）已交付；二进制托管在 GitHub Releases，签名私钥由维护者离线保管。**待做**：子任务三 ROLLBACK、子任务四 Gateway 独立监听器、子任务五控制面聚合与 Console 灰度可见性。
- **Kubernetes 部署**：未排期，`deploy/` 下只有 docker-compose。

## 未完成的真实环境验证

以下各项的代码与默认测试已通过并已勾选，但缺真实环境验证：

- **ComfyUI**：`TestLiveComfyUI`（`COMFYUI_BASE_URL`）从未针对真实服务器运行，提交、取消、进度与产物完整性都没在真实后端验证过（A06）。
- **真实数据库 live 测试未运行**：`request_logs`、`alert_rules`/`alert_instances`（P09），`response_turns`（Responses 持久会话），`usage_records`（用量账本），`SumArtifactStorageBytes`（A06），`GetJobArtifact`（故障切换收尾）。
- **发布**：发布流水线从未真实推送过 GHCR，需在第一次打 tag 时验证（R03）。
- **界面**：`/operator/metrics` 未做带真实会话与数据的交互验收（P08）；P01 的真实集群传播与全站 Q03 验收未完成。
- **升级兼容**：没有真实的混合版本滚动升级排练，CI 也没有 `buf breaking` 门禁（A03）。
- **长稳**：24h 长稳由维护者在自有服务器上进行，不在仓库归档，见 [P10 验收手册](deploy/p10-acceptance.md)。
- **后端适配**：TensorRT-LLM、TokenSpeed 未接真实后端；Gemini CLI 未用有效 Key 执行付费生图。
- **能力门禁关闭**：音频转录/翻译、rerank、音频输入、文档输入的协议已打通，但目前没有适配器发布对应能力，这类请求一律被本地拒绝，接入真实后端后才能打开。

## 已知设计缺口

按领域归并，详细依据见对应条目链接的设计文档或服务 STATUS。

- **Job 与产物**：路由绑定以单副本有界内存为主，持久化前就被逐出的 job（连同产物记录）会永久丢失；产物跨副本恢复要求它此前已被列举并持久化过，刚完成的产物会有短暂拿不到的窗口；`jobs`/`job_artifacts` 只支持 MySQL，控制面默认仍连 PostgreSQL，切换数据库是部署时的显式选择。
- **Console 直连 Gateway**：Job 取消与产物访问用的是一把未收窄的租户 Key，泄漏影响整个租户（J07）。这是当前无生产租户阶段的权衡，有真实租户后要重新评估。
- **身份与密钥**：节点禁用最终一致，没有 CRL/OCSP；`TokenAdmin` 用一把不区分调用者的共享密钥，没有 RBAC（S02/S03）。密钥轮换没有新旧并存，CA 轮换没有代码路径，对象存储没有自动对账；参考部署里 Gateway 的服务端证书有效期 30 天且不会自动续期（A02）。Registry 的 JSON 状态文件没有 schema version，回退二进制会抹掉新版本写入的字段（A03）。
- **路由与调度**：生效确认只覆盖已配置的 Fleet 端点，不保证全局原子切换（P02）；Direct 模式在代码里不存在（A04）；租户节点池、基于会话标识的亲和、成本评分都没做，`prefix_hash` 没有命中率指标（A05）；模型和工作流需要多少显存全靠人工声明，模板层面的显存声明还没设计。
- **模板与图像生成**：没有模板删除/归档 API，依赖与输出声明只做结构校验，不和节点实际能力核对（P03）；图像生成靠扩展名判断产物是不是图片，启动期校验不会随模板热替换重跑，`n` 只接受 1。
- **协议前门范围**：Anthropic 的 `tool_result` 内嵌图片按名字拒绝，user 回合里的 `thinking` 块按名字拒绝（assistant 回合回传的未签名块被丢弃，签名块只回放给 Anthropic 后端）；Chat 前门对未建模的未知字段仍静默忽略（Claude CLI 原生路径除外），与「不支持的参数按名字拒绝」不符；Responses 的 `file_id` 拒绝；Ollama 原生 API 的 `images` 拒绝，模型管理端点统一答 501。
- **数据与运维**：`response_turns` 没有保留期清理；用量账本最终一致（缓冲满即丢弃、推送失败不重试）；已通过鉴权的在途推理不因 Key 吊销中断（P06）；MySQL 迁移 dirty 后需人工检查再 resume，不提供 down（P07）；`Kind: ollama` 的模型拉取不受来源白名单与字节配额约束。
- Agent 的 `workflow/` 是占位包，工作流模板目录与输入绑定属于 Gateway，不要按目录名重复实现。

## 已完成

一项一行，细节见链接。

### 基础能力

- Registry 节点证书签发与续期、Gateway 副本名册；Agent 主动出站建立 mTLS 隧道、多副本连接与槽池。
- Ollama、vLLM、SGLang、ComfyUI 运行时适配；本机 Ollama/vLLM/SGLang 自动发现。
- OpenAI Chat Completions、Responses、Embeddings、Models 前门及流式接口。
- 模型别名、节点标签、优先级与权重路由，健康过滤、熔断、恢复和受限重试；路由支持文件配置与控制面版本管理/热更新。
- 工作流模板加载与输入绑定、Job 提交、状态查询、SSE 事件、取消、产物列举与流式下载。
- 控制面租户、用户、API Key、审计、配额；Gateway 租户请求数/token 数/并发限制（内存与 Redis）。
- Console 登录与服务端会话及各管理页面；Agent/Gateway Prometheus 指标；含 Console 的 Docker Compose 部署。
- Agent Gemini CLI 图片生成（`kind: gemini-cli`，2026-10-19），见 [Gemini CLI 说明](service/aiServeWeaveAgent/geminicli/README.md)。

历史验证（真实 Ollama + mTLS + 流式、多副本联调、故障注入与滚动升级）见 [隧道 README](service/aiServeWeaveAgent/tunnel/README.md) 与 Gateway e2e 测试。

### P0：Job 可靠性闭环

细节统一见 [ControlPlane STATUS「Job 持久化契约」](service/aiServeWeaveControlPlane/STATUS.md#job-持久化契约j01j08-实现与边界)与 [Gateway STATUS「工作流 Job」](service/aiServeWeaveGateway/STATUS.md#工作流-job)。

| 编号 | 交付 |
| --- | --- |
| J01 | 持久化契约与失败语义（仅设计） |
| J02 | Gateway 有界后台状态同步（`httpapi/jobsync.go`） |
| J03 | `jobs`、`job_artifacts` 表与带版本迁移 |
| J04 | 控制面内部 Job API（`/internal/v1/jobs*`）与 Gateway `JobsClient` |
| J05 | 提交故障窗口处理：有界退避重试，不是持久队列 |
| J06 | 重启恢复与跨副本访问：靠 `observed_seq` 单调门槛，不用锁或租约；节点永久失联时停在最后观测状态 |
| J07 | 持久化历史查询、Console 历史/详情/取消/产物预览 |
| J08 | 真实 MySQL 9.7 集成与故障恢复验证（`AISW_MYSQL_TEST_DSN` 门控） |

### P0：节点身份与开源交付

- **S01** `node_id` 唯一性，见 [Registry STATUS](service/aiServeWeaveRegistry/STATUS.md#node_id-唯一性s01)。
- **S02** `TokenAdmin` 受控签发/撤销与 `node_id` 绑定，见 [Registry STATUS](service/aiServeWeaveRegistry/STATUS.md#tokenadmin受控签发撤销与-node_id-绑定s02)。
- **S03** Gateway 加入名册的认证、节点禁用经名册广播断开已有连接，见 [Registry STATUS](service/aiServeWeaveRegistry/STATUS.md#gatewaydirectoryjoin-的认证s03)。
- **R01** Apache-2.0，[LICENSE](LICENSE)、[CONTRIBUTING.md](CONTRIBUTING.md)、[SECURITY.md](SECURITY.md)；漏洞报告走 GitHub Security Advisories。
- **R02** CI：[ci.yml](.github/workflows/ci.yml)，含 Go 门禁、Console 门禁与真实 MySQL 集成作业。
- **R03** 版本发布：`-version` flag、[build-release.sh](scripts/build-release.sh)、[release.yml](.github/workflows/release.yml)、[CHANGELOG.md](CHANGELOG.md)、[RELEASING.md](RELEASING.md)。
- **R04** 文档按源码核对一致性（仅改文档）。

### P1：平台管理与运维

| 编号 | 交付 | 详情 |
| --- | --- | --- |
| P01 | 节点审批/禁用/维护、平台运维独立身份与审计，Console C22 | ControlPlane README「节点写路径」「平台运维身份」、Console STATUS |
| P02 | 模型与路由版本管理：CAS 发布/回滚、Gateway 拉取热切换、Console C24 | Gateway README「控制面管理路由」 |
| P03 | 工作流模板版本与发布、可见范围、Console 模板页与 ComfyUI 可视化编辑 | [ControlPlane STATUS「工作流可视化编辑器」](service/aiServeWeaveControlPlane/STATUS.md#工作流可视化编辑器p03-后续)、Console AGENTS |
| P04 | 输入上传与持久产物存储（local/S3/WebDAV）、格式校验、保留期清理 | Gateway README「工作流 Job」第 12–15 条 |
| P05 | Redis 权威会话、用户/运维生命周期、禁用即吊销 Key | ControlPlane STATUS |
| P06 | Key 吊销 generation 通知与 Gateway 缓存失效 | ControlPlane STATUS |
| P07 | 固定版本 SQL 迁移、`-migrate` 命令、事务 outbox、备份恢复 | [数据库升级与恢复](deploy/database-recovery.md) |
| P08 | Registry/控制面指标、`common/reqid` 请求追踪、`metrics_history` 历史时序、Console C27 | [ControlPlane STATUS「指标与历史监控」](service/aiServeWeaveControlPlane/STATUS.md#指标与历史监控p08) |
| P09 | 请求检索 C28 与告警 C29（阈值规则、Webhook） | [Gateway](service/aiServeWeaveGateway/STATUS.md#请求日志中间件与推送p09c28)、[ControlPlane 检索](service/aiServeWeaveControlPlane/STATUS.md#请求日志检索p09c28)、[告警](service/aiServeWeaveControlPlane/STATUS.md#告警评估与通知p09c29) |
| P10 | 长稳工具、Console Q03 验收、熔断参数校准（保留默认值） | [验收手册](deploy/p10-acceptance.md)、[熔断校准记录](docs/acceptance/p10-breaker-calibration-2026-09-13/README.md) |

### 架构决策与验收口径

- **A01** 支持范围与可靠性目标（仅设计，含 11 项已知缺口清单）：[设计](docs/superpowers/specs/2026-09-14-a01-scope-reliability-design.md)。
- **A02** 灾难恢复与密钥轮换（Registry 与对象存储真实演练）：[设计与记录](docs/superpowers/specs/2026-09-14-a02-disaster-recovery-key-rotation-design.md)。
- **A03** 协议与配置升级兼容（含 proto 演进规则的真实运行验证、升级/回退顺序）：[设计](docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md)。
- **A04** Direct 模式边界核实（代码中不存在，定义允许列表/凭据/SSRF 边界）：[设计](docs/superpowers/specs/2026-09-15-a04-direct-mode-boundary-design.md)。
- **A05** 调度扩展拆分（六项后续任务）：[设计](docs/superpowers/specs/2026-09-15-a05-scheduling-extension-breakdown-design.md)。
- **A06** ComfyUI 专项指标（队列、时长/成功率、OOM、产物传输、存储用量）：见 [Gateway STATUS「工作流 Job」第 16/17 条](service/aiServeWeaveGateway/STATUS.md#工作流-job)。

### P2：后续扩展

- **API 兼容边界**（[设计](docs/superpowers/specs/2026-09-16-p2-api-compat-boundary-design.md)）：[Anthropic Messages](service/aiServeWeaveGateway/STATUS.md#anthropic-messages)（含工具调用）、[Ollama 原生 API](service/aiServeWeaveGateway/STATUS.md#ollama-原生-api)、[音频转录与翻译](service/aiServeWeaveGateway/STATUS.md#音频转录与翻译)、[Rerank](service/aiServeWeaveGateway/STATUS.md#rerank)、`ChatMessage.ContentParts` 结构化内容（图片、音频、文件/文档）。
- **图像生成、Responses 持久会话、多模态输入**（[设计](docs/superpowers/specs/2026-09-16-p2-images-responses-multimodal-boundary-design.md)）：`/v1/images/generations` 映射到受控 ComfyUI 模板；`store`/`previous_response_id` 见 [Gateway](service/aiServeWeaveGateway/STATUS.md#responses-持久会话p2) 与 [ControlPlane](service/aiServeWeaveControlPlane/STATUS.md#responses-持久会话p2)。
- **模型分发**（[设计](docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md)）：本地清单驱动的校验和/断点续传下载、按名字触发与状态回传、控制面跨副本转发、Ollama 原生拉取、并发与跨重启配额账本、Console `/operator/model-pulls`。见 [Agent STATUS](service/aiServeWeaveAgent/STATUS.md#当前实现)、[Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#模型拉取触发statusmd-的-p2模型分发子任务二)、[ControlPlane STATUS](service/aiServeWeaveControlPlane/STATUS.md#模型拉取转发p2-模型分发子任务二的控制面转发层)。
- **资源采集与资源感知调度**（[设计](docs/superpowers/specs/2026-09-15-p2-resource-aware-scheduling-design.md)、[实时利用率评估](docs/superpowers/specs/2026-09-16-p2-realtime-utilization-necessity-design.md)）：Hello 前的静态资源探测、`min_gpu_memory_bytes` 准入过滤（含工作流路径）、ComfyUI 队列占用排序、工作流有界排队，见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#工作流-job-的有界排队statusmd-的-p2)。
- **持久化用量账本**：按租户/模型的 token 用量，`request_id` 幂等去重，Console `/console/usage`、`/operator/usage`；可选的预付点数计费（Dodo Payments 充值、按模型单价扣费、余额耗尽 402）见 [ControlPlane「计费与充值」](service/aiServeWeaveControlPlane/STATUS.md#计费与充值)。见 [Gateway](service/aiServeWeaveGateway/STATUS.md#按租户模型的持久化用量账本statusmd-的-p2) 与 [ControlPlane](service/aiServeWeaveControlPlane/STATUS.md#用量账本statusmd-的-p2)。
- **独立 Tunnel Gateway 与事件基础设施评估**：结论是现在不拆，见 [评估](docs/superpowers/specs/2026-09-18-p2-tunnel-gateway-event-infra-evaluation.md)。
- **目标内排序策略**：`-scheduler-policy`（`least_load`/`power_of_two`/`prefix_hash`），见 [Gateway STATUS「模型别名与节点标签」第 6 条](service/aiServeWeaveGateway/STATUS.md#模型别名与节点标签)。
- **推理内容透传**：契约（proto、`runtime.ChatMessage`/`ChatMessageDelta`/`Usage`、`tunnelwire`）新增思考内容与思考 token；`openai` 适配器解析 `reasoning_content`/`reasoning`，`anthropic` 适配器解析 `thinking` 块；四个前门各按自己协议输出，客户端回传的思考不转发。见 [Gateway STATUS「Responses API」](service/aiServeWeaveGateway/STATUS.md#responses-api)、[Anthropic Messages](service/aiServeWeaveGateway/STATUS.md#anthropic-messages)、[Ollama 原生 API](service/aiServeWeaveGateway/STATUS.md#ollama-原生-api)。请求侧开关（Chat `reasoning_effort`、Responses `reasoning`、Anthropic `thinking`、Ollama `think`）按提示语义映射到各后端；Anthropic 签名块往返回放；`reasoning_parser` 兜底切分内联 `<think>`，见 [runtime README「思考开关与兜底解析器」](common/runtime/README.md#思考开关与兜底解析器)。
- **TensorRT-LLM 与 TokenSpeed 本地后端**：见 [runtime README](common/runtime/README.md#tensorrt-llm)。

## 状态维护与完成标准

任务完成后同时更新本文和所属服务文档：本文只写一行交付摘要、遗留缺口与链接，实现细节写进服务 STATUS 或设计文档。公共接口变化同步更新契约与 API 说明。勾选须有实现、适用测试及可复现的验收依据，不能仅凭依赖安装、建表或配置存在认定完成；缺真实环境验证的，写进上方「未完成的真实环境验证」。涉及 Go 代码执行根 AGENTS 的全部质量门禁，涉及 Console 同时执行其专属门禁。

各服务的详细状态：

- [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md)：Job 内存边界、状态观测、产物与路由配置。
- [ControlPlane STATUS](service/aiServeWeaveControlPlane/STATUS.md)：历史任务、管理写路径、数据库迁移与监控。
- [Registry STATUS](service/aiServeWeaveRegistry/STATUS.md)：身份冲突、token 存储、单实例与认证边界。
- [Agent STATUS](service/aiServeWeaveAgent/STATUS.md)：本地后端、模型拉取、Managed ComfyUI 与自动升级。
- [Console STATUS](service/aiServeWeaveConsole/STATUS.md)：C 系列子任务与 Q03 验收。

### Agent 独立主机部署（2026-10-30）

Ubuntu systemd、Apple Silicon macOS LaunchAgent、Windows 11 原生用户计划任务的安装维护脚本及 Windows ACL/CLI Job/本地控制适配已实现；入口见 [Agent 独立部署](deploy/agent/README.md)。支持指定版本在线/本地来源、制品校验、显式升级/回退与保留状态卸载。操作系统矩阵、真实 HTTP/API 和 Codex/Claude 验收仍待逐组合执行；[验证记录](deploy/agent/acceptance.md) 分开记录构建、默认检查与实机结果。

# A01 支持范围与可靠性目标

本文档交付根 STATUS.md「规划补充」A01：定义节点数、并发、任务/产物容量、API 兼容矩阵，以及可用性、延迟、RPO/RTO 的目标与测量方法，覆盖控制面、数据库、Redis、Registry 和节点分别故障时的行为。与 J01（"本项仅完成设计,不含建表与代码"）同一先例，A01 本身不新增代码，只新增本文档；后续如果某一项缺口需要补测试或补实现，另开任务号跟踪，不在这里顺手做掉。

## 方法论

STATUS.md 对 A01 的验收要求是"数值经测试确认，不先写未经验证的承诺"。本文档因此对每一个数字标注来源类别，三选一：

- **硬约束（代码强制）**：代码里存在会拒绝/丢弃超限请求的判断逻辑，给出 `文件:行号`。
- **已验证（有实测证据）**：在真实环境或集成测试中跑出过的结果，给出证据来源（`docs/acceptance/` 下的验收记录、或已合入的 live test）。
- **设计意图/占位默认值（待验证）**：代码里有一个可配置的默认常量，但没有真实生产负载或多节点规模下的验证——这类数字不当作目标承诺，只作为当前行为的如实记录，并给出后续验证方法。

不属于以上三类、只在 README/STATUS 散文里出现过的数字，本文档不引用为依据，直接列入第四节「已知缺口」。

## 一、容量与并发目标

### 1.1 节点与隧道

| 项目 | 数值 | 类别 | 依据 |
| --- | --- | --- | --- |
| 每 Agent 副本保有的最小空闲槽 `min_slots` | 2 | 硬约束（配置默认，可覆盖） | `service/aiServeWeaveAgent/tunnel/pool.go:38` |
| 槽位补充水位 `low_watermark` | 1 | 同上 | `pool.go:39` |
| 批量（产物传输）专用槽 `bulk_slots` | 1 | 同上 | `pool.go:40` |
| 单节点总槽数 `node_total_slots` | 32 | **设计意图/占位默认值** | `pool.go:41`；README 与代码均明确标注"真实数值有待生产吞吐量数据"，不是校准过的容量数字 |
| 槽位空闲回收超时 | 5 分钟 | 硬约束（配置默认） | `pool.go:42` |
| 单槽最大请求数（超过后主动重建） | 200 | 同上 | `pool.go:43` |
| 单槽最大存活时间 | 1 小时 | 同上 | `pool.go:92`（`MaxSlotAge`） |
| 单个 Agent 最多跟踪的 Gateway 副本数 `max_gateways` | 16 | 硬约束，防 FD 耗尽 | `service/aiServeWeaveAgent/tunnel/roster.go:37` |
| 每副本槽位分配公式 | `max(min_slots, ceil(node_total_slots / 活跃副本数))` | 已验证（1/3/100 副本场景测过公式本身） | 隧道 README:460, 816 |
| 隧道帧大小上限（Agent/Gateway 两侧必须一致） | 4 MiB | 硬约束 | `tunnel/dispatch.go:44`、`tunnelserver/server.go:81` |
| 单请求整体字节上限（工作流提交/上传） | 64 MiB | 硬约束 | `tunnel/dispatch.go:45` |
| 应用层心跳间隔/阈值 | 15s / 连续 3 次 | 硬约束（配置默认） | `tunnel/client.go:36-37` |
| Gateway 侧心跳超时 | 45s | 硬约束（配置默认） | `tunnelserver/server.go:76-78` |

**已测试的拓扑规模**：Gateway e2e 长稳测试用 3 副本 × 每副本 4 推理槽（`fleetMaxInferenceSlots=4`）作为唯一有测试覆盖的"多节点/多副本"具体数字（`e2e/soak_test.go:55`、`e2e/fleet_test.go:29`）——这是测试夹具规模，不是生产目标声明。

**目标（待验证）**：单 Gateway 副本或整个机群可接受的节点（Agent）总数、单节点可承载的并发推理请求数，代码里都没有强制上限，也没有真实多节点规模下的实测数据。在补齐真实硬件/多节点环境验证之前，A01 不对这两个维度写具体承诺数字，运维部署时应按 `node_total_slots` 与推理后端自身的 `MaxConcurrent` 逐节点核算，而非套用本文档的数字。

### 1.2 Job 与产物

| 项目 | 数值 | 类别 | 依据 |
| --- | --- | --- | --- |
| Gateway 单副本内存 job 表上限 | 10000（超出后淘汰最旧记录） | 硬约束（构造参数可调） | `service/aiServeWeaveGateway/httpapi/jobstore.go:23` |
| 单个路由绑定（node/runtime）恢复时一次最多取回的非终态 job | 500，按 `created_at` 最旧优先，非分页 | 硬约束 | ControlPlane README「已实现的重启恢复（J06）」，`store.MaxActiveJobsForRoute` |
| 产物过期清理批量上限 | 200 | 硬约束 | `service/aiServeWeaveControlPlane/internal/store/store.go:508`（`MaxExpiredJobArtifacts`） |
| 输出产物保留期 | 30 天 | 硬约束（配置默认） | `service/aiServeWeaveGateway/httpapi/artifactcleanup.go:24` |
| 临时/预览产物保留期 | 24 小时 | 同上 | `artifactcleanup.go:25` |
| 上传字节嗅探窗口 | 512 字节 | 硬约束（与 `net/http.DetectContentType` 文档窗口一致） | `service/aiServeWeaveGateway/httpapi/uploadformat.go:60` |
| 默认允许的上传扩展名 | 13 种图片/视频/音频格式 | 硬约束（配置默认，部署可覆盖） | `uploadformat.go:24-27` |

**目标（待验证）**：单租户可并行运行的 job 数量、机群级总吞吐——`common/quota.Limits` 中的 `MaxConcurrent` 字段存在但没有任何内置默认值（零值即不限），任何具体配额数字都需要运维按自己的容量显式配置，A01 不代其设定默认值。

### 1.3 模型路由与工作流模板（P02/P03，已是硬约束）

| 项目 | 数值 | 依据 |
| --- | --- | --- |
| 单版本路由文档大小 | 1 MiB（含开销上限约 1.0625 MiB） | `common/modelroute/modelroute.go:16,19` |
| 别名数上限 | 1000 | `modelroute.go:22` |
| 每别名目标数上限 | 100 | `modelroute.go:25` |
| 路由历史版本上限 | 1000 | `modelroute.go:28` |
| 工作流模板图大小上限 | 4 MiB（含开销上限 4 MiB + 256 KiB） | `common/workflowtemplate/workflowtemplate.go:53,61` |
| 单模板输入/输出/自定义节点依赖/模型依赖上限 | 100 / 20 / 100 / 100 | `workflowtemplate.go:64,67,70,73` |
| 平台模板 id 总数上限 | 500 | `workflowtemplate.go:79` |
| 单模板历史版本上限 | 1000 | `workflowtemplate.go:87` |

这组数字全部是代码强制（超限直接 4xx 拒绝），可以直接作为对外承诺的容量边界，不需要额外验证。

### 1.4 会话与连接

| 项目 | 数值 | 类别 | 依据 |
| --- | --- | --- | --- |
| 每账户并发会话上限 | 20，第 21 个挤掉最早到期者（非拒绝） | 硬约束，真实 PostgreSQL/MySQL 并发验证过 | `session/session.go:51`；ControlPlane README:226 |
| 账户变更（改密/改角色/禁用）的短期登录门 | 30 秒 | 硬约束 | `session/session.go:53` |
| 控制面数据库连接池（每副本） | `MaxOpenConns=20` / `MaxIdleConns=5` / `ConnMaxLifetime=30min` | 设计意图（显式设置以避免 gorm 默认无界，未与部署侧数据库 `max_connections` 联动核算） | `internal/config/config.go:339-346` |

**已知缺口**：仓库里没有任何地方把"每副本连接池大小 × 副本数"与数据库自身的 `max_connections` 对账——多副本部署时这项算术需要运维自己做，A01 在此不提供默认建议值，只如实记录缺口（见第四节）。

## 二、API 兼容矩阵

真正被代码强制的能力矩阵是 `common/runtime.Capability` 枚举（15 个值：`CapabilityChat`、`CapabilityChatStream`、`CapabilityCompletions`、`CapabilityEmbeddings`、`CapabilityResponses`、`CapabilityVision`、`CapabilityTools`、`CapabilityParallelToolCalls`、`CapabilityStructuredOutput`、`CapabilityReasoning`、`CapabilityWorkflowExecution`、`CapabilityWorkflowEvents`、`CapabilityWorkflowCancel`、`CapabilityArtifactRead`、`CapabilityInputWrite`，`common/runtime/capability.go:11-25`）。根 README 里的 `DeploymentCapability` 结构体（README.md:230-249）是尚未落地的伪代码草案，字段比实际实现更宽（例如 `image_generation`、`audio`、`video` 目前都不存在对应的 `Capability` 值），**A01 明确以 `capability.go` 为准，不采用 README 那段伪代码作为已实现的矩阵**；这算是 R04 式的一处文档与代码不一致，已经在此指出，后续修 README 时应把那段伪代码要么删除要么明确标成路线图。

### 已实现的前门端点

| 端点 | 状态 | 备注 |
| --- | --- | --- |
| `POST /v1/chat/completions`（含 SSE） | 已实现 | |
| `POST /v1/responses`（含 SSE） | 已实现，内部按 `CapabilityChat` 转码执行 | `CapabilityResponses` 这个枚举值目前没有任何后端路径实际使用（README:206-208），Responses 是前门转码进 Chat，不是原生转发 |
| `POST /v1/embeddings` | 已实现 | |
| `GET /v1/models` | 已实现 | |

### Responses 端点明确拒绝的字段（400，非静默丢弃）

| 字段 | 拒绝原因 |
| --- | --- |
| `previous_response_id` | Gateway 不持有历史响应，也不做节点粘性调度 |
| `store` / `background` | 需要跨请求的服务端状态，当前不提供 |
| 内置工具（`web_search`/`file_search`/`code_interpreter`/`mcp`） | 由 OpenAI 自己的基础设施执行，本网关只转发到某个模型 |
| 图片/音频/文件类输入部分 | 代码里尚无这些输入类型的规范化表示 |

`usage` 字段在后端未上报时是**省略**而非置零——调用方不应把缺失字段误判为零用量。

### 明确列为未支持（规划中，无任何代码支撑）

Anthropic `POST /v1/messages`、Ollama 原生 API、音频转录/翻译、rerank、OpenAI 兼容图像生成——这些都在根 README「规划中的扩展协议范围」提及，但**没有任何 enforcing 代码**，属于 STATUS.md P2 范畴。任何对外的兼容性声明都不应包含它们，直至各自被拆分成具体任务实现。

## 三、可用性、延迟与 RPO/RTO：按故障域

### 3.1 控制面（ControlPlane）故障

推理请求路径与控制面持久化是解耦的旁路关系，这是 J01 定下、J02–J06 遵守的核心设计约束（ControlPlane README「数据库故障不拖垮普通推理链路」）：

| 操作 | 控制面不可达时的行为 | 类别 |
| --- | --- | --- |
| 提交 job | 推理请求不失败，`run_id` 照常返回；持久化记为"结果未知"，由 J05 补写机制在后台重试，绝不在请求路径上等待或重提 | 硬约束（架构层面） |
| 状态更新/轮询/SSE | 完全不受影响，Gateway 内存/节点直连继续应答 | 硬约束 |
| 取消/产物访问（非重启场景） | 直连节点，不经过控制面 | 硬约束 |
| 提交结果未知的补写窗口 | 初始间隔 5s，失败按 2 的幂次退避，上限 5 分钟；重试状态存在 Gateway 内存里，**副本自身重启即丢**，且受 `DefaultMaxJobs=10000` 逐出上限约束 | 设计意图，已如实记录为已知边界，非"不丢"承诺 |

**RTO（Gateway 副本重启后的 job 恢复）**：`jobRecoverer` 每 30 秒扫描一次已连接节点/runtime 上的非终态 job（`GET /internal/v1/jobs/active`），这决定了一次副本重启后"重新知道自己欠了什么"的上界约为 30 秒，是设计参数而非实测 RTO——目前没有真实环境下从进程崩溃到完全恢复的端到端计时记录（见第四节缺口 1）。

**RPO（未持久化即丢失的窗口）**：一个 job 的状态变化如果只停留在 Gateway 内存、还没被 `jobPersister` 追平到控制面时该副本进程崩溃，这段变化永久丢失；窗口大小取决于 `jobPersister` 的补写节奏（5 秒基础间隔，失败时最长 5 分钟）与逐出策略的组合，没有硬性上限承诺——这是仓库明确写下的"尽力而为的旁路，不是可靠队列"，A01 如实继承这一立场，不额外承诺更强的 RPO。

**吊销/配额生效延迟**：Key 吊销经 Redis Lua 原子 `INCR + PUBLISH` 通知，长轮询心跳 2 秒（`service/aiServeWeaveControlPlane/internal/handler/revocations.go:12`），Gateway 侧 5 秒静默断线检测（`controlplaneclient/revocations.go:18`）。"两 Gateway 1 秒内感知"这个数字来自 `revocations_test.go:410-450` 的 `TestEveryVerifierInvalidatesWithinHealthyDeadline`——这是针对**内存假控制面**的测试断言上界，不是真实网络环境下测得的传播延迟，A01 不把它当作生产延迟指标引用（见第四节缺口 2）。

### 3.2 数据库（Postgres/MySQL）故障

| 场景 | 行为 | 类别 |
| --- | --- | --- |
| 数据库不可达 | 控制面各写路径快速失败，不做无界重试或排队 | 已验证（J08 真实 MySQL 9.7 集成测试） |
| 迁移锁竞争 | Postgres 用 `pg_try_advisory_lock`（非阻塞尝试），MySQL 用 `GET_LOCK(..., 0)`（0 秒超时即非阻塞尝试），拿不到锁立即返回错误提示稍后重试，没有可配置的等待超时 | 硬约束 |
| 并发状态更新冲突（同一 job 被多个来源更新） | 由数据库行锁在一条 `UPDATE ... WHERE state NOT IN (...) AND observed_seq < ?` 语句内裁定，不是应用层先读后写 | 已验证（真实 MySQL race 测试） |
| 原生备份恢复 | 逐表一致性已在真实 PostgreSQL 18 / MySQL 9.7 上验证过（P07） | 已验证 |
| 迁移可重复执行 | 有版本账本 + SHA-256 校验 + dirty 标记，验证过部分失败后 resume | 已验证（P07 真实数据库） |

**RPO/RTO**：数据库层面的 RPO 由底层数据库的备份策略与 WAL/binlog 保留期决定，本仓库负责的是"恢复后应用层能正确识别版本、拒绝结构漂移、支持 resume"，不负责数据库自身的连续归档策略——这部分留给部署侧按 `deploy/database-recovery.md` 的操作步骤执行，A01 不重复定义数据库厂商已有的 RPO 概念。RTO 目前只有"迁移与恢复步骤可执行"的验证，没有端到端计时（例如"数据库从故障到控制面恢复服务需要 N 分钟"），这是缺口（见第四节缺口 3）。

### 3.3 Redis 故障

Redis 在这套系统里身兼三个角色，故障行为分别独立：

| 角色 | Redis 不可用时的行为 | 类别 |
| --- | --- | --- |
| 会话权威存储（控制面登录/管理） | Fail closed：返回 503，不降级为只验 JWT | 已验证，ControlPlane README:226 |
| Key 吊销通知（Gateway 正向缓存） | 通知链路不健康时清空并停用本地缓存，逐请求回源到控制面校验；仍保持既有 503 语义而非放行未经校验的请求 | 已验证，ControlPlane README:67 |
| 跨副本限流计数 | `-redis-addr` 留空时退回每副本独立内存计数——**多副本部署下会放行 N 倍额度**，这是文档记录的既定代价，不是故障，是配置缺省的直接后果 | 硬约束/设计已知代价，`service/aiServeWeaveGateway/main.go:679` |

Redis 中途断开又恢复的补偿机制：本地 epoch 阻止用一次过期的正向验证结果回填缓存；漏收到的 Pub/Sub 通知、重连、控制面副本切换，都由 Gateway 侧持有的游标（generation）在下一次成功轮询时补偿，不依赖"不能丢消息"这个前提。

**RPO/RTO**：会话与吊销两条路径都是 fail closed，代价是可用性下降换取正确性，因此这两条路径的"RPO"概念不适用（没有数据丢失，只有服务暂时不可用）；限流路径没有 fail closed，是已知精度下降而非中断。目前没有测过"Redis 从故障到恢复，服务从 503 恢复正常需要多久"的真实计时（见第四节缺口 4）。

### 3.4 Registry 故障

Registry README 明确写着"单实例假设"（157-159 行）：bootstrap token 校验与 `node_id` 账本靠本地文件 + 内存锁保证强一致，只在单进程下成立，高可用留在 STATUS.md 的 P2。据此可以推出但仓库尚未显式验证的行为边界：

| 场景 | 推断行为 | 类别 |
| --- | --- | --- |
| 已建立的 Agent↔Gateway mTLS 隧道 | 不依赖 Registry 持续在线——证书已经签发（30 天有效期，`internal/ca/ca.go:32`），隧道本身走 Gateway/Agent 直连 | 设计推断，未见专门的故障注入测试验证 |
| 新节点注册 / 证书续期 | 阻塞，直到 Registry 恢复 | 设计推断（依赖 `Register`/`RenewCertificate` 走 Registry gRPC） |
| 节点禁用/启用、吊销传播（S03） | 阻塞——依赖 `GatewayRoster` 广播，Registry 是这条链路唯一来源 | 设计推断 |
| Gateway 加入名册（`GatewayDirectory.Join`） | 阻塞，直到 Registry 恢复 | 设计推断 |

这一整块是**设计推断而非已验证行为**——仓库里没有一次真实的"关掉 Registry 进程，观察已连接隧道和新注册请求分别怎样"的故障注入测试。证书 30 天有效期给了较大的容忍窗口（远超一次运维故障处理的合理时长），但这不等于验证过。RTO 是纯运维问题（重启单进程 Registry），没有单独的代码级恢复逻辑需要测量。这是明确的缺口（见第四节缺口 5）。

### 3.5 节点（Agent）故障

| 场景 | 行为 | 类别 |
| --- | --- | --- |
| 连续请求失败触发熔断 | 阈值 5 次连续失败，冷却 5s 起、封顶 2 分钟；`ErrorBackpressure`/`ErrorRateLimited` 明确不计入失败 | 硬约束（配置默认），单机单节点场景下已用真实 Ollama 校准过（见下） |
| 节点失联后的 job 记录 | 停在最后观测到的状态，不编造终态；只要节点还没被判定失联，其他仍在线副本各自独立同步/持久化同一个 job，无需协调（`observed_seq` 单调门槛天然化解并发） | 硬约束（架构设计） |
| 节点被禁用（S03） | Gateway 强制结束其 `Control` 流、清空空闲槽，握手对被禁用 `node_id` 直接拒绝；忙碌槽上正在处理的请求不受打断 | 硬约束，最终一致（副本收到最新名册前仍可能认为该节点合法） |

**已验证（单机单节点单后端）**：2026-09-13 熔断校准（`docs/acceptance/p10-breaker-calibration-2026-09-13/README.md`），单机 ControlPlane+Registry+真实 Agent+真实 Ollama（gemma4:26b）：

- 稳定负载（并发 2）：默认参数（5/5s/2m）成功率 99.71%（345 样本），零熔断触发。
- 中度饱和（并发 6）：成功率 100%（171 样本），零熔断触发。
- 硬饱和（并发 14，客户端限速）：成功率 3.48%（4945 样本），零熔断触发——`ErrorBackpressure` 被正确排除在熔断统计外。
- 真实 Ollama 断开/恢复：默认参数在断开后 ≤20 秒内触发熔断，恢复后 20–40 秒内自动恢复。
- 结论：保留默认值（5/5s/2m），不调整。

**明确声明的边界**：以上全部结果是单机/单副本/单节点规模，验收记录原文自称"不扩大为生产容量、SLO 或跨版本兼容承诺"。多节点、多副本、真实网络分区下的熔断/恢复行为没有测过（见第四节缺口 6）。

## 四、已知缺口清单（供后续任务拆分）

以下每一项目前都没有满足"数值经测试确认"的门槛，A01 不代为承诺具体数字，如实列出，供后续按需拆成独立任务（多数天然落在 A02/A05 或专项验收范围内）：

1. **控制面 RTO 未实测**：没有一次真实的"杀掉 ControlPlane 进程/容器，测量到服务完全恢复所需时间"的记录；`jobRecoverer` 30 秒扫描间隔是设计参数，不是实测 RTO。
2. **吊销/配额传播延迟只有合成测试证据**：`TestEveryVerifierInvalidatesWithinHealthyDeadline` 断言的"1 秒"是内存假控制面上的测试上界，不是真实网络环境测得的数字。
3. **数据库故障端到端 RTO 未测**：P07 验证了迁移/恢复步骤本身可执行、可重复，但没有"从数据库故障到应用层完全恢复"的整体计时。
4. **Redis 故障端到端恢复时长未测**：会话与吊销两条 fail-closed 路径验证过行为正确（返回 503、不放行未经校验请求），没有测过"Redis 恢复后到服务恢复正常"的具体时长。
5. **Registry 故障行为是设计推断，没有故障注入测试**：已建立隧道是否真的完全不受影响、新注册/禁用广播阻塞多久、Registry 恢复后名册重新同步需要多久，都没有专门测试覆盖。
6. **熔断/恢复只在单机单节点规模验证过**：多副本、多节点、真实网络分区下的熔断触发与恢复行为未知。
7. **`node_total_slots=32` 是未经生产验证的占位默认值**：不应被引用为容量规划的依据。
8. **没有"节点数/机群规模"上限**：既没有单 Gateway 副本可接受的节点数上限，也没有整个机群的节点数上限，代码和文档都没有给出依据。
9. **应用层连接池与数据库 `max_connections` 未联动核算**：多副本部署时，`MaxOpenConns=20` × 副本数是否会超出数据库自身连接上限，需要运维手工核算，仓库未提供换算工具或建议值。
10. **`common/quota.Limits` 没有任何内置默认值**：零值即不限，任何具体配额数字（如"每租户默认 60 req/min"）都是全新数字，不是从现有代码或测试中提炼的。
11. **24 小时以上的真实长稳结果不存在**：`docs/acceptance/` 下只有 2 分钟合成长稳与单机熔断校准两份记录；24 小时长稳自 P10 起改为运维自行部署后验证，不再要求归档到本仓库，因此可用性/MTTR 一类跨天级别的数字目前没有任何依据。

## 五、与 STATUS.md 的关系

本文档本身即视为 A01 的完成交付：定义已经给出，测量方法已经给出，凡有测试或代码依据的数字都已引用到具体行号或验收记录，凡没有依据的一律列入第四节，不作为承诺。STATUS.md 的 A01 行据此勾选为完成，并链接回本文档；第四节的缺口清单不阻塞勾选，与 R04/P09 等既有条目"已知限制不影响完成判定，但必须如实记录"的先例一致。后续 A02（灾难恢复演练）、A05（调度扩展）如果需要补齐第四节里的某一项测量，应在各自任务里引用本文档对应缺口编号，而不是重新调查一遍。

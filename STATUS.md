# 按 token 计价：官方价 × 倍率（2026-11-16）

点数计费改为参照 [sub2api](https://github.com/Wei-Shaw/sub2api) 的计价方式：**售价 = 基础价 × 模型倍率 × 方案系数**，1 点 = 1 美元。规则、运维设置与升级步骤见[按 token 计价](docs/billing-token-pricing.md)，设计与取舍见 `.scratch/token-pricing/`。

- **计量项**：用量按 `input`、`input.cache_read`、`input.cache_write`（含 `.1h`）、`output`、`output.reasoning` 分别计价；子项没有单价时按父项计费，不会少收。支持长上下文档位。新增计量项不必改协议，也不必改表。
- **价目表**：内置 LiteLLM 价目表快照，按配置定时同步；新条目自动生效，大幅改价、降为 0 与删除等运维审核。
- **定价配置与价格方案**：按别名设置参照模型、覆盖价、模型倍率；默认价；价格方案（默认系数、按别名的系数或固定售价），租户属于一个方案。每次修改追加版本，**按请求时间计价**。
- **每笔费用明细**：用量记录保存原价、实扣、倍率、价格来源与各计量项的单价和费用。
- **扣费防重**改由按月分区的 `usage_records` 承担，`usage_charges` 不再写入（它不分区也不清理，会无限增长）。
- **余额账目**：扣费累计在租户计费行上，按 `Billing.LedgerTimezone` 每天封成一条用量账目，遇到其他资金事件先封账；账目只追加、不修改。
- **上报链路**：Anthropic / OpenAI adapter、隧道协议、Agent 的 Claude 风格 / Cursor / Codex CLI runtime 上报缓存与推理明细；Gateway 推送前过滤。**修正既有漏计**：Gateway 原生 Messages 路径（Claude CLI 桥接）原本漏计缓存读取与写入。
- **查询接口**：用量汇总新增缓存与费用列、逐笔明细、租户价格、运维有效价格与未定价提示；公开目录改为默认方案下的售价。旧的 `/operator/v1/billing/model-prices` 已移除。
- **Console**：运维的模型定价、价目表审核、价格方案；租户的用量费用与逐笔明细、模型价格、账目「进行中」与时间段，见 [Console STATUS](service/aiServeWeaveConsole/STATUS.md)。

升级须知：

- **顺序**：先控制面与 Console（一起），再 Gateway，最后 Agent。用量推送新增 `details`、`attributes` 字段，旧控制面会拒绝整批。
- **迁移**：`usage_records/0005`（即时加列）、`usage_records/0006`（索引 `(tenant_id, created_at, id)`：**PostgreSQL 建索引期间阻塞用量写入**，MySQL 在线执行；大表须先在恢复副本上计时）、`billing/0012`–`0014`。`sql/init.sql` 已同步。
- **行为变更**：名称与价目表条目完全一致、原本落到 `*` 默认价的别名，升级后按官方价收费；1 点 = 1 美元成为定价基准，请重新核对充值包；已有 `model_prices` 行转换为覆盖价，价格不变；**Claude CLI 桥接的用量与费用会明显增加**（缓存 token 开始计入），升级前请通知相关商户。
- **不能直接回退**到旧版控制面（旧版只查 `usage_charges` 防重，重发的记录会被重复扣费），回退须恢复备份。
- **可选维护命令**：`-migrate backfill-usage-cost`（把升级前费用写回 `usage_records`）、`-migrate compact-usage-ledger`（按天合并升级前的用量账目，会改写财务记录，须先备份）。
- **新配置**：`Billing.PriceCatalog`（`URL`、`RefreshInterval`、`AutoApplyMaxChangePercent`）、`Billing.LedgerTimezone`（默认 UTC，台湾或大陆市场建议 `Asia/Taipei` / `Asia/Shanghai`）。

已通过的检查：

- 全仓 `go vet` 与默认测试；`common/pricing`、控制面 store、logic、handler、config、e2e 与 Gateway `httpapi` / `controlplaneclient` 的竞态检查。
- 临时 PostgreSQL 16 与 MySQL 9.7 上 gormstore 全部 live 用例，新增：价目表（含按完整快照导入）、定价配置、扣费防重（含跨月分区重发与并发推送）、余额链（含同一微秒、时钟倒退、扣费 / 入账 / 封账并发、升级前的旧租户）、回填与压缩命令、用量查询；分区升级用例同步了新列。
- 计价引擎单元测试含规格的计算示例；上游 LiteLLM 原始文件（5993 个条目）解析与过滤。
- Console 409 项单元测试、类型检查、lint 与生产构建；Playwright 新增运维定价与租户用量用例，与计费相关用例合跑 181 项通过。`billing.spec.ts:382` 失败：断言的文案在 HEAD 中就不存在，与本次无关。

尚未验收：

- 真实控制面、Gateway、Agent 与 Console 的端到端计价联调。
- 真实 Claude Code、Codex CLI 上报的缓存字段（尤其是 1 小时缓存）。
- 从真实网络同步上游价目表。
- 生产规模数据上 `usage_records/0006` 的建索引耗时。

# Studio 首页与会话鉴权（2026-11-16）

新服务 [Studio](service/aiServeWeaveStudio/README.md)：面向租户成员的 AI 工作台，规格见 `.scratch/studio-chat-flows/spec.md`。本次交付首页与它所需的后端：

- **控制面**：新增内部接口 `POST /internal/v1/sessions/verify`，校验 Console 会话并返回与 API Key 同形的租户授权，外加用户与角色。租户授权的计算抽成 `VerifyTenant`，由 key 校验与会话校验共用。
- **Gateway**：新增开关 `-studio`，挂载 `/studio/v1`（`me`、`models`）。会话令牌与 API Key 互不通用。会话校验结果最多缓存 5 秒，见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#studio-api-与会话鉴权2026-11-16)。
- **Studio**：登录与登出、首页（侧边栏、问候、输入框、「+」菜单上传文件、模型选择）。**发送消息尚未接入**。

升级须知：先升级控制面，再以 `-studio` 启动 Gateway。不开启 `-studio` 时行为不变。

已通过的检查：

- Gateway 与控制面的默认测试。新增用例：
  - Gateway Studio 前门的鉴权矩阵、两扇前门凭据隔离、未开启时不挂载、模型授权过滤；
  - 控制面 e2e：真实登录 → Gateway 客户端校验 → 登出后失效，以及平台运维、伪造令牌与 API Key 被拒。
- Studio 的 lint、typecheck、18 项单元测试和生产构建。
- 用桩上游在无头 Chromium 中走通登录、首页、附件、模型选择与退出流程。

尚未验收：真实控制面、Gateway 与 Studio 的联调。

# 用量按用户与 API Key 归属（2026-11-15）

用量账本新增 key 与用户维度，可看每个用户、每个 key、每个模型的输入与输出 token：

- **Gateway**：用量记录附发起请求的 `key_id`。
- **控制面**：`usage_records/0003` 新增 `key_id`、`user_id` 两列；入账时按 key 查创建者写入 `user_id`，之后删除 key 或用户不影响归属。`/admin/v1/usage/summary` 与 `/operator/v1/usage/summary` 新增 `group_by=key`，按 `(tenant, user, key, model)` 分组。
- **Console**：租户「用量」页改为：时间范围预设（全部 / 近 7 天 / 近 30 天 / 自定义）、四项合计卡片（输入、输出、总 token、请求数），以及「按模型 / 按用户 / 按 Key」三个标签页；名称取自用户与 key 列表首页（200 条），找不到的显示 id。`/api/admin/*` 与 `/api/operator/*` 的用量汇总白名单放行 `group_by`（首次上线漏放，按 Key 统计实际拿到的是按模型数据）。
- **Console**：「API Key」页与「用户」页新增「Token 用量」列，按模型列出该 key / 该用户的全部输入与输出 token（不限时间窗口）。

升级须知：**先升级控制面，再升级 Gateway**（旧控制面会拒绝带 `key_id` 的批次并丢弃）。迁移只加两列带默认值的列；升级前的历史用量显示为「未归属」。`sql/init.sql` 已同步。

已通过的检查：控制面与 Gateway 默认测试；临时 MySQL 9.7 与 PostgreSQL 16 上 gormstore 全部 live 用例（含升级前数据保留与按 key 分组查询）；Console typecheck、lint 与单元测试。

尚未验收：真实环境端到端（Gateway 推送 → Console 显示）。

# 增长表按时间分区（2026-11-14）

控制面五张持续增长的表改为按时间 RANGE 分区，MySQL 9.7 与 PostgreSQL 都做，两边表结构一致：

| 表 | 分区 | 保留期（默认） |
| --- | --- | --- |
| `request_logs` | 按天，`created_at` | 30 天 |
| `usage_records` | 按月，`created_at` | 400 天 |
| `metrics_history_points` | 按天，`bucket_at` | 90 天（启用指标历史时） |
| `response_turns` | 按天，`created_at` | 30 天（新增 `ResponseTurnRetention`） |
| `audit_logs` | 按月，`created_at` | 400 天（新增 `AuditLogRetention`） |

- **清理（至少保留 N 天）**：每小时运行，只整段删除过期分区，截止时间所在的分区整段保留（最多多留一个分区周期：按天的表 1 天，按月的表约 1 个月）。稳态下没有逐行删除；零散迟到行分批删除，每批 5000 行。
- **维护**：控制面启动时及之后每小时预建未来分区（按天 7 天、按月 2 个月），多副本之间用迁移锁串行；每张表有兜底分区，维护落后时写入不会失败。兜底分区积压超过 20 万行时，定时维护不搬移，交给 `-migrate up` 在低峰期完成。
- **维护账号**：新增可选的 `Database.MaintenanceDSN`，生产运行账号没有 DDL 权限时，分区 DDL 走这个账号。分区 DDL 失败时，保留期清理退回分批逐行删除，不会停摆。
- **配置**：新增 `ResponseTurnRetention`、`AuditLogRetention`、`Database.MigrationTimeout`、`Database.MaintenanceDSN`；按天分区的表保留期上限 3650 天。
- **不分区**：`usage_charges` 等计费表，扣费去重依赖 `request_id` 全局唯一。

MySQL 9.7 对分区没有实质性加强：主键和唯一键须包含分区列、分区表不能有外键，这些限制仍在，本次按此设计。

升级须知（不兼容）：

- 五张表的主键改为 `(id, 分区列)`；`audit_logs.created_at` 改为 `NOT NULL`，NULL 回填为迁移时间。
- 保留期改为「至少保留 N 天」，数据可能多留最多一个分区周期。
- 升级后约 1 小时的第一轮清理，会删除超过 30 天的对话轮次和超过 400 天的审计记录；需要保留更久的部署，先调大配置再升级。
- 生产运行账号没有 DDL 权限，须配置 `Database.MaintenanceDSN`（一般用迁移账号）。
- 迁移原地转换已有数据，整表复制期间不可写；大表须先在恢复副本上计时，并设置 `Database.MigrationTimeout`。
- 没有反向迁移，回退只能恢复升级前的备份。只需升级控制面，Gateway 与 Agent 无需改动。

说明见 [数据库升级与恢复「按时间分区」](deploy/database-recovery.md#按时间分区base-0017--request_logs-0004--usage_records-0002--metrics_history-0002--response_turns-0002)，设计与规格见 `openspec/changes/partition-time-series-tables/`。

已通过的检查：

- 控制面默认测试与 gormstore、logic、retention、partitionmaint 的竞态检查；Gateway `httpapi` 默认测试与竞态检查（新增「用量重试保持原 `created_at`」契约测试）。
- 临时 MySQL 9.7.4 与 PostgreSQL 16 上 gormstore 全部 live 用例，新增分区用例：
  - 升级保留数据与索引、自增计数不回退；
  - PostgreSQL 转换失败整体回滚、MySQL 已转换后显式 resume；
  - 迁移超时拒绝启动并可恢复；
  - 复合主键下用量去重、扣费不重复、对话轮次重试；
  - 多副本维护、兜底分区搬移；
  - 「至少保留」的清理（截止日分区整段保留、迟到行逐行删除）与多副本并发清理；
  - 没有 DDL 权限时退回分批逐行删除、受限运行账号配合维护账号、积压超过上限交给迁移命令、MySQL 会话锁等待恢复；
  - 查询回归与审计跨月分页；
  - schema 检查按表名拒绝。
- 带分区表的原生备份与恢复演练（mysqldump / pg_dump）。
- `EXPLAIN` 核对：租户倒序分页走跨分区索引扫描，无全量排序；单日范围查询只扫一个分区。
- 本机 Docker 上转换 100 万行 `request_logs`：MySQL 22.4 秒、PostgreSQL 16 为 5.6 秒。删除 16 万行，MySQL `DELETE` 1.81 秒，`DROP PARTITION` 0.085 秒。

既有问题（与本次无关，已在未改动的 HEAD 上复现）：

- PostgreSQL 18 上 gormstore live 用例因 `tenant_nodes` 索引检查失败。
- `TestLiveServiceStartupRelaysRecoveredRevocations` 失败。

尚未验收：

- 生产规模数据的转换耗时。
- 真实多副本部署下的维护与清理。
- 长期运行后分区数与清理节奏的观察。

# Gateway Redis 队列与 Agent 整机准入（2026-11-14）

Gateway 的等待改为全集群：

- **工作流与图像生成**：JSON 工作流提交与 ComfyUI 图像生成进入 Redis + asynq 持久队列（`jobqueue/`），提交立即答 202 `queued`，任一副本的 worker 在节点空闲时派发。
  - 投递至少一次，但提交至多一次：意图标记保证结果未知时不重新提交。
  - 任一副本都能查询、取消排队中的 job，也能订阅它的 `queue_changed` 事件。
  - 图像生成在原有的超时预算内同步等待派发。
- **对话**：对话请求的等待改为 Redis 跨副本 FIFO（`waitqueue/`，带头部窗口与停滞逃逸），派发仍在持有连接的副本上。
- **开启条件**：两个队列默认关闭，开启都必须配 `-redis-addr`，否则拒绝启动。进程内排队已删除，`-*-queue-max-waiters` 改为全集群上限。

Agent 新增实时 CPU/内存采集（不引入 gopsutil），经心跳新字段 `Heartbeat.load` 上报，并提供可选的整机准入：

- **资源闸门**：CPU 或内存越过阈值时拒绝新的重任务，带迟滞。
- **本机信号量**：`cli` 与 `general` 两类，满了先有界等待，超时返回可重试背压。
- **Gateway 与 Console**：Gateway 把资源紧张节点在所有策略中排到最后（不跳过）；Console 机群页显示「实时负载」与「资源紧张」。

升级顺序：

- 负载上报与 Agent 准入不限顺序。
- 队列须等所有 Gateway 副本都升级后再开启。
- 新依赖 `github.com/hibiken/asynq` v0.25.1 只用于 Gateway。

说明见 [Gateway STATUS「工作流 Job 的持久队列」](service/aiServeWeaveGateway/STATUS.md#工作流-job-的持久队列asynq2026-11-14)、[对话请求的全集群等待队列](service/aiServeWeaveGateway/STATUS.md#对话请求的全集群等待队列redis2026-11-14) 与 [Agent README「整机准入与负载上报」](service/aiServeWeaveAgent/README.md#整机准入与负载上报)。

已通过的检查：

- Gateway、Agent、common 与控制面 fleet 的默认测试及竞态检查。
- 新增测试：
  - 队列状态机：意图未确认不重提、持有后交还、期限超时、提交途中取消；
  - 两副本共享内存队列的 HTTP 路径：跨副本派发与查询、取消、队列满 429、Redis 不可用时退回同步、租户隔离、排队事件流、图像排队超时；
  - 对话等待：头部窗口、停滞逃逸、租约过期、Redis 故障；
  - Agent：准入迟滞与信号量、派发分类与豁免、心跳负载、配置往返；
  - Gateway：压力排序。
- 在专用 Redis 7 容器上通过 Lua 脚本与 asynq 端到端投递的 live 用例（`AISW_REDIS_ADDR=… go test -p 1 ./service/aiServeWeaveGateway/jobqueue/... ./service/aiServeWeaveGateway/waitqueue/... ./service/aiServeWeaveGateway/ratelimit/...`；`-p 1` 是因为 ratelimit 的 live 用例会 FLUSHDB）。
- Agent 交叉编译：windows/linux/darwin，amd64/arm64。
- 实际采样：Linux arm64 容器在 256 MiB cgroup 限额下读到 256 MiB 总量，macOS 本机读到估算 CPU。
- Console：378 项单元测试、lint（仅 1 个既有警告）、TS7 类型检查与生产构建。

复审修复（2026-11-14，三路独立代码审查之后）：

- **工作流提交不再重复**：
  - 工作流提交只在确定后端没收到时重试：新增跨隧道哨兵 `ErrNotDelivered`，同步派发也适用；
  - 提交途中的取消不再丢失；
  - 写入意图对同一次投递幂等；
  - 最终写入失败会交还任务；
  - 等待超过时限的孤儿记录在读取时以 `queue_timeout` 收尾；
  - 入队不受客户端断开影响；
  - Redis 读不到时答 503 而不是 404；
  - 图像等待能容忍短暂读失败，放弃时撤回排队任务；
  - 排队事件流加保活；
  - worker 持有时长缩短到 10 秒、默认并发 8，关停等待（35 秒）大于单次提交上限（30 秒）。
- **对话等待**：
  - 按模型分通道，修复跨模型的队头阻塞；
  - 获得资格的票在派发期间被领走，繁忙时按原序号放回；
  - 每次 Redis 调用有短超时，共享客户端改为 1 秒超时、重试 1 次；
  - 新增 `-chat-queue-name`，等待数与轮询间隔设了上下限。
- **Agent**：
  - 迟滞退出线始终可达，并有配置校验；
  - 指标持续缺失时 fail-open；
  - 请求期限到期不再被当作可重试背压；
  - gauge 写入不再乱序；
  - 采样失败后不再上报陈旧数据；
  - cgroup 内存按 MemAvailable 口径计算，并支持嵌套 cgroup；
  - 压力原因变化也会补发心跳。
- **Gateway 节点视图**：
  - 越界时间戳视为未上报，修复一个异常 Agent 能让机群视图变成空 200 的问题；
  - 节点断线时压力指标复位，重连时清除旧负载；
  - 非 least_load 策略的压力排序改为无分配。

调度热路径基准与改动前一致（`PickCandidates` 7 allocs/op）。

尚未验收：

- 多副本真实部署与真实 ComfyUI 下的排队和崩溃恢复。
- 真实负载下的阈值校准。
- Windows 实机采样。
- 孤儿运行（节点已接单、绑定未写入时 worker 崩溃）的对账。

# 商户订单删除（2026-11-13）

商户计费页「订单」新增删除与确认框，owner/admin 可删除任意状态的本商户订单；member 只读。删除持久化为 `checkout_orders.tenant_deleted_at`，仅商户列表隐藏，运维历史、付款恢复、去重、发货、退款与库存占用继续使用原订单。新接口为 `DELETE /admin/v1/billing/orders/:id`，成功 `204`，跨商户、未知与已删除统一 `404`，审计动作 `billing.order.delete`。Console 白名单、八语言提示、失败保留确认框与末页回退已接入。迁移为 PostgreSQL/MySQL `billing/0010_order_deletion.sql`，`sql/init.sql` 已同步；控制面与 Console 须一起更新。行为与升级说明见[商户订单删除](docs/billing-orders.md)。

验证：控制面默认测试及 logic/handler/store 竞态检查、Console 375 项单元测试、lint（仅既有 alerts 页警告）、类型检查与生产构建、Chrome 计费回归 29 项通过。新增共享 store 场景覆盖所有订单状态、租户隔离、分页前过滤、删除后的迟到付款与重放去重；新增 HTTP/logic 用例覆盖授权与审计。真实 PostgreSQL/MySQL 迁移、真实付款与生产部署尚未验收。

# 易支付（EZFP）人民币收款（2026-11-12）

新增易支付（彩虹易支付 V2）作为 Dodo 之外的第二个法币收款通道，租户可选支付宝或微信支付，用于充值包、账号包月与账号预售三条结账流程。只有币种为 CNY 的商品可以走易支付，金额就是现有标价，不另设价格；CNY 商品可以不填 Dodo 产品 ID，此时只能经易支付购买。易支付可与 Dodo 并存（结账时由租户选择），也可单独开启计费。到账只认经平台公钥验签的异步通知或控制面主动查询，两者都走 Dodo 已有的入账事务与去重；Console 回跳只触发一次查询，订单列表另有「再次确认」，后台每分钟补查 2 小时内的待付款订单。退款以退款申请 id 为 `out_refund_no` 同步结算；答复丢失时以退款查询恢复，不自动重发。配置为控制面 `Billing.Ezfp`，密钥经 `AISW_EZFP_PRIVATE_KEY` / `AISW_EZFP_PUBLIC_KEY` 注入，反向代理须暴露 `/billing/v1/ezfp/notify`（已更新 `deploy/` 下的配置示例、`.env.example`、Compose 与 nginx 示例）。迁移为 `billing/0009_ezfp.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。规则、开通步骤与验证边界见[易支付说明](docs/billing-ezfp.md)。

已通过的检查：控制面默认测试，billing、logic、handler、store、config、e2e 的竞态检查；签名内容与算法与 OpenSSL 生成的固定向量一致；e2e 以真实客户端经 HTTPS 连模拟易支付，跑完「只配易支付 → 建 CNY 充值包 → 结账 → 签名 GET 通知入账 → 重放不重复 → 运维退款」；临时 PostgreSQL 16 与 MySQL 8.0 上的 gormstore live 用例（含 0009 迁移与 `storetest.EzfpOrders`）。Console 375 项单元测试、lint（仅 1 个既有警告）、TS7 类型检查与生产构建通过，本机 Chrome 计费专项 26 项通过；全套浏览器用例中 `console-header.spec.ts` 两项、`console.spec.ts` 三项（320px 的 `/operator/routes`、`/operator/workflow-templates` 与导航 `aria-current`）失败，已在未含本次改动的 HEAD 上复现同样 5 项，与本次无关。未用真实商户号联调（手机微信 jump、`out_refund_no` 防重复、通知重投是否重新签名均未实测），`clientip` 是 Console 的地址而不是付款人真实 IP，尚未部署。

# 公开首页与法律条款（2026-11-12）

为配合 Antom MOR 审核（要求网站公开展示产品、价格、T&C、隐私条款与退款条款），Console 新增公开页面：`/` 未登录时改为产品介绍首页（已登录照旧进入概览），新增 `/terms`、`/privacy`、`/refund`（英文、简中、繁中，以英文为准，其余语言显示英文），首页、价格、登录、注册页加统一页脚，注册页加同意声明，价格页加分区说明，计费页加条款链接。退款条款按现有规则撰写（点数在 `RefundWindowDays` 内退未用部分，经邮件申请；已开通包月不退；预售未交付全额退；支付宝额度不可撤销）。营运方名称、地址、邮箱、准据法与退款天数来自 Console 环境变量 `AISW_CONSOLE_LEGAL_*`（已接入 Compose 与 `.env.example`），未设置时显示方括号占位符。只需更新 Console，控制面、Gateway 与 Agent 无需升级。说明见 [Console README](service/aiServeWeaveConsole/README.md#公开首页与法律条款)。

同时修复 `app/globals.css` 中 `--font-sans` 引用自身的问题（原本整个 Console 实际显示为浏览器默认的 Times 字体，现恢复为已载入的 Geist），这会影响所有页面的西文字形。

已通过：Console 362 项单元测试（含新增 8 项法律页测试与八语言词典检查）、lint、TS7 类型检查与生产构建；本机生产模式下以模拟目录检查 7 个公开页 × 4 种宽度（1440/768/390/320）× 4 种语言（繁中、英、日、德），均无页面横向卷动或元素超出视窗，页首、正文、页脚左右对齐，页脚链接点击高度不小于 24px。条款正文未经法律审阅，营运方资讯仍为占位符，尚未部署，也未提交 Antom 审核。

# 支付宝（一人付）点数兑换（2026-11-11）

新增支付宝付款方式：商户在支付宝购买次数或积分套餐，额度留在支付宝，在 Console 计费页「支付宝」分区兑换为点数。控制面先保存兑换（编号即一人付幂等键），只有一人付确认核销成功且编号、数量、单位一致时，才在同一事务里记入 `alipay_topup` 点数；结果未知时以原编号查询或原参数重发，后台每分钟恢复，绝不换号再扣。购买入口、回跳和扫码都不入账。Dodo 改为可选，只配支付宝也能开启点数计费（充值包、结账、门户与退款随 Dodo 关闭）。API Key 只从控制面环境变量 `opcpay_api_key` 读取。迁移为 `billing/0007_alipay.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。规则、开通步骤与验证边界见[支付宝说明](docs/billing-alipay.md)。

已通过的检查：控制面默认测试与 opcpay 客户端、logic、handler、store 的竞态检查；临时 PostgreSQL 16 与 MySQL 8.0 上的 gormstore live 用例（迁移、schema 检查、并发结算只入账一次、并发创建只留一笔待确认）。Console 347 项单元测试、lint、TS7 类型检查与生产构建通过，本机 Chrome 计费专项 17 项通过。未配置真实 `opcpay_api_key`，没有只读查询真实能力与套餐，也没有真实支付宝购买、核销和部署验收；只支持次数/积分套餐，一人付通知与退款未接入。

# 账号预售（2026-11-11）

新增账号预售：运维只定义类型（Claude Code / Codex）、法币月价、Dodo 产品 ID 和名额上限，不绑定具体账号。商户经 Dodo 付款后，订单进入「已付款，待发货」，此时不开通账号，也不切换计费模式。运维在 `/operator/billing` 的「账号预售 → 待发货订单」为每笔订单分配一个同类型的空闲库存账号，分配时开通一个自然月并切换为账号包月；无法交付时可以在同一列表全额退款。账号商品新增 `kind`（创建时必填），下架商品可以不标价，仅供预售分配。迁移为 `billing/0006_account_presale.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。规则与契约见[账号预售](docs/billing-accounts.md#账号预售)。

**支付宝额度支付（2026-11-11 新增）**：运维可在预售上设置「支付宝额度数量」（0 或留空表示不走支付宝），商户在 Console「账号购买」用支付宝额度支付一个月。控制面先保存待支付订单与待确认核销（`action_id` 幂等），只有一人付确认核销成功才把订单标为已付款并进入「待发货」；确定失败则订单失败并释放名额；结果未知保持待确认并由原恢复流程续办。支付宝订单**系统内不可退款**（一人付没有核销撤销），由运维线下处理。次数包每次核销只能扣 1，数量大于 1 的预售需要积分包。迁移 `billing/0008_alipay_presale.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。未用真实 `opcpay_api_key` 验收，gormstore 的预售支付宝路径只由共享规则与默认测试覆盖，未在临时 PostgreSQL/MySQL 上跑 live 用例。

已通过的检查：控制面默认测试，store、logic、handler 与 e2e 的竞态检查；在临时 PostgreSQL 16 与 MySQL 8.0 上运行的 gormstore live 用例（迁移、升级保留数据、账号结账、预售）。Console 侧通过 338 项单元测试、lint、TS7 类型检查与生产构建，本机 Chrome 计费专项 15 项全部通过。`console.spec.ts` 中有 1 项与本次改动无关的导航用例失败。真实 Dodo 付款、本地与生产部署、真实 CLI 账号的发货联调尚未验收。

# 账号包月改为法币标价（2026-11-11）

账号包月商品改为以法币标价（币种 + 月价 + Dodo 产品 ID），商户经 Dodo 在线结账购买或续费一个月，不再使用点数余额；点数购买路由已移除。付款 webhook 验签后才开通期限，待付款结账为本商户保留账号 30 分钟，超时后被他人买走的付款记为「未开通，待退款」。迁移 `billing/0005_account_fiat.sql` 会把现有商品下架并清零价格（已售期限保留），运维须重新定价上架。购买需要 `Billing.Enabled` 并配置 Dodo，见[账号包月说明](docs/billing-accounts.md)。

控制面默认测试与竞态检查、Console 328 项单元测试、lint、TS7 类型检查、生产构建和 Chrome 计费专项 12 项通过；临时 MySQL 8.0 实测迁移（含旧数据升级）与结账/付款事务通过。PostgreSQL live、真实 Dodo 付款、本地与生产部署尚未验收。

# Agent 默认不共享与运维控制（2026-11-09）

机群页现可直接对每个平台注册节点确认开启或停止共享（2026-11-10）。新增 Agent 接入/审批共享选项与共享设置，默认不共享；未配置的 Agent 也不进入普通租户的模型目录和调度池。有效的独享账号购买权益与专属租户绑定保留。基础迁移 `0015_node_sharing.sql`、授权载荷和 Gateway 检查已实现，使用及升级顺序见[Agent 共享设置](docs/agent-sharing.md)。

控制面与 Gateway 相关默认/竞态测试、326 项 Console 单元测试、共享操作与运维宽窄屏 Chrome 检查通过；隔离 MySQL 的迁移、模式持久化与账号购买回归通过。本地 Gateway、控制面和 Console 已备份后重建，真实运维/租户登录验证：1 台 Agent 已接入、无显式共享，租户模型目录不包含其服务；共享设置 API 返回 200，租户写入被拒绝。PostgreSQL live、生产升级和真实 CLI 账号联调尚未验收。


## Agent 账号独享包月（2026-11-09）

已交付 Claude Code 与 Codex 账号商品、点数余额购买/续费、独享授权与 Console 入口。同一个 Agent 中的两种账号可分别售给不同商户。账号以 Agent ID + 运行时 ID 定位，按 UTC 自然月延长期限；购买后切换为账号包月模式，仅可调用自己已购且未到期的账号。接口、迁移和升级顺序见 [账号包月说明](docs/billing-accounts.md)。

账号包月现支持独立开关 `AccountSales.Enabled`，不要求配置 Dodo。新增表单改为选择 Agent 与实际上报的 Claude Code / Codex 账号，排除重复商品，并支持刷新清单与失败重试。本地 Docker 的控制面和 Console 已重建、启用独立开关，真实运维登录后账号 API 返回 200；Agent 接入后的默认共享准入见上节，真实 CLI 账号售卖联调尚未验收。

两种账号同 Agent 分别售卖的 logic 测试和真实隧道加模拟后端的 scheduler 测试（Claude 原生 Messages / Codex Chat）已通过竞态检查；两种真实上游登录的售卖联调仍待验收。

默认测试、相关竞态检查、Console 类型检查与生产构建通过；Chrome 计费专项 11 项和隔离 MySQL 9.7 账号事务实测通过。PostgreSQL live 测试未运行，生产升级、真实付款及实际 CLI 账号联调尚未验收。

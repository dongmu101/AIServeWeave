## Purpose

控制面里随请求量或时间持续增长的表，在 MySQL 与 PostgreSQL 上统一按时间范围分区存储，使过期数据能以与行数无关的成本整段移除，时间范围查询只触达相关分区，同时保持既有的写入幂等与查询结果不变。

## ADDED Requirements

### Requirement: 增长表按时间范围分区
控制面 SHALL 在 MySQL 与 PostgreSQL 上把下列表存为按时间 RANGE 分区的表，分区列与粒度如下，两种数据库一致：

| 表 | 分区列 | 粒度 |
|---|---|---|
| `request_logs` | `created_at` | 天 |
| `usage_records` | `created_at` | 月 |
| `metrics_history_points` | `bucket_at` | 天 |
| `response_turns` | `created_at` | 天 |
| `audit_logs` | `created_at` | 月 |

分区边界 MUST 以 UTC 计算。每张表 MUST 有一个兜底分区（MySQL 为 `MAXVALUE` 分区，PostgreSQL 为 `DEFAULT` 分区），保证任何时间值的写入都不会因找不到分区而失败。

`usage_charges` 及其它计费表 MUST NOT 分区。

#### Scenario: 新安装得到分区表
- **WHEN** 在空库上执行迁移
- **THEN** 上表五张表都以对应分区列和粒度分区，且各有一个兜底分区

#### Scenario: 时间超出已建分区时写入仍然成功
- **WHEN** 写入一行，其分区列的值晚于最后一个已建的常规分区
- **THEN** 写入成功，该行落入兜底分区

#### Scenario: 计费表保持不分区
- **WHEN** 迁移完成后检查 `usage_charges`
- **THEN** 该表未分区，`request_id` 仍受全局唯一约束

### Requirement: 主键包含分区列且保持写入幂等
上述五张表的主键 SHALL 为 `(id, <分区列>)`。数据库层面以 `(id, <分区列>)` 判定重复。

`request_logs` 与 `usage_records` 的批量写入 MUST 保持幂等：同一 `id`、同一 `created_at` 的重复推送不得插入第二行，也不得报错。Gateway 重发 outbox 中的记录时 MUST 沿用该记录首次生成时的 `created_at`（按数据库精度截断后相同）。

`metrics_history_points` 的 upsert 仍以 `(metric, labels, bucket_at)` 判定冲突并更新 `value`。

`response_turns` 的 `created_at` 由控制面在收到请求时赋值，重试时会变，因此写入前 MUST 先在最近 24 小时内按 `id` 查找：已存在且租户相同时返回已有的行，租户不同时返回冲突。两次写入并发到达时，允许留下 `id` 和内容都相同的两行，此时按 id 读取返回其中任意一行。

#### Scenario: 顺序重试对话轮次返回已有行
- **WHEN** Gateway 记录一轮后，以相同的 ResponseID 与租户再次记录
- **THEN** 第二次返回第一次写入的行（`created_at` 不变），表中该 id 只有一行

#### Scenario: 其它租户占用的 ResponseID 返回冲突
- **WHEN** 以已被另一租户使用的 ResponseID 记录一轮
- **THEN** 返回冲突，原有的行不变

#### Scenario: 重复推送请求日志不产生重复行
- **WHEN** Gateway 两次推送同一批请求日志，`id` 与 `created_at` 均相同
- **THEN** 表中每个 `id` 只有一行，两次推送都返回成功

#### Scenario: 重复推送用量记录不产生重复行
- **WHEN** Gateway 重发一批已入库的用量记录，`id` 与 `created_at` 均相同
- **THEN** 表中每个 `id` 只有一行，结算汇总不重复计数，扣费不重复

#### Scenario: Gateway 重发保持原时间戳
- **WHEN** Gateway 向控制面推送某批记录失败后重试
- **THEN** 重试请求里每条记录的 `created_at` 与首次推送时相同

#### Scenario: 指标汇总 upsert 仍按序列与时间桶合并
- **WHEN** 对同一 `(metric, labels, bucket_at)` 写入两次不同的 `value`
- **THEN** 表中只有一行，`value` 为后一次的值

### Requirement: 按 id 读取的结果不变
仅凭 `id`（以及租户）读取的操作 SHALL 返回与分区前相同的结果，包括按 `previous_response_id` 读取对话轮次、按请求 id 过滤请求日志、按租户列出审计记录。

#### Scenario: 按 id 读取对话轮次
- **WHEN** 以租户和响应 id 读取一条仍在保留期内的对话轮次
- **THEN** 返回该行；租户不匹配时返回未找到

#### Scenario: 按请求 id 过滤请求日志
- **WHEN** 以请求 id 过滤请求日志，且不给时间范围
- **THEN** 返回该 id 对应的行

### Requirement: 审计记录时间不可为空
`audit_logs.created_at` SHALL 为 `NOT NULL`。升级时已有的 NULL 值 MUST 回填为迁移执行时的 UTC 时间，这些行不得因升级被立即清理。

#### Scenario: 历史 NULL 时间被回填
- **WHEN** 升级前 `audit_logs` 中有 `created_at` 为 NULL 的行，执行升级迁移
- **THEN** 这些行的 `created_at` 等于迁移执行时间，行数不变

### Requirement: 已有数据在升级迁移中原地转换
对已存在且未分区的五张表，升级迁移 SHALL 原地转换为分区表，保留全部行与全部列值，并重建原有二级索引。转换期间表不可写。转换中断后 MUST 能通过既有的显式 resume 流程重新执行，且重复执行不会损坏已转换的表。

迁移超时 SHALL 可配置；未配置时沿用 5 分钟。超时或失败时迁移 MUST 标记为 dirty 并停止启动，不得在半转换状态下对外服务。

#### Scenario: 升级保留全部数据
- **WHEN** 对含历史数据的未分区表执行升级迁移
- **THEN** 迁移后行数与每行列值和升级前一致，原有二级索引都存在

#### Scenario: 转换后 resume 是安全的
- **WHEN** 转换已经完成，但账本仍为 dirty，此时以显式 resume 重新执行该迁移
- **THEN** 迁移识别出表已分区，跳过转换并把账本标记为完成

#### Scenario: 迁移超时则拒绝启动
- **WHEN** 大表转换超过配置的迁移超时
- **THEN** 迁移失败并保持 dirty，控制面启动失败并报告超时

### Requirement: 自动维护未来分区
控制面 SHALL 在启动后及之后每小时维护分区：按天分区的表始终至少预建到未来 7 天，按月分区的表至少预建到未来 2 个月。维护 MUST 在多副本之间串行：同一时刻最多一个副本对同一数据库执行分区 DDL，拿不到锁的副本跳过本轮，不报错。维护 MUST 与迁移互斥。

维护失败时 MUST 记录错误日志并在下一轮重试，不得影响写入与查询。在兜底分区已有数据的极端情况下（例如维护长期停止），维护 MUST 把这些行正确地移入新建分区，且不丢行。搬移期间该表不能写入，因此定时维护要搬移的行超过 20 万时 MUST 拒绝执行，每轮报错并提示执行迁移命令；迁移命令（`-migrate up`/`resume` 或 `AutoMigrate`）在迁移完成后 MUST 执行一次不限搬移量的维护。分区 DDL 等待元数据锁的时间 MUST 不超过 2 秒。

#### Scenario: 启动后预建未来分区
- **WHEN** 控制面启动并完成迁移
- **THEN** `request_logs` 已有覆盖今天及之后 7 天的按天分区，`usage_records` 已有覆盖本月及之后 2 个月的按月分区

#### Scenario: 多副本同时维护
- **WHEN** 两个控制面副本同时触发分区维护
- **THEN** 只有一个副本执行 DDL，另一个跳过本轮，最终分区集合不重复也不缺失

#### Scenario: 兜底分区已有数据时补建分区
- **WHEN** 兜底分区里已有某天的少量行，维护为这一天建立常规分区
- **THEN** 这些行都进入新分区，总行数不变

#### Scenario: 积压过多时交给迁移命令
- **WHEN** 兜底分区里待搬移的行超过定时维护的上限
- **THEN** 定时维护不搬移并报错，错误信息提示执行 `-migrate up`；随后执行迁移命令，这些行都进入新分区

### Requirement: 分区 DDL 可以使用单独的维护账号
运行账号没有 DDL 权限的部署 SHALL 能通过可选配置 `Database.MaintenanceDSN` 指定执行分区 DDL 的账号（预建分区、删除过期分区）；读写、行删除与其它查询仍使用 `Database.DSN`。不配置时分区 DDL 使用 `Database.DSN`。维护账号与运行账号 MUST 共用同一把迁移锁。

#### Scenario: 受限运行账号配合维护账号
- **WHEN** `Database.DSN` 的账号只能读写，`Database.MaintenanceDSN` 的账号是表的所有者
- **THEN** 未来分区照常预建，过期分区照常整段删除，运行账号能读写新建分区里的行

### Requirement: 启动时校验分区结构
控制面的 schema 检查 SHALL 校验五张表已按规定的分区列分区，且有兜底分区；不满足时 MUST 报告具体的表名并拒绝启动。

#### Scenario: 表未分区时启动失败
- **WHEN** 迁移账本显示已完成，但 `request_logs` 实际未分区
- **THEN** schema 检查失败，错误信息指出 `request_logs`

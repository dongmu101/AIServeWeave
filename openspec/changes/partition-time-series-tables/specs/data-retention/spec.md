## Purpose

规定控制面五张增长表各自保留多久、如何配置，以及过期数据如何清理，让存储占用有上限，同时保证保留期内的数据一行不少、过期数据最终一行不留。

## ADDED Requirements

### Requirement: 每张增长表都有保留期
控制面 SHALL 为下列表按配置的保留期清理过期行；保留期配置为零时采用默认值，为负时拒绝启动：

| 表 | 配置项 | 默认值 | 判定列 |
|---|---|---|---|
| `request_logs` | `RequestLogRetention` | 30 天 | `created_at` |
| `usage_records` | `UsageRecordRetention` | 400 天 | `created_at` |
| `metrics_history_points` | `MetricsHistory.Retention` | 90 天 | `bucket_at` |
| `response_turns` | `ResponseTurnRetention` | 30 天 | `created_at` |
| `audit_logs` | `AuditLogRetention` | 400 天 | `created_at` |

`request_logs`、`usage_records`、`response_turns`、`audit_logs` 的清理 MUST 无条件运行；`metrics_history_points` 的清理与现在一样，只在指标历史功能启用时运行。

为避免超过 MySQL 每表 8192 个分区的上限，按天分区的表（`request_logs`、`metrics_history_points`、`response_turns`）的保留期 MUST NOT 超过 3650 天，超过时拒绝启动。

#### Scenario: 新增保留期取默认值
- **WHEN** 配置中未设置 `ResponseTurnRetention` 与 `AuditLogRetention`
- **THEN** 对话轮次保留 30 天，审计记录保留 400 天

#### Scenario: 负的保留期被拒绝
- **WHEN** 配置 `AuditLogRetention` 为负值
- **THEN** 配置校验失败，控制面拒绝启动，错误信息指出该配置项

#### Scenario: 按天分区表的保留期过长被拒绝
- **WHEN** 配置 `RequestLogRetention` 为 3651 天
- **THEN** 配置校验失败，控制面拒绝启动，错误信息指出该配置项与上限

### Requirement: 清理精确到行
一次清理完成后，表中 SHALL 不存在判定列早于「当前时间 − 保留期」的行，且 MUST NOT 删除任何判定列不早于该截止时间的行。截止时间落在某个分区内部时，该分区内未过期的行 MUST 保留。

#### Scenario: 截止时间落在分区中间
- **WHEN** 按天分区的 `request_logs` 截止时间为某天 12:00，该天 11:00 与 13:00 各有一行
- **THEN** 清理后 11:00 的行被删除，13:00 的行保留

#### Scenario: 整段过期的分区被移除
- **WHEN** 某个分区覆盖的整个时间段都早于截止时间
- **THEN** 清理后该分区及其全部行不复存在，且清理耗时与该分区行数无关

#### Scenario: 升级时转换进来的历史数据按期清理
- **WHEN** 升级时转换进来的历史行逐步超过保留期
- **THEN** 它们与新写入的行一样在过期后被清理，历史数据全部过期后，存放历史数据的分区被移除

### Requirement: 过期的对话轮次不可再引用
超过 `ResponseTurnRetention` 被清理的对话轮次 SHALL 与从未存在一样处理：用它的 id 作为 `previous_response_id` 时，返回与「未找到」相同的错误。

#### Scenario: 引用已清理的上一轮
- **WHEN** 客户端以一条已被清理的响应 id 作为 `previous_response_id` 发起请求
- **THEN** 请求返回与未知 id 相同的未找到错误

### Requirement: 清理在多副本下安全
多个控制面副本同时清理同一张表时，SHALL 不报错、不重复计数、不误删未过期的行。分区移除 MUST 与分区维护、迁移互斥；拿不到锁的副本本轮只执行行级删除或直接跳过，结果仍满足「清理精确到行」。

#### Scenario: 两个副本同时清理
- **WHEN** 两个副本在同一时刻对 `request_logs` 执行清理
- **THEN** 两者都成功返回，清理后的表满足「清理精确到行」

### Requirement: 清理结果可观测
每次清理移除了分区或删除了行时，控制面 SHALL 记录一条日志，包含表名、截止时间、移除的分区数与删除的行数；清理失败时 MUST 记录错误日志并在下个周期重试。

#### Scenario: 移除分区时记录日志
- **WHEN** 一次清理移除了 2 个分区并删除了 10 行边界数据
- **THEN** 日志中有一条记录，包含表名、截止时间、`partitions_dropped=2` 与 `rows_deleted=10`

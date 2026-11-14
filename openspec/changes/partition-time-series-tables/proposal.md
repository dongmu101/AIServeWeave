## Why

控制面有五张表随请求量或时间持续增长：`request_logs`、`usage_records`、`metrics_history_points`、`response_turns`、`audit_logs`。前三张现在靠定时 `DELETE … WHERE created_at < ?` 清理。行数一多，大批量删除会产生大量 undo/redo（MySQL）或死元组（PostgreSQL），还会造成主从延迟和锁等待；后两张表根本没有清理，会无限增长。按时间分区后，过期数据整段 `DROP` 掉，成本与行数无关，范围查询也能只扫描相关分区。

要先澄清一点：MySQL 9.7 对分区**没有实质性加强**（9.7.0–9.7.4 只修了 EXCHANGE/TRUNCATE PARTITION 等几个 bug）。主键和唯一键必须包含分区列、分区表不能有外键、每表最多 8192 个分区，这些限制都还在。本变更按这些限制设计。

## What Changes

- 五张表改为按时间 RANGE 分区，MySQL 9.7 与 PostgreSQL 都做，两边表结构一致：
  - `request_logs`：按天，分区列 `created_at`
  - `usage_records`：按月，分区列 `created_at`
  - `metrics_history_points`：按天，分区列 `bucket_at`
  - `response_turns`：按天，分区列 `created_at`
  - `audit_logs`：按月，分区列 `created_at`
- **BREAKING（表结构）**：这五张表的主键由 `(id)` 改为 `(id, <分区列>)`。数据库不再保证 `id` 单独全局唯一，而是由写入方保证：`request_logs`/`usage_records` 的幂等依赖 Gateway outbox 重发时保持原 `created_at`，这一点会在契约与测试中固化。`audit_logs` 的 id 由控制面随机生成（128 位）。`response_turns` 的 id 是 Gateway 生成的随机 ResponseID，`created_at` 则是控制面收到请求时的时间，重试时会变，所以写入前先按 id 查一次：顺序重试照旧返回已有行，只有并发重试可能留下内容相同的重复行（读取结果不变）。
- **BREAKING（表结构）**：`audit_logs.created_at` 改为 `NOT NULL`。历史 NULL 行回填为迁移执行时间，因此不会在升级时立即被清理掉。
- 已有数据在升级迁移中原地转换成分区表。迁移要整表复制，耗时与行数成正比；新增可配置的迁移超时，取代写死的 5 分钟。
- 新增分区维护任务：预先建好未来的分区（兜底分区保证维护落后时写入也不会失败），多副本之间用锁串行化。
- 保留期清理改为：整段过期的分区直接 `DROP`，边界分区里剩下的过期行仍用 `DELETE`。保留期语义不变，精度仍然到行。
- 新增保留期配置：`ResponseTurnRetention`（默认 30 天）与 `AuditLogRetention`（默认 400 天），零值采用默认值，与 `RequestLogRetention` 风格一致。**BREAKING（行为）**：升级后超过 30 天的对话轮次与超过 400 天的审计记录会被清理，超过 30 天的 `previous_response_id` 将返回未找到。
- 不分区：`usage_charges` 等计费表。扣费去重依赖 `request_id` 全局唯一，分区后无法保证。

## Capabilities

### New Capabilities
- `time-partitioned-storage`：五张增长表的分区布局、主键约束、升级转换、分区维护与多副本协调。
- `data-retention`：五张表的保留期配置、默认值，以及「整段删分区 + 边界行删除」的清理语义。

### Modified Capabilities
（无。`openspec/specs/` 目前没有既有规格。）

## Impact

- **代码**：`service/aiServeWeaveControlPlane/internal/store/gormstore`（五个命名空间的 MySQL/PG 新迁移、迁移执行器的动态分区展开与可配置超时、`DeleteXBefore` 系列改为先删分区、schemacheck 增加分区校验），`internal/model`（主键 tag），`internal/config`（新增两项保留期和迁移超时），`internal/svc/servicecontext.go`（接入维护任务与两个新清理协程），新增 `internal/partitionmaint`、`internal/responseturnretention`、`internal/auditretention`，`internal/store`（接口与 memstore 新增删除方法）。
- **Gateway**：不改代码，只补测试，固化「outbox 重发保持原 `created_at`」的契约。
- **数据库与运维**：升级须停掉全部控制面副本（现行流程已经要求），大表转换期间不可写；`deploy/database-recovery.md` 补充分区表的备份恢复与转换耗时说明；`sql/init.sql` 同步；`deploy/controlplane.example.yaml` 增加新配置项。
- **文档**：根目录 `STATUS.md`、控制面 `STATUS.md`/`README.md`、`docs/architecture.md` 的存储章节。
- **依赖**：无新增。

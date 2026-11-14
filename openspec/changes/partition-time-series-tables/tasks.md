## 1. 前置核实

- [x] 1.1 在 MySQL 9.7 容器上验证：同一条 `ALTER TABLE` 能否同时完成 `DROP/ADD PRIMARY KEY`、`MODIFY … NOT NULL` 和 `PARTITION BY RANGE COLUMNS`；`REORGANIZE PARTITION p_max` 在 `p_max` 为空和有数据时的耗时与锁行为。结论记入 design.md D2/D5。
- [x] 1.2 在 PostgreSQL 18（以及测试矩阵里的 16）上验证：分区父表的 `ON CONFLICT DO NOTHING`（不带目标）、`ON CONFLICT (metric, labels, bucket_at)`、`DETACH`/`ATTACH` 默认分区，以及 GORM `ColumnTypes`/`HasIndex` 在分区父表上的结果。
- [x] 1.3 在 gormstore、logic、handler 中 grep 五个模型（`RequestLog`、`UsageRecord`、`MetricsHistoryPoint`、`ResponseTurn`、`AuditLog`）按主键读写的用法（`First(&x, id)`、`Save`、`Delete(&x)`），列出改成复合主键后受影响的位置。

## 2. 分区清单与迁移执行器

- [x] 2.1 在 gormstore 新增分区表清单（表名、分区列、粒度、预建数、常规分区上限 1000），作为迁移展开、维护、清理和 schemacheck 的唯一来源。
- [x] 2.2 实现两种数据库的分区子句生成：从 `MIN(分区列)` 所在周期建到「当前周期 + 预建数」，超过上限的部分并入 `p_hist`，最后加兜底分区；边界一律按 UTC。为空表、少量数据、超过上限的历史数据、跨月或跨年边界写单元测试。
- [x] 2.3 在 `executeMigration` 中识别 `/*aisw:partitions …*/` 占位并展开，校验和仍对模板计算；MySQL 下表已分区时跳过 ALTER，保证 resume 可重复执行。补测试：校验和稳定、resume 跳过。
- [x] 2.4 新增 `Database.MigrationTimeout` 配置（默认 5 分钟）并接入 `withMigrationLock`；转换前记录各表的估算行数。补配置校验测试。

## 3. 迁移文件

- [x] 3.1 `base/{mysql,postgres}/0017_partition_audit_logs.sql`：回填 NULL `created_at`，改为 `NOT NULL`，主键改为 `(id, created_at)`，按月分区，重建 `0008`/`0016` 的审计索引。
- [x] 3.2 `request_logs/{mysql,postgres}/0004_partition_request_logs.sql`：主键 `(id, created_at)`，按天分区，重建三个二级索引；PG 的列定义包含 `0003` 新增的四列。
- [x] 3.3 `usage_records/{mysql,postgres}/0002_partition_usage_records.sql`：主键 `(id, created_at)`，按月分区，重建两个二级索引。
- [x] 3.4 `metrics_history/{mysql,postgres}/0002_partition_metrics_history_points.sql`：主键 `(id, bucket_at)`，按天分区，保留唯一索引 `(metric, labels, bucket_at)`；PG 按 design D2 迁移 `BIGSERIAL` 序列的归属，计数不回退。
- [x] 3.5 `response_turns/{mysql,postgres}/0002_partition_response_turns.sql`：主键 `(id, created_at)`，按天分区，重建租户索引。
- [x] 3.5a gormstore `CreateResponseTurn` 改为先按 id 查找再插入（design D4），live 测试覆盖顺序重试与跨租户冲突。
- [x] 3.6 更新五个模型的 GORM tag（分区列加 `primaryKey`，`MetricsHistoryPoint.ID` 加 `autoIncrement`），修正 1.3 列出的受影响调用。
- [x] 3.7 更新 `migrationfiles_test.go` 等迁移文件测试；用 `UPDATE_INIT_SQL=1` 重新生成 `sql/init.sql`，让生成器把占位展开为只有兜底分区（design D10）。

## 4. schema 检查

- [x] 4.1 在 `checkSchemaObjects` 中按清单校验五张表的分区方式、分区列和兜底分区，失败时报告表名。
- [x] 4.2 补测试：未分区、分区列不对、缺兜底分区时，schema 检查报告对应的表名。

## 5. 分区维护

- [x] 5.1 在 store 接口新增 `MaintainPartitions(ctx, now)`；gormstore 按 design D5 实现两种数据库的预建与「兜底分区已有数据」时的搬移，并设置会话级短锁等待；memstore 实现为空操作。
- [x] 5.2 复用迁移锁的非阻塞获取实现互斥，拿不到锁时返回「已跳过」。
- [x] 5.3 新增 `internal/partitionmaint`（每小时运行，可注入时钟），在 `servicecontext.go` 中接入：迁移后同步执行一次（限时 30 秒，失败只记日志），之后由定时器接手，退出时随其它协程一起关闭。
- [x] 5.4 单元测试：定时循环、跳过、失败重试与日志。

## 6. 保留期清理

- [x] 6.1 新增 `store.RetentionResult{PartitionsDropped, RowsDeleted}`，把 `DeleteRequestLogsBefore`、`DeleteUsageRecordsBefore`、`DeleteRollupBefore` 改为先删整段过期分区（含 `p_hist`，永不删兜底分区）再 `DELETE` 边界行；拿不到锁时只做行删除。同步更新 memstore 与三个清理包的接口、日志字段和测试。
- [x] 6.2 store 接口新增 `DeleteResponseTurnsBefore`、`DeleteAuditLogsBefore`，gormstore 按同一逻辑实现，memstore 同步实现。
- [x] 6.3 照 `usageretention` 的结构新增 `internal/responseturnretention` 与 `internal/auditretention`，并补单元测试。
- [x] 6.4 `config.go` 新增 `ResponseTurnRetention`（默认 30 天）、`AuditLogRetention`（默认 400 天）、`DefaultResponseTurnRetention`/`DefaultAuditLogRetention`；负值报错；按天分区的三张表保留期上限 3650 天。补配置测试。
- [x] 6.5 在 `servicecontext.go` 中接入两个新清理协程（间隔分别与 `request_logs`、`usage_records` 相同）。
- [x] 6.6 确认 `previous_response_id` 指向已清理的轮次时，返回与未知 id 相同的错误，并补 logic 测试。

## 7. Gateway 契约测试

- [x] 7.1 在 Gateway 请求日志与用量账本的 outbox 测试中断言：推送失败后重试，每条记录的 `created_at` 与首次推送相同，且 JSON 往返后仍相同。

## 8. Live 测试（真实 MySQL 9.7 与 PostgreSQL）

- [x] 8.1 升级测试：用旧迁移建表并写入跨多个周期的历史数据（含 `audit_logs` 的 NULL 时间），执行新迁移，比对行数、逐行列值和索引；`AUTO_INCREMENT` 或序列不回退；schema 检查通过。
- [x] 8.2 resume 测试：MySQL 在转换完成后把账本手工标为 dirty，显式 resume 后能完成且数据不变；PG 转换失败时整体回滚。
- [x] 8.3 幂等测试：分区表上重复推送请求日志、用量记录不产生重复行；在开启计费时，`ChargeUsage` 重复推送不重复扣费；指标 upsert 按序列合并。
- [x] 8.4 维护测试：预建范围正确；两个 goroutine 模拟两个副本并发维护，最终分区集合正确；兜底分区有数据时补建分区，行全部进入新分区且行数不变。
- [x] 8.5 清理测试：截止时间落在分区中间时精确到行；整段过期的分区被删除；`p_hist` 过期后被删除；两个副本并发清理都成功。
- [x] 8.6 查询回归：`GetResponseTurn`、按请求 id 过滤请求日志、审计分页和结算汇总的结果与分区前一致；用 `EXPLAIN` 核对审计分页与时间范围查询会做分区裁剪且没有全表排序，结论记入 design.md 的风险一节。
- [x] 8.7 计时：在 MySQL 和 PG 上各对百万行级别的 `request_logs` 计时转换，结果写入运维文档作为设置 `MigrationTimeout` 的参考。

## 9. 文档

- [x] 9.1 `deploy/database-recovery.md`：分区表的备份与恢复、恢复后由维护任务补齐分区、升级转换的耗时与 `MigrationTimeout`、不要修改 `sql_mode`、回滚只能从备份恢复。
- [x] 9.2 `deploy/controlplane.example.yaml`：新增 `ResponseTurnRetention`、`AuditLogRetention`、`Database.MigrationTimeout` 的说明与默认值。
- [x] 9.3 控制面 `README.md`/`STATUS.md`、`docs/architecture.md` 存储章节：分区布局、保留期一览、主键契约。
- [x] 9.4 根目录 `STATUS.md` 新增条目，写明升级须知（BREAKING：主键、审计 `NOT NULL`、新保留期会清理旧数据），并分开列出代码已交付、默认测试通过与真实环境验收的状态。

## 10. 收尾验证

- [x] 10.1 控制面与 Gateway 的默认测试、相关包的 `-race` 检查，以及第 8 节的 live 用例全部通过。
- [x] 10.2 执行 `openspec validate partition-time-series-tables --strict`，核对每条 spec 场景都有对应的测试或已记录的验证。

## 11. 审查修复（2026-11-14）

- [x] 11.1 新增可选 `Database.MaintenanceDSN` 与 `gormstore.WithMaintenanceDB`，分区维护与删分区走维护连接池（2 个连接），锁按数据库共用；启动失败与关闭时一并释放。
- [x] 11.2 保留期改为「至少保留到截止时间」：截止时间向下取整到分区起点，只整段删除分区，零散行分批删除（每批 5000 行）。
- [x] 11.3 删分区等锁最多 2 分钟，锁忙本轮跳过；元数据锁超时（MySQL 1205 / PG 55P03）报错重试；其它 DDL 失败退回分批逐行删除并报错。
- [x] 11.4 清理协程改为每小时运行。
- [x] 11.5 定时维护的兜底分区搬移上限 20 万行，超出报错并提示 `-migrate up`；`MigrateAll` 完成迁移后执行不限搬移量的维护。
- [x] 11.6 分区 DDL 元数据锁等待降为 2 秒；MySQL 会话值事先读出、事后恢复，恢复失败丢弃连接。
- [x] 11.7 `CreateResponseTurn` 的重复查找限定在最近 24 小时。
- [x] 11.8 Live 用例：至少保留语义、迟到行逐行删除、无 DDL 权限退回、受限运行账号配合维护账号、积压交给迁移命令、MySQL 会话恢复；同步 spec、design、proposal 与文档。

## Context

动机见 proposal.md「Why」，行为契约见 `specs/time-partitioned-storage` 与 `specs/data-retention`。与设计相关的现状：

- **迁移框架**（`gormstore/migrate.go`）：每个命名空间一组内嵌 SQL 文件，账本记录文件名与 SHA-256 校验和。PostgreSQL 每个文件在一个事务里执行；MySQL 不用事务，失败时留下 dirty 记录，须显式 resume。`executeMigration` 已有按迁移 id 走 Go 逻辑的先例（`jobs` 命名空间的存在性判断）。`withMigrationLock` 用 `GET_LOCK(…, 0)` / `pg_try_advisory_lock` 做非阻塞互斥，上下文超时写死 5 分钟。
- **清理协程**：`requestlogretention`、`usageretention`、`metricshistory.Retention` 三个包各自复制同一个「定时 `DeleteXBefore(cutoff)`」的结构（项目刻意复制而不抽象），每个副本都会跑，靠 `DELETE` 的天然幂等保证多副本安全。
- **写入幂等**：`request_logs`、`usage_records` 的批量写入用 `OnConflict{DoNothing}` 撞主键去重。`created_at` 在 Gateway 请求开始时赋值（`httpapi/requestlog.go`、`httpapi/usageledger.go`）。用量账本的 pusher 失败后会原样重发同一批记录（`usageledgerpush.go`）；请求日志的 pusher 失败即丢弃、不重发（`requestlogpush.go`），所以不会出现同 id 换时间重发的情况。扣费去重另有 `usage_charges.request_id` 唯一约束，不依赖 `usage_records`。
- **ID**：`model.NewID` 是前缀加 128 位随机数的十六进制，不含时间，无法从 id 推出分区。
- **`sql/init.sql`**：按顺序拼接全部 MySQL 迁移得到的参考 schema，不是部署路径；默认测试会校验它与迁移一致。
- 五张表都没有外键引用，也没有被引用（已检查迁移目录中没有 `REFERENCES`/`FOREIGN KEY`）。

## Goals / Non-Goals

**Goals:**
- 新安装与升级走同一条迁移路径，转换只整表复制一次。
- 稳态下 DDL 都是瞬时的：建分区只切分空的兜底分区，删分区只动元数据。
- 分区个数由「保留期 ÷ 粒度 + 预建数」决定，不随运行时间增长。
- 存储接口对上层（logic、handler、memstore）的语义不变，只有清理的返回值从行数扩展为结构体。

**Non-Goals:**
- 不做在线、无停机的转换（不引入 pt-online-schema-change / gh-ost / pg_partman / 逻辑复制）。
- 不对 `jobs`、`job_artifacts`、计费表分区。
- 不做子分区（例如按 tenant 做 HASH）。
- 不提供关闭某张表保留期的开关（与现有 `RequestLogRetention` 一致，零值取默认值）。
- 不提供自动回滚迁移。

## Decisions

### D1. 动态分区边界：迁移 SQL 中的占位指令，执行时由 Go 展开

分区边界取决于执行迁移的日期和表里已有的数据，静态 SQL 写不出来。做法是在迁移文件里放一个占位注释，例如：

```sql
ALTER TABLE request_logs
  DROP PRIMARY KEY, ADD PRIMARY KEY (id, created_at)
  PARTITION BY RANGE COLUMNS(created_at) (/*aisw:partitions table=request_logs column=created_at unit=day*/);
```

`executeMigration` 识别出带占位的文件后：先查分区列的 `MIN()` 和当前时间，生成分区子句替换占位，再执行。校验和对展开前的模板计算，所以重复执行时账本校验和保持稳定。

展开规则（两种数据库相同）：
- 从 `MIN(分区列)` 所在周期开始，每个周期一个常规分区，一直建到「当前周期 + 预建数」（天表 7 天，月表 2 个月）。
- 常规分区最多 1000 个。数据早于这个窗口时，更早的行全部放进历史分区 `p_hist`（PG 为 `FROM (MINVALUE)`），过期后由清理整段删除。
- 最后放兜底分区：MySQL 为 `p_max VALUES LESS THAN (MAXVALUE)`，PG 为 `<table>_default DEFAULT`。
- 表为空时（新安装）只生成「当前周期 + 预建数」和兜底分区。

命名：MySQL 用 `p20261114` / `p202611` / `p_hist` / `p_max`；PG 子表用 `<table>_p20261114` / `<table>_p202611` / `<table>_hist` / `<table>_default`，最长 `metrics_history_points_p20261114` 共 32 字符，在 PG 的 63 字符上限以内。

**备选方案**：
- (a) 迁移里只建兜底分区，启动后由维护任务切分：已有数据都在兜底分区里，切分要再搬一遍数据，而且是在运行时阻塞写入，排除。
- (b) 把整个转换写成纯 Go 代码，不进 SQL 文件：DDL 脱离了可审阅的版本化 SQL，违背现有迁移约定，排除。
- (c) 在 SQL 里写死一个日期作边界：实际升级日期不确定，排除。

### D2. 转换方式：MySQL 单条 ALTER，PG 建新表、拷贝、改名

- **MySQL**：主键变更、`NOT NULL`、`PARTITION BY` 合并成一条 `ALTER TABLE`（语法为 `alter_option[, …] partition_options`），只重建一次表（`ALGORITHM=COPY`，期间可读不可写）。`audit_logs` 在同一文件中先执行 `UPDATE … SET created_at = UTC_TIMESTAMP(3) WHERE created_at IS NULL`。MySQL 迁移不在事务里，所以 Go 钩子先查 `information_schema.PARTITIONS`：表已分区就跳过 ALTER，保证 resume 可重复执行。`AUTO_INCREMENT` 计数器会被 ALTER 保留。
- **PostgreSQL**：PG 不能把普通表原地改成分区表。在迁移事务里依次执行：删除旧表的二级索引与主键约束（否则新表的 `<table>_pkey` 和索引名会冲突）→ 旧表改名为 `<table>_unpartitioned` → 创建分区父表（完整列定义，包含后来加的列）→ 按占位展开创建子表 → `INSERT INTO <table> SELECT <显式列> FROM <table>_unpartitioned` → 删除旧表 → 在父表上建二级索引（拷贝完再建，比边拷贝边维护索引快）。整个过程在一个事务里，失败就整体回滚。
  - `metrics_history_points.id` 是 `BIGSERIAL`：删旧表会连带删掉它拥有的序列。处理方式是先 `ALTER SEQUENCE … OWNED BY NONE`，新表 `id` 的默认值沿用这个序列，最后再 `OWNED BY` 新表的列，计数不会回退。

**实测结论（2026-11-14，MySQL 9.7.4、PostgreSQL 16 与 18）**：
- MySQL 一条 `ALTER TABLE … MODIFY …, DROP PRIMARY KEY, ADD PRIMARY KEY (…) PARTITION BY RANGE COLUMNS(…) (…)` 可以执行成功；复合主键下 `ON DUPLICATE KEY UPDATE` 去重有效。
- MySQL `REORGANIZE PARTITION p_max` 会把兜底分区里已有的行正确搬进新分区。
- MySQL 按 `(tenant_id, created_at, id)` 倒序分页走的是跨分区的索引倒序扫描，没有 filesort。
- PG 的建表、拷贝、改名路径（含 `BIGSERIAL` 序列的归属交接）、分区父表上的 `ON CONFLICT DO NOTHING` / `ON CONFLICT (metric, labels, bucket_at) DO UPDATE`、`DETACH` → 搬移 → `ATTACH` 默认分区，在 16 和 18 上结果一致；`DELETE … WHERE 分区列 < ?` 只扫描相关分区。
- 五个模型只经过 `Create`、条件查询和条件 `Delete`，没有按主键读写的调用，复合主键不影响调用方。

**备选方案**：PG 用 `ATTACH PARTITION` 把旧表整个挂成历史分区，不需要拷贝。但这个历史分区的主键仍只有 `id`，与父表的主键约束不一致：PG 要求挂上来的分区有匹配的唯一索引，补建这个索引同样要扫全表，还会留下一个结构特殊的分区，排除。

### D3. 迁移超时可配置

新增 `Database.MigrationTimeout`，默认 5 分钟（行为不变），取代 `withMigrationLock` 里写死的值。转换前记录每张表的估算行数（MySQL 用 `information_schema.TABLES.TABLE_ROWS`，PG 用 `pg_class.reltuples`），便于运维判断要不要调大超时。文档给出经验值：升级前先在副本上演练，测量实际耗时。

### D4. 主键 `(id, 分区列)` 与 GORM

- 模型给分区列也加上 `primaryKey` tag。`metrics_history_points.id` 显式加 `autoIncrement`，因为 GORM 在复合主键下默认不自增。
- `OnConflict{DoNothing}` 在 PG 生成不带冲突目标的 `ON CONFLICT DO NOTHING`，对分区表有效；在 MySQL 生成 `ON DUPLICATE KEY UPDATE`，以复合主键判定。两者都由 live 测试验证。
- `metrics_history_points` 的 upsert 冲突目标 `(metric, labels, bucket_at)` 已包含分区列，PG 分区表接受这个唯一索引。
- `response_turns` 的 id 来自 Gateway（`resp_` 加随机数），`created_at` 则是控制面的时钟，Gateway 重试时会变，复合主键挡不住重复。gormstore 的 `CreateResponseTurn` 改为先 `SELECT tenant_id … WHERE id = ? LIMIT 1`：命中就返回 `ErrConflict`，logic 层已有的逻辑会读出已有行或报告租户冲突；没命中再插入。并发重试可能留下两行内容相同的记录，读取结果不变，过期后一起清理。备选方案是另建不分区的 id 唯一表，代价是多一张表，而且那张表仍要逐行删除，所以不采用。
- 实现时要 grep 这五个模型按主键读写的用法（`First(&x, id)`、`Save`、`Delete(&x)`）：复合主键下，这些调用会要求两个主键字段都有值。

### D5. 分区维护：`partitionmaint` 定时调用 store

- gormstore 维护一张分区表清单（表名、分区列、粒度、预建数），这是唯一的真相来源，迁移展开、维护、清理和 schemacheck 都读它。
- 新增包 `internal/partitionmaint`，只负责定时（每小时）调用 `store.MaintainPartitions(ctx, now)`。memstore 实现为空操作。
- 启动时在迁移完成后同步执行一次，限时 30 秒，失败只记日志、不阻止启动（有兜底分区，写入不受影响），之后由定时器接手。
- **MySQL**：`ALTER TABLE t REORGANIZE PARTITION p_max INTO (PARTITION pX VALUES LESS THAN (...), …, PARTITION p_max VALUES LESS THAN (MAXVALUE))`。`p_max` 为空时只建空分区，瞬时完成；有数据时会正确搬移（满足 spec 的极端情况），耗时与搬移行数成正比。
- **PG**：先查 `<table>_default` 里有没有落在新范围内的行。没有就直接 `CREATE TABLE … PARTITION OF … FOR VALUES FROM … TO …`。有的话在一个事务里：`DETACH` 默认分区 → 建新分区 → 把范围内的行从被摘下的默认分区插入父表并删除 → 重新 `ATTACH` 默认分区。
- **互斥**：直接复用迁移锁的非阻塞获取。拿不到就跳过本轮（返回「已跳过」，不报错），这样就同时与迁移、其它副本的维护和分区删除互斥。
- **锁等待保护**：DDL 前设置会话级短等待，MySQL 用 `SET SESSION lock_wait_timeout = 5`，PG 用 `SET LOCAL lock_timeout = '5s'`，避免 DDL 排在长事务后面时连带阻塞后续查询；超时则本轮失败，下一轮重试。

**备选方案**：MySQL Event Scheduler 或 PG 的 pg_partman / pg_cron。依赖数据库侧配置与扩展，部署面变大，不能与迁移锁互斥，两种数据库的行为也不一致，排除。

### D6. 清理：先删整段分区，再删边界行

`DeleteXBefore(ctx, before)` 的返回值改为 `RetentionResult{PartitionsDropped, RowsDeleted int64}`，三个既有清理包与两个新包都以它记日志。实现分两步：

1. 非阻塞获取迁移锁。拿到后列出上界不晚于 `before` 的常规分区和 `p_hist`（兜底分区永远不删），一条语句删除：MySQL 用 `ALTER TABLE t DROP PARTITION a, b, …`，PG 用 `DROP TABLE t_a, t_b, …`（删分区子表只短暂锁父表）。同样设置 D5 的短等待。
2. 无论是否拿到锁，都执行 `DELETE FROM t WHERE 分区列 < ?`。分区裁剪让它只触达边界分区（以及升级后尚未整段过期的 `p_hist`）。

第 1 步拿不到锁时只做第 2 步，结果仍然满足「清理精确到行」，只是这一轮代价较高。删掉的分区不统计行数（`COUNT` 本身就要扫表），日志中记分区数。

**备选方案**：清理只删分区、不删边界行，保留期精度退化为一个分区粒度（月表最多多留约 31 天）。会改变现有「精确到行」的语义，`usage_records` 的结算口径也会受影响，排除。

### D7. 两个新清理协程照搬既有结构

新增 `internal/responseturnretention` 与 `internal/auditretention`，结构照抄 `usageretention`，与项目「复制形状、不抽象」的既有选择一致。store 接口新增 `DeleteResponseTurnsBefore` / `DeleteAuditLogsBefore`，memstore 同步实现。清理间隔：`response_turns` 与 `request_logs` 相同，`audit_logs` 与 `usage_records` 相同。

### D8. 配置

`config.go` 新增 `ResponseTurnRetention`、`AuditLogRetention`（零值取默认值，负值报错）、`Database.MigrationTimeout`；按天分区的三张表保留期超过 3650 天时报错。`deploy/controlplane.example.yaml` 写明新配置项与默认值。

### D9. schemacheck

在 `checkSchemaObjects` 中加分区校验：对清单中的每张表，MySQL 查 `information_schema.PARTITIONS` 的 `PARTITION_METHOD = 'RANGE COLUMNS'` 与 `PARTITION_EXPRESSION`，并确认有 `p_max`；PG 查 `pg_partitioned_table` + `pg_attribute` 确认分区列，并确认存在默认分区。现有的列与索引校验应能通过分区表的父表完成，实现时验证 GORM `ColumnTypes`/`HasIndex` 在分区父表上的表现。

### D10. `sql/init.sql`

生成器把占位展开为只有兜底分区（即空表的展开结果去掉预建分区），让参考 schema 保持可读、可执行，同时说明真实部署由迁移和维护生成分区。

## Risks / Trade-offs

- [数据库不再保证 `id` 单独唯一] → `request_logs`/`usage_records` 依靠 Gateway 重发保持原 `created_at`，新增 Gateway 测试固化这一契约；扣费去重仍由不分区的 `usage_charges.request_id` 兜底。`response_turns`/`audit_logs` 的 id 是 128 位随机数，碰撞概率可以忽略。
- [不带分区列的查询要探测所有分区]（`GetResponseTurn` 按 id 读、审计按租户分页） → 稳态分区数约为：请求日志 37、对话轮次 37、指标 97、用量 16、审计 16，都在可接受范围。已用 `EXPLAIN` 核对（100 万行 `request_logs`、41 个分区）：按 `(tenant_id, created_at, id)` 倒序分页，MySQL 9.7 是跨分区的索引倒序扫描（无 filesort），PostgreSQL 16 是各分区倒序索引扫描再 Merge Append（无全量排序）；单日时间范围查询在两种数据库上都只扫描当天一个分区。
- **实测耗时（2026-11-14，本机 Docker，100 万行 `request_logs` 分布在 30 天内）**：转换 MySQL 9.7 为 22.4 秒，PostgreSQL 16 为 5.6 秒（拷贝 3.7 秒，建三个索引 1.9 秒）。清理 15.8 万行，MySQL 用 `DELETE` 要 1.81 秒，`DROP PARTITION` 删 5 个分区（16.7 万行）只要 0.085 秒；PostgreSQL 的 `DELETE` 只要 40 毫秒，但会留下死元组等 VACUUM 回收，删分区则在 10 毫秒以内且不留死元组。生产硬件与数据量不同，升级前仍需在恢复副本上实测。
- [大表转换期间不可写、耗时不确定] → 现行升级流程本就要求停掉全部副本，Gateway outbox 会缓冲请求日志与用量；`MigrationTimeout` 可调；文档要求先在副本或恢复演练环境实测。
- [DDL 被长事务阻塞并引发 MDL 堆积] → 会话级短等待（D5），超时下一轮重试。
- [维护长期停止，兜底分区积累数据] → 写入不受影响；恢复后的第一轮维护会搬移数据（MySQL REORGANIZE 期间阻塞写入），耗时与积累量成正比；维护失败会有错误日志，可以据此告警。
- [升级时清理掉旧数据：超过 30 天的对话轮次、超过 400 天的审计记录] → 已在 proposal 标为 BREAKING，写入升级说明；需要更长保留期的部署在升级前调大配置。
- [MySQL 分区表不保存创建时的 SQL mode] → 文档注明升级后不要修改服务器 `sql_mode`（官方限制）。
- [`ALTER TABLE … ADD COLUMN … ALGORITHM=INSTANT` 后无法 EXCHANGE PARTITION] → 本方案不使用 EXCHANGE PARTITION，不受影响；在设计中记录，供后续变更参考。
- [备份恢复] → mysqldump/pg_dump 都会导出分区定义，恢复后由维护任务补齐缺失的未来分区；`deploy/database-recovery.md` 补充说明，并在恢复演练中验证。

## Migration Plan

1. 升级前：按 `deploy/database-recovery.md` 做完整备份；在恢复演练环境上用生产数据量执行一次升级，记录转换耗时，据此设置 `Database.MigrationTimeout`；如需保留更久的对话轮次或审计记录，先调大 `ResponseTurnRetention`/`AuditLogRetention`。
2. 停止全部控制面副本（现行流程）。Gateway 可以继续运行，请求日志和用量会在 outbox 中缓冲。
3. 启动一个新版本控制面执行迁移（`AutoMigrate` 或显式迁移命令）。依次执行：`base/0017`（`audit_logs`）、`metrics_history/0002`、`request_logs/0004`、`response_turns/0002`、`usage_records/0002`。
4. 迁移成功后启动时会同步跑一次分区维护和 schema 检查，再启动其余副本。
5. 观察日志：第一轮清理会把超过保留期的历史数据整段删掉，日志中应能看到 `partitions_dropped`。

**回滚**：没有反向迁移。旧版本看到账本里有未知迁移会拒绝启动，所以回滚只能从第 1 步的备份恢复，然后启动旧版本。Gateway outbox 中尚未确认的记录会重新推送给恢复后的库。

## Open Questions

（已结：常规分区上限 1000、预建 7 天 / 2 个月用常量，不做成配置。）

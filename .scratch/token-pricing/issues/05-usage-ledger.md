# 05 余额账目按天封账

Status: ready-for-human

实现（2026-11-16）：代码、默认测试与 live 数据库用例（PostgreSQL 16、MySQL 9.7）都已通过。

规格：[spec.md](../spec.md)「余额账目（`credit_ledger`）」。

## 范围

迁移 `billing/0014_usage_ledger.sql`；扣费事务累加到 `tenant_billing`；`applyCredit` 写入前先封账；账目时间严格递增；后台封账任务；`Billing.LedgerTimezone`（引入 `time/tzdata`）；memstore；账目 API 的新字段与 `pending_usage`；运维租户列表的未封账用量；压缩命令 `-migrate compact-usage-ledger`（使用 `Database.MaintenanceDSN`）。

## 验收

规格「验收要点 / 余额账目」；确认控制面没有对 `credit_ledger` 的 UPDATE 或 DELETE。

## Comments

### 2026-11-16 实现记录

- 共用规则 `store/usageledger.go`：`NextLedgerTime`（微秒精度、严格晚于上一条）、`AccrueUsage`、`SealDue`、`SealUsage`、`NextLocalMidnight`；两套 store 都在租户计费行锁下调用。
- `ChargeUsage(ctx, records, now, sealAt)`：已到期的累计先封账，再扣余额、累加；不再逐批写账目。`applyCredit` 写入前先封账，并让账目时间严格递增；`InitTenantBilling` 记下赠送账目的时间。只有这三处写账目，退款、争议、支付宝入账、调整都经过 `applyCredit`。
- `last_ledger_at` 为 NULL 的旧租户，封账时先从账目读出最新时间（`loadLastLedgerAt`）。
- 定时封账：`store.SealDueUsage`（按 `usage_acc_until` 索引、每轮最多 500 个租户、每个租户一个事务并在锁内复查）；`logic.RunUsageLedgerSeal` 每分钟一次，开启计费时随控制面启动、关闭时停止。
- 配置 `Billing.LedgerTimezone`（默认 UTC，名称无效拒绝启动），`config` 包引入 `time/tzdata`。
- 迁移 `billing/0014_usage_ledger.sql`（MySQL 对 `credit_ledger` 用 `ALGORITHM=INSTANT`）；`init.sql` 已重新生成。
- 接口：账目新增 `period_start`、`period_end`、`request_count`；单一租户的账目列表带 `pending_usage`（`amount_micro` 为负数，与用量账目一致：最新一条的 `balance_after` 加上它就是余额）；运维租户列表带 `pending_usage`。
- 压缩命令 `-migrate compact-usage-ledger`：按 `Billing.LedgerTimezone` 合并同一天、中间没有其他账目的连续用量账目；设置了 `Database.MaintenanceDSN` 时用它执行；每一串一个事务，可中断后再执行。`-migrate` 现在同时读取配置文件的 `Billing` 段。
- 测试：共享场景 `storetest.UsageLedger` 与 `storetest.CheckLedgerChain`（memstore、gormstore live），覆盖累计、入账先封账、期限前后的定时封账、跨日由扣费封账、同一时刻与时钟倒退、扣费 / 入账 / 封账并发；gormstore live 另测旧租户；`TestLiveCompactUsageLedger`（台北时区日界、调整分段、重复执行）；`TestEzfpRefunds` 结尾检查退款流程的余额链；日界（含纽约夏令时两端）、账目时间、时区配置与账目接口的单元测试。
- 已确认：除压缩命令外，控制面没有对 `credit_ledger` 的 UPDATE 或 DELETE。
- 修正：分区升级的 live 用例在升级前表结构上写种子数据时，省略并在比对时扣除 `usage_records/0005` 新增的列（第 1 项遗留，Docker 可用后才发现）。

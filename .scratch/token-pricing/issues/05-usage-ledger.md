# 05 余额账目按天封账

Status: ready-for-agent

规格：[spec.md](../spec.md)「余额账目（`credit_ledger`）」。

## 范围

迁移 `billing/0014_usage_ledger.sql`；扣费事务累加到 `tenant_billing`；`applyCredit` 写入前先封账；账目时间严格递增；后台封账任务；`Billing.LedgerTimezone`（引入 `time/tzdata`）；memstore；账目 API 的新字段与 `pending_usage`；运维租户列表的未封账用量；压缩命令 `-migrate compact-usage-ledger`（使用 `Database.MaintenanceDSN`）。

## 验收

规格「验收要点 / 余额账目」；确认控制面没有对 `credit_ledger` 的 UPDATE 或 DELETE。

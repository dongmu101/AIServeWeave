# 09 文档

Status: ready-for-agent

Blocked by: 01–08

## 范围

- 控制面 STATUS「计费与充值」与用量账本两节：新的计价规则、价目表、定价配置与方案、扣费防重、余额账目，以及「费用不进 `usage_records`」「汇总不做定价」两条约定的修订。
- Gateway、Agent STATUS：计量项上报。
- 根 STATUS 交付记录与升级须知（升级顺序、行为变更、不能直接回退、回填与压缩命令、`Billing.LedgerTimezone`）。
- `GLOSSARY.md`：计量项、价目表、基础价、模型倍率、价格方案、方案系数、售价。
- 部署配置示例与 `deploy/database-recovery.md` 的新迁移说明。

## Comments

### 2026-11-16 进度

已写：
- 新文档 `docs/billing-token-pricing.md`（写给运维：计价规则、计量项、按请求时间计价、模型定价 / 价格方案 / 价目表审核、余额账目、配置、升级顺序与迁移锁、行为变更、不能直接回退、维护命令、验证边界）。
- 控制面 STATUS：「用量账本」（新列与 `0006` 索引、计量项明细、去重、汇总新列、逐笔明细，修订「汇总不做定价」）与「计费与充值」（计价、按请求时间、数据表、修订「费用不进 usage_records」、扣费与余额账目、维护命令、接口、审计动作、已知缺口），以及分区与公开目录两处引用。
- Gateway STATUS：用量账本第 4、7、8 点（去重、计量项明细与过滤指标、原生 Messages 缓存 token 修正）。
- Agent STATUS：新增「用量的计量项明细」。
- `GLOSSARY.md`：计量项、价目表、基础价、模型倍率、价格方案、方案系数、售价、用量账目。
- `deploy/controlplane.example.yaml`：计费段说明与 `PriceCatalog`、`LedgerTimezone` 示例。
- `deploy/database-recovery.md`：新增「按 token 计价」迁移一节，修正分区一节的扣费去重说明。
- `docs/billing-alipay.md`：模型售价的设置入口。

待第 7、8 项合并后：根 STATUS 交付记录、Console README 与 STATUS 的计费页说明。

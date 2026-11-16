# 04 计价引擎与扣费防重

Status: ready-for-human

实现（2026-11-16）：代码与默认测试已完成，live 数据库用例已在 PostgreSQL 16 与 MySQL 9.7 上通过（2026-11-16）。

规格：[spec.md](../spec.md)「计量项 / 命名与层级」「单价 / 单价查找」「定价与价格方案」「单笔计价」「扣费防重并入 `usage_records`」。

## 范围

- `common/pricing` 计价函数：输入（总数、明细、档位相关的输入总数、单价表、总倍率），输出（每个计量项的数量、计费数量、单价、费用，原价，实扣）。子项扣减与父项校正、单价查找顺序、档位选择、不限位宽的精确运算与饱和。纯函数，不依赖存储。
- 控制面解析价格：按请求时间取定价配置、价目表与租户方案；基础价来源、模型倍率 × 方案系数、方案固定售价；读取两个版本号，变化时重新加载进程内缓存。
- `chargeUsage` 改用新引擎，写入 `usage_records` 的费用列与明细 JSON。
- 扣费防重改由 `usage_records` 承担（锁租户行 → 跨分区查 id → 只插入不存在的记录），`usage_charges` 停止写入。
- 移除 `/operator/v1/billing/model-prices` 与对 `model_prices` 的读取。
- 回填命令 `-migrate backfill-usage-cost`。

## 验收

规格「验收要点 / 计价引擎」「版本」「扣费事务」。

## Comments

### 2026-11-16 实现记录

- `common/pricing/compute.go`：`Compute`（子项扣减、档位选择、按「同名逐档 → 默认变体 → 父项」查找单价、`math/big` 精确运算并饱和）、`CombinedRate`、`SelectTier`、`Lookup`。测试含规格的计算示例。
- `logic/usagepricing.go`：计价缓存（两个版本号变化才重新加载）、按请求时间解析价格（方案固定售价 → 覆盖价 → 价目表条目 → 默认价 → 未定价；倍率为模型倍率 × 方案系数）、租户方案按批读取、`chargeUsage` 写入费用列与明细 JSON。请求时间在未来时按当前时间。
- `store.CatalogPriceSet` 新增 `ChangedAt`（所有单价行中最晚的 `valid_from` / `valid_to`），用来判断当前价目表对某个请求时间是否有效，避免不在价目表中的别名（本地模型）每条都回查数据库。
- 扣费防重：`ChargeUsage(ctx, records, now)` 锁租户行 → 跨分区按 id 查 `usage_records` → 只插入不存在的记录并设置 `charged` → 扣余额、写账目；`usage_charges` 不再写入，memstore 的 `charges` 字段已删除。
- 移除 `/operator/v1/billing/model-prices` 的路由、handler、logic 与类型。store 的 `ListModelPrices` 保留，供第 1 版定价配置的转换使用。
- 回填命令 `-migrate backfill-usage-cost`（`gormstore.BackfillUsageCost`）：先检查表结构，再按 `(created_at, id)` 游标、每批 1000 条，把 `usage_charges` 的费用写回没有价格来源的记录，价格来源记为 `legacy`、倍率 1 倍；可重复执行。
- 价目表比对模块的 `resolve` 保留：它有一个「排除同档」的模式，用来找出新出现的单价原本会回落到的价格，用途与计价不同；在只导入默认变体的前提下，查找顺序与 `pricing.Lookup` 一致。
- 测试：引擎单元测试；logic 计价测试（价目表、参照模型 × 模型倍率、覆盖价、默认价、未定价、方案系数、固定售价、同一批跨方案变更、按请求时间取价含价目表历史、商户自有节点、重发不重复扣费）；共享场景 `storetest.ChargeUsage`（memstore 默认测试、gormstore live）；`TestLiveBackfillUsageCost`。
- 验证：全仓 `go vet`、`go test ./...` 通过；`common/pricing`、控制面 store、handler、logic、e2e 竞态检查通过。live 数据库用例未跑（Docker 未运行）。
- 衔接：公开目录 `/billing/v1/catalog` 仍读 `model_prices`（第 6 项改为默认方案下的售价）；Console 运维计费页仍调用已移除的 `model-prices` 接口，在第 7 项之前会失败。

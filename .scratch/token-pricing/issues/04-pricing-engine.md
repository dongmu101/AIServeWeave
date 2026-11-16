# 04 计价引擎与扣费防重

Status: ready-for-agent

Blocked by: 01, 02, 03

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

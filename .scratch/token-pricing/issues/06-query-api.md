# 06 查询 API

Status: ready-for-human

实现（2026-11-16）：代码、默认测试、Console 检查与 live 数据库用例（PostgreSQL 16、MySQL 9.7）都已通过。

规格：[spec.md](../spec.md)「API」。

## 范围

- 用量汇总新增 `cache_read_tokens`、`cache_write_tokens`，开启计费时带 `cost_micro`。
- 逐笔明细 `GET /admin/v1/usage/records` 与 `/operator/v1/usage/records`（运维版带 `ref`，租户版不带）。
- `GET /admin/v1/billing/prices`（按租户方案）与 `GET /operator/v1/billing/effective-prices`。
- 公开目录 `/billing/v1/catalog` 改为默认方案下的售价。
- 未定价提示所需的数据（价格来源为 `none` 的别名、最近 24 小时 `price_source = none` 的用量数）。
- Console `/api/admin/*`、`/api/operator/*` 白名单放行新接口与参数。

- 注意（第 4 项之后）：公开目录 `/billing/v1/catalog` 目前仍读取 `model_prices`，内容已经不是实际售价，本项必须改掉。

## 验收

各接口的授权、分页、租户隔离与字段；计费关闭时不返回费用字段。

## Comments

### 2026-11-16 实现记录

- 新文件 handler / logic / types 的 `usagequery.go`，`storetest/usagequery.go` 共享场景（memstore、`TestLiveUsageQueries`）；`common/pricing.ApplyRates`（售价展示用，四舍五入；扣费仍用精确值）。
- **新增迁移 `usage_records/0006_tenant_created_index.sql`**：`(tenant_id, created_at, id)`。原有索引以 `model` 为第二列，租户不带 model 的倒序分页走不了它。这一步**不是即时完成**：MySQL 用 `ALGORITHM=INPLACE, LOCK=NONE`（不阻塞写入）；PostgreSQL 在分区父表上 `CREATE INDEX`，建索引期间阻塞对 `usage_records` 的写入（分区父表不支持 `CONCURRENTLY`）。第 9 项的升级须知必须写明，大表要先在恢复副本上计时。
- **偏离规格**：
  - 逐笔明细的时间参数沿用汇总接口的 `since` / `until`，不是规格写的 `from` / `to`。
  - 未定价提示的数据放在 `effective-prices` 响应里：`unpriced_aliases`、`unpriced_requests_24h`（跨租户）。
  - 公开目录保留 `input_micro_per_mtok` / `output_micro_per_mtok`（改为默认方案下的基础档售价，Console 价目页不用改），另加 `items` 列出全部售价；没有任何单价的别名不列出，避免被当成免费。
- 逐笔明细：明细 JSON 解码后重新组装，不透传原文；租户版不带 `ref`、`plan`、`rev`；计费关闭时不带费用与单价字段；没有明细的旧记录按总数补出 input、output 两行。
- 价格接口：运维版 = 路由别名 ∪ 已配置别名（后者 `routed: false`）；租户版只列路由别名、按模型授权过滤、只给总倍率；`plan_id` 不存在返回 404。
- Console 白名单：删除 `model-prices`；租户侧放行 `usage/records`、`billing/prices`；运维侧放行第 2、3、6 项的全部接口，第 7 项可直接使用。
- 合并：分身也修了分区 live 用例的同一个问题，合并时保留主线程的修法；`init.sql` 重新生成。
- 未做：EXPLAIN 核对。

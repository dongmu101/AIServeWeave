# 06 查询 API

Status: ready-for-agent

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

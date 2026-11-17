# 02 价目表

Status: ready-for-human

实现（2026-11-16）：代码与默认测试已完成并合并进主工作区，live 数据库用例已在 PostgreSQL 16 与 MySQL 9.7 上通过（2026-11-16）。

规格：[spec.md](../spec.md)「单价」「价目表」「数据变更 / `billing` 命名空间」「API / 运维」中价目表相关的接口。

## 范围

- **解析与映射**：把 LiteLLM 条目解析为 `pricing.PriceList`，字段映射、档位后缀、TTL 后缀、服务等级后缀（本期跳过）按规格；值不是数字的字段跳过；过滤规则按规格。USD / token 乘以 1e12 四舍五入为整数。
- **补齐档位**：某档缺少子项单价时，按「基础档子项单价 × 该档根单价 / 基础档根单价」补齐并标记为推算值。
- **存储**：迁移 `billing/0012_price_catalog.sql`（PostgreSQL、MySQL）建立 `catalog_models`、`catalog_prices`（带 `valid_from` / `valid_to`）、`catalog_pending`、`catalog_sync_state`（含价目表版本号）。同步 `sql/init.sql`。新增 store 接口及 gormstore、memstore 实现与 `storetest` 共享场景。
- **按时间查询**：提供「某个时刻生效的某个条目的单价表」与「当前全部生效条目」的读取，供第 4 项使用。
- **同步与比对**：来源接口（远端 URL、内嵌快照）；首次导入直接生效；之后新条目自动生效，改价超过 `AutoApplyMaxChangePercent`、降为 0、删除单价或删除条目时进入待审核；上游删除的条目标记 `removed_upstream` 并继续使用原价；拒绝后同一份上游内容不再提醒。写入用同步状态行上的内容哈希做比较并交换。拉取限 30 秒、20 MB，带 `Accept: application/vnd.github.raw`。
- **内嵌快照**：`go:embed` 一份按过滤规则裁剪过的上游文件，以及 `scripts/update-price-catalog.sh`（裁剪逻辑复用 Go 解析器，不另写一套）。本机可经 `api.github.com` 取得上游文件，`/tmp/litellm-ref/upstream.json` 是 2026-11-16 取得的副本。
- **配置**：`Billing.PriceCatalog`（`URL`、`RefreshInterval`、`AutoApplyMaxChangePercent`），默认值按规格。
- **后台任务**：开启计费时按 `RefreshInterval` 同步，在 `svc/servicecontext.go` 启动。
- **运维 API**：`GET /operator/v1/billing/catalog`、`POST .../catalog/sync`、`GET .../catalog/pending`、`POST .../catalog/pending/approve`、`.../reject`；审计 `billing.catalog.*`。

## 不在本项

参照模型的校验（第 3 项合并时接上）、计价（第 4 项）、Console（第 7 项）。

## 验收

规格「验收要点 / 价目表」全部场景，外加：真实上游文件解析不报错且条目数符合过滤规则；单位换算与四舍五入；补齐档位的数值。默认测试通过；live 数据库用例在合并后统一跑。

## Comments

### 2026-11-16 实现记录

- 新包 `internal/pricecatalog`：解析与字段映射、补齐档位（`parse.go`）、比对与审核规则（`diff.go`）、HTTP 来源与内嵌快照（`source.go`）、快照裁剪（`trim.go`、`cmd/pricecatalog-snapshot`）。内嵌快照 3689 条、约 1.1MB，由 2026-11-16 的上游文件生成；更新脚本 `scripts/update-price-catalog.sh`。
- store：`PriceCatalog` 接口与两套实现共用的事务规则，gormstore、memstore、`storetest` 共享场景，`TestLivePriceCatalog`（含完整快照导入与 schema 检查）。
- 迁移 `billing/0012_price_catalog.sql`；配置 `Billing.PriceCatalog`；开启计费时启动后台同步；运维 API 五个接口；审计 `billing.catalog.{sync,auto_apply,approve,reject}`。
- 规格未覆盖、按保守原则定的细节已写回规格「价目表 / 同步与审核」与「数据变更」：0 单价的处理、`removed` 状态、新计量项进入审核、`utf8mb4_bin`、条目名 255 字节、启动顺序、审计条数上限。
- 合并时由主线程接上：第 3 项的参照模型必须是状态不为 `removed` 的条目；参照模型长度上限改为引用 `pricecatalog.MaxModelLen`（255）。
- 单价查找在比对模块里有一份实现（`resolve`），第 4 项应统一收进 `common/pricing`。
- 验证：合并后全仓 `go vet`、`go test ./...` 通过；store、handler、pricecatalog 竞态检查通过。live 数据库用例未跑（Docker 未运行）。

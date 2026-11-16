# 03 定价配置与价格方案

Status: ready-for-human

实现（2026-11-16）：代码与默认测试已完成并合并进主工作区，live 数据库用例待运行。

规格：[spec.md](../spec.md)「定价与价格方案」「数据变更 / `billing` 命名空间」「API / 运维」中定价与方案相关的接口。

## 范围

- **定价配置版本**：迁移 `billing/0013_pricing_config.sql`（PostgreSQL、MySQL）建立 `pricing_revisions`（自增 id、`effective_at`、JSON 文档、`created_by`、备注）、`tenant_price_plans`（`valid_from` / `valid_to`）、`pricing_config_state`（定价配置版本号）。同步 `sql/init.sql`。
- **文档结构**（Go 类型 + JSON，带格式版本号）：默认价（`pricing.PriceList`）；按别名的参照模型、覆盖价、模型倍率；价格方案（名称、默认系数、按别名的系数或固定售价）；默认方案 id。校验用 `pricing.PriceList.Validate` 与 `pricing.ValidRate`。
- **迁移转换**：迁移时把现有 `model_prices` 转成第一个版本：每行变成该别名的覆盖价（`input`、`output`），模型倍率 1；`*` 行变成默认价；建立系数为 1 的默认方案。`model_prices` 表保留不删。若迁移 SQL 难以在两种数据库上生成 JSON，可改为控制面启动时由 Go 代码完成首次转换（须幂等、多副本安全），在任务评论里说明选择。
- **修改**：每次修改追加一个完整版本，须带 `base_revision`，版本已变化返回 409；同一事务内版本号加一。
- **租户方案**：`PUT /operator/v1/billing/tenants/:id/plan`；写入新行并关闭旧行，同一事务内版本号加一。默认方案不能删除；还有租户在用的方案返回 409。
- **按时间查询**：提供「某个时刻生效的定价配置」与「某个时刻租户所属方案」的读取，供第 4 项使用。
- **运维 API**：`GET /operator/v1/billing/pricing`、`PUT/DELETE .../pricing/models`、`PUT .../pricing/default-prices`、`GET/POST .../plans`、`PUT/DELETE .../plans/:id`、`PUT .../tenants/:id/plan`、`GET .../pricing/revisions`；`GET .../tenants` 每行带方案。全部写审计。
- store 接口、gormstore、memstore 与 `storetest` 共享场景。

## 不在本项

- 旧的 `/operator/v1/billing/model-prices` 与 `chargeUsage` 对 `model_prices` 的读取**暂时保留**，由第 4 项切换并移除。本项不改扣费行为。
- 参照模型是否存在于价目表的校验：本项只校验名称格式，第 2 项合并后接上。
- `effective-prices`（第 6 项）。

## 验收

转换（含 `*` 行、无行、多行）；版本冲突 409；方案删除规则；租户方案历史与按时间查询；校验错误；审计。默认测试通过；live 数据库用例在合并后统一跑。

## Comments

### 2026-11-16 实现记录

- 新包 `internal/pricingconfig`：不依赖存储的文档类型、校验、编码与修改函数，另有给第 4 项用的 `PlanFor`、`ModelRate`、`PlanTerms`。
- store：`PricingConfig` 接口（`ErrPricingRevisionChanged`、`ErrPricePlanInUse` 都包装 `ErrConflict`），gormstore、memstore、`storetest` 共享场景，`TestLivePricingConfig`。所有写入都在 `pricing_config_state` 行锁下以 READ COMMITTED 进行，生效时间不会因副本时钟落后而倒退。
- 迁移 `billing/0013_pricing_config.sql`（MySQL 文档列用 `MEDIUMTEXT`，文档上限 4 MiB）；运维 API 十个接口；租户列表带 `price_plan_id`；全部写审计。
- **偏离规格**：`model_prices` 的转换改为第一次读取时由 Go 代码完成（规格已同步）。第 4 项的扣费请直接调用 `EnsurePricingConfig` 与 `PricingConfigAt`。
- 按保守原则定的细节：删除默认方案 409；分配不存在的方案 400；修改接口都要求 `base_revision`（缺失 400、过期 409）；更新方案时省略 `default_rate_ppm` 或 `models` 表示保持原值；方案分配在锁内核对版本号，期间方案被删时重读最多 3 次；回到默认方案时只关闭当前行。
- 合并时由主线程接上参照模型的存在性校验（`checkCatalogReference`），并把原有用到参照模型的测试改为先写入价目表。
- 待后续处理：旧的 `/operator/v1/billing/model-prices` 在第 1 版生成后改价不会反映到新配置，第 4 项须移除；Console 白名单（第 6、7 项）；方案条款很多时可能超过 64 KB 的请求体上限（第 7 项评估）。
- 验证：合并后全仓 `go vet`、`go test ./...` 通过；pricingconfig、store、handler、e2e 竞态检查通过。live 数据库用例未跑（Docker 未运行）。

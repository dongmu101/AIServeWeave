# 01 计量项上报

Status: ready-for-human

实现（2026-11-16）：代码与默认测试已完成，等待与第 2、3 项合并后统一跑 live 数据库用例。

规格：[spec.md](../spec.md)「计量项」「上报链路」「数据变更 / `usage_records/0005_pricing.sql`」。

## 范围

把缓存读取、缓存写入（含 1 小时）与推理 token 从上游一路传到控制面并存下来。**本项不计价**，费用列只建不写。

- `common/runtime.Usage` 新增 `Details map[string]int64`、`Attributes map[string]string`。计量项名称与上限使用 `common/pricing`。
- 上游 adapter：
  - Anthropic Messages（`common/runtime/anthropic`，流式与非流式）：`input.cache_read`、`input.cache_write`、`input.cache_write.1h`（`cache_creation.ephemeral_1h_input_tokens`）。
  - OpenAI Chat（`common/runtime/openai`，流式与非流式）：`prompt_tokens_details.cached_tokens` → `input.cache_read`。
  - Responses：`input_tokens_details.cached_tokens` → `input.cache_read`；推理沿用 `ReasoningTokens`。
- 隧道协议 `Usage`：`map<string, int64> details = 5`、`map<string, string> attributes = 6`，重新生成代码；Agent 与 Gateway 两侧的转换。
- Agent runtime：Claude Code、Codex、Cursor 等 CLI runtime（`service/aiServeWeaveAgent/cliagent`、`codexcli`、`claudecode`），以及远端供应商 runtime：上游报告了缓存数量时填写明细。
- Gateway：`recordUsage` 带上明细（`ReasoningTokens` 并入 `output.reasoning`）；推送前用 `pricing.SanitizeDetails` / `SanitizeAttributes` 过滤，丢弃项记指标；推送 JSON 新增 `details`、`attributes`，为空时省略。
- 控制面：`decodeUsageRecords` 接受这两个字段，用 `pricing.ValidateDetails` / `ValidateAttributes` 严格校验；写入 `usage_records` 的 `details`（明细 JSON 格式 v1，只含数量，见规格「单笔计价」）以及 `cache_read_tokens`、`cache_write_tokens`。计费关闭与开启两条入账路径都要写。
- 迁移 `usage_records/0005_pricing.sql`（PostgreSQL、MySQL）：一次建好规格列出的全部列（`cache_read_tokens`、`cache_write_tokens`、`cost_micro`、`base_cost_micro`、`rate_ppm`、`price_source`、`details`、`charged`）；同步 `sql/init.sql`。须是即时加列。
- memstore 同样实现。

## 不在本项

计价、费用列的写入、汇总 API 的新列（第 4、6 项）。

## 验收

- adapter 单元测试：各协议流式与非流式都取到明细；上游没报告时明细为空。
- 隧道：新旧版本互通（旧 Agent 不带明细、旧 Gateway 忽略新字段）。
- Gateway：过滤规则、推送契约测试（字段名与省略规则）。
- 控制面：校验上限与错误返回、两条入账路径写入明细与缓存列、重复推送不重复写。
- 默认测试通过；live 数据库用例在合并后统一跑。

## Comments

### 2026-11-16 实现记录

- `common/pricing`：计量项命名、明细与属性的校验和过滤、`Quantities`（父项不小于子项之和的校正）、单价表类型。
- `runtime.Usage` 新增 `Details`、`Attributes` 与 `AddDetail`；Anthropic 与 OpenAI adapter 填写缓存明细；隧道 `Usage` 新增 `details = 5`、`attributes = 6`（重新生成的 `tunnel.pb.go` 文件头版本号随本机工具链变化，`tunnel_grpc.pb.go` 未变）。
- Agent：Claude 风格与 Cursor 的 CLI 用量改用 `usageWithDetails`（含 1 小时缓存写入）；Codex 读取 `cachedInputTokens`。
- Gateway：账本记录带过滤后的明细（推理并入 `output.reasoning`），新指标 `gateway_usage_details_dropped_total`；推送 JSON 新增 `details`、`attributes`，为空时省略。
- 控制面：迁移 `usage_records/0005_pricing.sql`（MySQL 用 `ALGORITHM=INSTANT`）；入账写入明细 JSON（`model.UsageDetails` v1）与缓存列；`init.sql` 已重新生成。
- **偏离规格**：明细不合规时只丢明细、不整批拒绝；请求体上限提高到 4MiB。规格已同步。
- **修正既有漏计**：原生 Messages 路径（Claude CLI 桥接）原本漏计缓存 token，见规格「上报链路」与「升级与兼容」。
- 验证：`go vet ./...`、`go test ./...` 全部通过；`common/pricing`、`tunnelwire`、Gateway `httpapi`/`controlplaneclient`、控制面 `logic`/`handler` 竞态检查通过。live 数据库用例（`0005` 在 MySQL 9.7 与 PostgreSQL 16 上即时加列）待合并后运行。

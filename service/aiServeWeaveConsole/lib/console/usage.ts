import { ApiError } from "./errors.ts";

/**
 * This module mirrors GET /admin/v1/usage/summary and
 * GET /operator/v1/usage/summary (internal/types.UsageSummaryResponse), which
 * the control plane builds by grouping the usage_records table into one row
 * per (tenant, model) pair and summing its token fields in the database
 * (STATUS.md's persisted usage ledger, P2).
 *
 * 本模块镜像 GET /admin/v1/usage/summary 与 GET /operator/v1/usage/summary
 * (internal/types.UsageSummaryResponse)，控制面把 usage_records 表按
 * (租户, 模型) 分组成每对一行、在数据库内对其 token 字段求和来构造它
 * (STATUS.md 的持久化用量账本，P2)。
 */

/** UsageSummaryEntry is one (tenant, model) row of a usage summary.
 *
 * `tenantId` is "" on the tenant-scoped endpoint, which omits the field
 * entirely because the caller already knows which tenant it is asking about.
 *
 * UsageSummaryEntry 是用量汇总里的一个 (租户, 模型) 行。
 *
 * `tenantId` 在租户自助端点上为 ""，该端点整体省略这个字段，因为调用方本就知道
 * 自己在问哪个租户。 */
export interface UsageSummaryEntry {
  tenantId: string;
  /** userId and keyId are set only for `group_by=key`; "" means unattributed.
   * userId 与 keyId 只在 `group_by=key` 时出现；"" 表示未归属。 */
  userId: string;
  keyId: string;
  model: string;
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  requestCount: number;
}

/** UsageSummary is the parsed response of a usage summary endpoint.
 *
 * UsageSummary 是某个用量汇总端点的解析结果。 */
export interface UsageSummary {
  items: UsageSummaryEntry[];
}

function fail(): never {
  throw new ApiError("contract");
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    fail();
  }
  return value as Record<string, unknown>;
}

function required(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (typeof value !== "string" || value === "") {
    fail();
  }
  return value;
}

/** optionalText reads an `omitempty` string field. Absent becomes "" —
 * matching Go's own zero value, which is what the field encodes as anyway
 * when it is present but empty.
 *
 * optionalText 读取一个 `omitempty` 的字符串字段。缺席时为 ""——与 Go 自己的
 * 零值一致，反正该字段存在但为空时编码出来也是这样。 */
function optionalText(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (value === undefined) {
    return "";
  }
  if (typeof value !== "string") {
    fail();
  }
  return value;
}

function num(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  if (typeof value !== "number" || !Number.isFinite(value)) {
    fail();
  }
  return value;
}

function list<T>(value: unknown, parse: (item: unknown) => T): T[] {
  if (value === undefined || value === null) {
    return [];
  }
  if (!Array.isArray(value)) {
    fail();
  }
  return value.map(parse);
}

function parseEntry(value: unknown): UsageSummaryEntry {
  const source = record(value);
  return {
    tenantId: optionalText(source, "tenant_id"),
    userId: optionalText(source, "user_id"),
    keyId: optionalText(source, "key_id"),
    model: required(source, "model"),
    promptTokens: num(source, "prompt_tokens"),
    completionTokens: num(source, "completion_tokens"),
    totalTokens: num(source, "total_tokens"),
    requestCount: num(source, "request_count"),
  };
}

/** parseUsageSummary validates a usage summary response.
 *
 * parseUsageSummary 校验一次用量汇总响应。 */
export function parseUsageSummary(value: unknown): UsageSummary {
  const source = record(value);
  return { items: list(source.items, parseEntry) };
}

/** ModelUsage is one model's input and output tokens.
 *
 * ModelUsage 是一个模型的输入与输出 token。 */
export interface ModelUsage {
  model: string;
  promptTokens: number;
  completionTokens: number;
}

/** usageByModel folds a `group_by=key` summary into per-model usage for each
 * id that `pick` returns (a key id or a user id), largest total first.
 * Unattributed rows (an empty id) are left out.
 *
 * usageByModel 把一份 `group_by=key` 汇总折叠成每个 id（由 `pick` 取出的 key id 或
 * user id）各模型的用量，按总量从大到小排列。未归属的行（id 为空）不计入。 */
export function usageByModel(items: UsageSummaryEntry[], pick: (item: UsageSummaryEntry) => string): Map<string, ModelUsage[]> {
  const totals = new Map<string, Map<string, ModelUsage>>();
  for (const item of items) {
    const id = pick(item);
    if (!id) continue;
    let models = totals.get(id);
    if (!models) {
      models = new Map();
      totals.set(id, models);
    }
    const entry = models.get(item.model) ?? { model: item.model, promptTokens: 0, completionTokens: 0 };
    entry.promptTokens += item.promptTokens;
    entry.completionTokens += item.completionTokens;
    models.set(item.model, entry);
  }
  const out = new Map<string, ModelUsage[]>();
  for (const [id, models] of totals) {
    out.set(id, [...models.values()].sort((a, b) => b.promptTokens + b.completionTokens - a.promptTokens - a.completionTokens));
  }
  return out;
}

import { addCost, type UsageSummaryEntry } from "./usage.ts";

/**
 * The operator usage page reads one cross-tenant summary — a row per (tenant,
 * model) pair, summed by the control plane — and folds it two ways: by tenant
 * and by model. Folding is only addition over rows the server already grouped,
 * so narrowing to one tenant here gives the same figures as asking the server
 * with `tenant_id`, while the tenant picker can still list every tenant that
 * used something in the window.
 *
 * 运维用量页读取一份跨租户汇总（控制面按（租户, 模型）分组求和，每对一行），再按租户与
 * 按模型两种方式折叠。折叠只是对服务端已分组的行做加法，因此在这里收窄到单个租户，与
 * 带 `tenant_id` 询问服务端得到的数字相同，同时租户选择器仍能列出窗口内有用量的全部租户。
 */

/** UsageTotals sums the token, request and credit fields of some rows.
 * UsageTotals 是若干行的 token、请求与点数字段之和。 */
export interface UsageTotals {
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  requestCount: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  /** null while every row came without a cost, i.e. billing is off. / 所有行都不带费用（即计费关闭）时为 null。 */
  costMicro: number | null;
}

/** usageTotals adds up rows.
 * usageTotals 对各行求和。 */
export function usageTotals(items: readonly UsageSummaryEntry[]): UsageTotals {
  return items.reduce<UsageTotals>(
    (acc, item) => ({
      promptTokens: acc.promptTokens + item.promptTokens,
      completionTokens: acc.completionTokens + item.completionTokens,
      totalTokens: acc.totalTokens + item.totalTokens,
      requestCount: acc.requestCount + item.requestCount,
      cacheReadTokens: acc.cacheReadTokens + item.cacheReadTokens,
      cacheWriteTokens: acc.cacheWriteTokens + item.cacheWriteTokens,
      costMicro: addCost(acc.costMicro, item.costMicro),
    }),
    { promptTokens: 0, completionTokens: 0, totalTokens: 0, requestCount: 0, cacheReadTokens: 0, cacheWriteTokens: 0, costMicro: null },
  );
}

/** UsageGroup is one tenant's or one model's share of a summary.
 * UsageGroup 是汇总中一个租户或一个模型所占的部分。 */
export interface UsageGroup {
  id: string;
  totals: UsageTotals;
  /** share is this group's fraction of all tokens, 0 when nothing was used. / share 是该组占全部 token 的比例，总量为零时为 0。 */
  share: number;
  /** breadth counts the other dimension: models for a tenant, tenants for a model. / breadth 统计另一维度：租户的模型数、模型的租户数。 */
  breadth: number;
}

/** groupUsage folds rows by tenant or by model, largest total first, ties by id.
 * groupUsage 按租户或按模型折叠各行，总量大的在前，相同时按 id 排序。 */
export function groupUsage(items: readonly UsageSummaryEntry[], by: "tenant" | "model"): UsageGroup[] {
  const rows = new Map<string, UsageSummaryEntry[]>();
  for (const item of items) {
    const id = by === "tenant" ? item.tenantId : item.model;
    rows.set(id, [...(rows.get(id) ?? []), item]);
  }
  const overall = usageTotals(items).totalTokens;
  return [...rows.entries()]
    .map(([id, group]) => {
      const totals = usageTotals(group);
      return {
        id,
        totals,
        share: overall > 0 ? totals.totalTokens / overall : 0,
        breadth: new Set(group.map((item) => (by === "tenant" ? item.model : item.tenantId))).size,
      };
    })
    .sort((a, b) => b.totals.totalTokens - a.totals.totalTokens || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}

/** UsageRange names the time window presets.
 * UsageRange 命名时间窗口预设。 */
export type UsageRange = "all" | "7d" | "30d" | "custom";

/** rangeSince is the `since` a preset asks for at `now`; "" means unbounded.
 * Custom windows are entered by hand and have no preset start.
 * rangeSince 是某个预设在 `now` 时刻对应的 `since`；"" 表示不设下界。自定义窗口
 * 手动输入，没有预设起点。 */
export function rangeSince(range: Exclude<UsageRange, "custom">, now: number = Date.now()): string {
  if (range === "all") return "";
  return new Date(now - (range === "7d" ? 7 : 30) * 86_400_000).toISOString();
}

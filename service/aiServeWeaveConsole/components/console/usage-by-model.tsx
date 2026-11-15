"use client";

import * as React from "react";

import { useI18n } from "@/components/i18n/provider";
import { useResource } from "@/components/console/use-resource";
import { parseUsageSummary, usageByModel, type ModelUsage, type UsageSummary } from "@/lib/console/usage";

/** useUsageByKey reads the tenant's all-time usage grouped by key and returns
 * per-model usage for each key and each user, or null until it has loaded or
 * when it fails (the lists still render; only the usage column stays empty).
 *
 * useUsageByKey 读取本租户按 key 分组的全部用量，返回每个 key、每个用户各模型的用量；
 * 尚未载入或读取失败时为 null（列表照常显示，只有用量列留空）。 */
export function useUsageByKey(): { byKey: Map<string, ModelUsage[]>; byUser: Map<string, ModelUsage[]> } | null {
  const summary = useResource<UsageSummary>({
    method: "GET",
    path: "/admin/v1/usage/summary",
    query: { group_by: "key" },
    parse: parseUsageSummary,
  });
  return React.useMemo(() => {
    if (!summary.data) return null;
    return {
      byKey: usageByModel(summary.data.items, (item) => item.keyId),
      byUser: usageByModel(summary.data.items, (item) => item.userId),
    };
  }, [summary.data]);
}

/** UsageByModelCell lists one row's input and output tokens per model.
 *
 * UsageByModelCell 按模型列出一行的输入与输出 token。 */
export function UsageByModelCell({ usage }: { usage: ModelUsage[] | undefined }) {
  const { locale, t } = useI18n();
  if (!usage || usage.length === 0) {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <ul className="grid gap-0.5 text-xs">
      {usage.map((entry) => (
        <li key={entry.model} className="whitespace-nowrap">
          <code>{entry.model}</code>{" "}
          <span className="text-muted-foreground">{t("tenant.usageInput")}</span> {entry.promptTokens.toLocaleString(locale)}{" · "}
          <span className="text-muted-foreground">{t("tenant.usageOutput")}</span> {entry.completionTokens.toLocaleString(locale)}
        </li>
      ))}
    </ul>
  );
}

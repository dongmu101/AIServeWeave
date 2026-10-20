"use client";
import type { Translator } from "@/lib/i18n/translate";




import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";


import * as React from "react";
import ReactECharts from "echarts-for-react";

import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { parseUsageSummary, type UsageSummary, type UsageSummaryEntry } from "@/lib/console/usage";

/** localToIso turns a `datetime-local` input value into an RFC 3339
 * timestamp, or "" when the field is empty — the shape `GET
 * /admin/v1/usage/summary` requires for `since`/`until`.
 *
 * localToIso 把一个 `datetime-local` 输入值转换成 RFC 3339 时间戳，字段为空时
 * 转换成 ""——这正是 `GET /admin/v1/usage/summary` 的 `since`/`until` 所要求的形状。 */
function localToIso(value: string): string {
  if (!value) {
    return "";
  }
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? "" : parsed.toISOString();
}

function totalsOf(items: UsageSummaryEntry[]) {
  return items.reduce(
    (acc, item) => ({
      promptTokens: acc.promptTokens + item.promptTokens,
      completionTokens: acc.completionTokens + item.completionTokens,
      totalTokens: acc.totalTokens + item.totalTokens,
      requestCount: acc.requestCount + item.requestCount,
    }),
    { promptTokens: 0, completionTokens: 0, totalTokens: 0, requestCount: 0 }
  );
}

function tokensByModelOption(items: UsageSummaryEntry[], t: Translator) {
  const sorted = [...items].sort((a, b) => b.totalTokens - a.totalTokens);
  return {
    tooltip: { trigger: "axis", axisPointer: { type: "shadow" } },
    grid: { left: 140, right: 24, top: 16, bottom: 24 },
    xAxis: { type: "value", name: "token" },
    yAxis: { type: "category", data: sorted.map((item) => item.model), inverse: true },
    series: [{ type: "bar", name: t("tenant.totalTokens"), data: sorted.map((item) => item.totalTokens) }],
  };
}

/**
 * UsageView shows this tenant's token consumption by model over an optional
 * time window.
 *
 * The window is unbounded by default: an empty `since`/`until` is a valid
 * request the backend answers by summing every record, not an error state —
 * so a tenant who has never set a window still sees their full usage rather
 * than an empty page.
 *
 * UsageView 展示本租户按模型统计的 token 消耗，时间窗口可选。
 *
 * 窗口默认不设边界：空的 `since`/`until` 是后端会正常回答（对全部记录求和）的
 * 合法请求，不是一种错误状态——因此从未设置过窗口的租户看到的是完整用量，而不是
 * 一个空页面。
 */
export function UsageView() {
  const {t } = useI18n();
  const [sinceInput, setSinceInput] = React.useState("");
  const [untilInput, setUntilInput] = React.useState("");
  const [since, setSince] = React.useState("");
  const [until, setUntil] = React.useState("");

  const query: Record<string, string> = {};
  if (since) query.since = since;
  if (until) query.until = until;

  const resource = useResource<UsageSummary>({
    method: "GET",
    path: "/admin/v1/usage/summary",
    query,
    parse: parseUsageSummary,
  });

  function applyWindow(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSince(localToIso(sinceInput));
    setUntil(localToIso(untilInput));
  }

  return (
    <div className="grid gap-6">
      <div>
        <h1 className="font-heading text-lg font-semibold"><Message id="tenant.usage" /></h1>
        <p className="text-sm text-muted-foreground">
          <Message id="tenant.tokenConsumptionByModelFromThePersistentUsage" /></p>
      </div>

      <form onSubmit={applyWindow} className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1">
          <label htmlFor="usage-since" className="text-xs text-muted-foreground"><Message id="tenant.startTimeOptional" /></label>
          <Input
            id="usage-since"
            type="datetime-local"
            value={sinceInput}
            onChange={(event) => setSinceInput(event.target.value)}
          />
        </div>
        <div className="grid gap-1">
          <label htmlFor="usage-until" className="text-xs text-muted-foreground"><Message id="tenant.endTimeOptional" /></label>
          <Input
            id="usage-until"
            type="datetime-local"
            value={untilInput}
            onChange={(event) => setUntilInput(event.target.value)}
          />
        </div>
        <Button type="submit" variant="outline"><Message id="tenant.applyTimeRange" /></Button>
        {since || until ? (
          <Button
            type="button"
            variant="ghost"
            onClick={() => {
              setSinceInput("");
              setUntilInput("");
              setSince("");
              setUntil("");
            }}
          >
            <Message id="tenant.clear_7b15e5" /></Button>
        ) : null}
      </form>

      {resource.error ? (
        <ErrorState message={resource.error} onRetry={resource.reload} />
      ) : resource.loading || !resource.data ? (
        <LoadingState label={t("tenant.loadingUsage")} rows={5} />
      ) : resource.data.items.length === 0 ? (
        <EmptyState title={t("tenant.noUsageRecordsInTheSelectedTimeRange")} description={t("tenant.tryADifferentTimeRangeOrCheckWhether")} />
      ) : (
        <UsageSummaryBody items={resource.data.items} />
      )}
    </div>
  );
}

function UsageSummaryBody({ items }: { items: UsageSummaryEntry[] }) {
  const { locale, t } = useI18n();
  const totals = totalsOf(items);
  const sorted = [...items].sort((a, b) => b.totalTokens - a.totalTokens);
  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium"><Message id="tenant.total_92bcbf" /></CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-4">
            <div className="grid gap-1">
              <dt className="text-xs text-muted-foreground">Prompt token</dt>
              <dd className="text-sm">{totals.promptTokens.toLocaleString(locale)}</dd>
            </div>
            <div className="grid gap-1">
              <dt className="text-xs text-muted-foreground">Completion token</dt>
              <dd className="text-sm">{totals.completionTokens.toLocaleString(locale)}</dd>
            </div>
            <div className="grid gap-1">
              <dt className="text-xs text-muted-foreground"><Message id="tenant.totalTokens" /></dt>
              <dd className="text-sm">{totals.totalTokens.toLocaleString(locale)}</dd>
            </div>
            <div className="grid gap-1">
              <dt className="text-xs text-muted-foreground"><Message id="tenant.requests" /></dt>
              <dd className="text-sm">{totals.requestCount.toLocaleString(locale)}</dd>
            </div>
          </dl>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium"><Message id="tenant.totalTokensByModel" /></CardTitle>
        </CardHeader>
        <CardContent>
          <ReactECharts option={tokensByModelOption(items, t)} style={{ height: Math.max(160, sorted.length * 36) }} notMerge />
        </CardContent>
      </Card>

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead><Message id="tenant.models_98fd0c" /></TableHead>
            <TableHead>Prompt token</TableHead>
            <TableHead>Completion token</TableHead>
            <TableHead><Message id="tenant.totalTokens" /></TableHead>
            <TableHead><Message id="tenant.requests" /></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {sorted.map((item) => (
            <TableRow key={item.model}>
              <TableCell><code className="text-xs">{item.model}</code></TableCell>
              <TableCell>{item.promptTokens.toLocaleString(locale)}</TableCell>
              <TableCell>{item.completionTokens.toLocaleString(locale)}</TableCell>
              <TableCell>{item.totalTokens.toLocaleString(locale)}</TableCell>
              <TableCell>{item.requestCount.toLocaleString(locale)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </>
  );
}

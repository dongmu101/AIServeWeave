"use client";
import type { Locale } from "@/lib/i18n/locale";
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
 * timestamp, or "" when the field is empty.
 *
 * localToIso 把一个 `datetime-local` 输入值转换成 RFC 3339 时间戳，字段为空时
 * 转换成 ""。 */
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

/** byModel folds per-(tenant, model) rows down to per-model totals, which is
 * what a fleet-wide chart should show — a chart with one bar per (tenant,
 * model) pair would grow unreadable as tenants are added.
 *
 * byModel 把按 (租户, 模型) 分组的行折叠成按模型的合计，这才是一张机群级别的图表
 * 该展示的东西——按 (租户, 模型) 各画一根柱子的图表，会随着租户增多而变得没法看。 */
function byModel(items: UsageSummaryEntry[]): { model: string; totalTokens: number }[] {
  const totals = new Map<string, number>();
  for (const item of items) {
    totals.set(item.model, (totals.get(item.model) ?? 0) + item.totalTokens);
  }
  return Array.from(totals.entries())
    .map(([model, totalTokens]) => ({ model, totalTokens }))
    .sort((a, b) => b.totalTokens - a.totalTokens);
}

function tokensByModelOption(rows: { model: string; totalTokens: number }[], t: Translator, locale: Locale) {
  return {
    tooltip: { trigger: "axis", axisPointer: { type: "shadow" }, valueFormatter: (value: unknown) => new Intl.NumberFormat(locale).format(Number(value)) },
    grid: { left: 140, right: 24, top: 16, bottom: 24 },
    xAxis: { type: "value", name: "token", axisLabel: { formatter: (value: number) => new Intl.NumberFormat(locale).format(value) } },
    yAxis: { type: "category", data: rows.map((row) => row.model), inverse: true },
    series: [{ type: "bar", name: t("tenant.totalTokens"), data: rows.map((row) => row.totalTokens) }],
  };
}

/**
 * UsageView shows token consumption across tenants and models, for platform
 * operators. An optional `tenant_id` narrows it to one tenant, matching the
 * backend's own filter — the Console does not compute a tenant breakdown
 * client-side from an unfiltered read, since the control plane already does
 * that grouping in the database.
 *
 * UsageView 向平台运维展示跨租户、跨模型的 token 消耗。可选的 `tenant_id` 把它
 * 收窄到单个租户，与后端自己的筛选一致——Console 不会在客户端从一次未筛选的读取里
 * 自行算出按租户的拆分，因为控制面已经在数据库里做了这次分组。
 */
export function UsageView() {
  const { t } = useI18n();
  const [sinceInput, setSinceInput] = React.useState("");
  const [untilInput, setUntilInput] = React.useState("");
  const [tenantInput, setTenantInput] = React.useState("");
  const [since, setSince] = React.useState("");
  const [until, setUntil] = React.useState("");
  const [tenantId, setTenantId] = React.useState("");

  const query: Record<string, string> = {};
  if (since) query.since = since;
  if (until) query.until = until;
  if (tenantId) query.tenant_id = tenantId;

  const resource = useResource<UsageSummary>({
    method: "GET",
    surface: "operator",
    path: "/operator/v1/usage/summary",
    query,
    parse: parseUsageSummary,
  });

  function applyFilters(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSince(localToIso(sinceInput));
    setUntil(localToIso(untilInput));
    setTenantId(tenantInput.trim());
  }

  return (
    <div className="grid gap-6">
      <div>
        <h1 className="font-heading text-lg font-semibold"><Message id="tenant.usage" /></h1>
        <p className="text-sm text-muted-foreground">
          <Message id="operator.tokenConsumptionByModelAcrossTenantsFromThe" /></p>
      </div>

      <form onSubmit={applyFilters} className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1">
          <label htmlFor="usage-tenant" className="text-xs text-muted-foreground"><Message id="operator.tenantIdOptional" /></label>
          <Input
            id="usage-tenant"
            className="w-56"
            placeholder={t("operator.leaveBlankForAllTenants")}
            value={tenantInput}
            onChange={(event) => setTenantInput(event.target.value)}
          />
        </div>
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
        <Button type="submit" variant="outline"><Message id="operator.applyFilters" /></Button>
        {since || until || tenantId ? (
          <Button
            type="button"
            variant="ghost"
            onClick={() => {
              setSinceInput("");
              setUntilInput("");
              setTenantInput("");
              setSince("");
              setUntil("");
              setTenantId("");
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
        <EmptyState title={t("operator.noUsageRecordsMatchTheSelectedCriteria")} description={t("operator.tryADifferentTimeRangeOrTenantId")} />
      ) : (
        <UsageSummaryBody items={resource.data.items} showTenant={!tenantId} />
      )}
    </div>
  );
}

function UsageSummaryBody({ items, showTenant }: { items: UsageSummaryEntry[]; showTenant: boolean }) {
  const { locale, t } = useI18n();
  const totals = totalsOf(items);
  const sorted = [...items].sort((a, b) => b.totalTokens - a.totalTokens);
  const modelTotals = byModel(items);
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
          <ReactECharts option={tokensByModelOption(modelTotals, t, locale)} style={{ height: Math.max(160, modelTotals.length * 36) }} notMerge />
        </CardContent>
      </Card>

      <Table>
        <TableHeader>
          <TableRow>
            {showTenant ? <TableHead><Message id="tenant.tenant" /></TableHead> : null}
            <TableHead><Message id="tenant.models_98fd0c" /></TableHead>
            <TableHead>Prompt token</TableHead>
            <TableHead>Completion token</TableHead>
            <TableHead><Message id="tenant.totalTokens" /></TableHead>
            <TableHead><Message id="tenant.requests" /></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {sorted.map((item) => (
            <TableRow key={`${item.tenantId}:${item.model}`}>
              {showTenant ? <TableCell><code className="text-xs">{item.tenantId}</code></TableCell> : null}
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

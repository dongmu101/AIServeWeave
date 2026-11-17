"use client";
import type { Translator } from "@/lib/i18n/translate";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import * as React from "react";
import ReactECharts from "echarts-for-react";

import { UsageRecords } from "@/app/console/usage/usage-records";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { UsageByModelCell } from "@/components/console/usage-by-model";
import { useResource } from "@/components/console/use-resource";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatCredits } from "@/lib/console/billing";
import { parseApiKeys, parseUsers, type ApiKeySummary, type Page, type User } from "@/lib/console/contract";
import { addCost, billingShown, parseUsageSummary, usageByModel, type UsageSummary, type UsageSummaryEntry } from "@/lib/console/usage";
import { isoToLocalInput, type UsageLink } from "@/lib/console/usage-records";
import { CHART_COLORS } from "@/lib/console/chart-colors";

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

type Totals = {
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  requestCount: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  costMicro: number | null;
};

function totalsOf(items: UsageSummaryEntry[]): Totals {
  return items.reduce<Totals>(
    (acc, item) => ({
      promptTokens: acc.promptTokens + item.promptTokens,
      completionTokens: acc.completionTokens + item.completionTokens,
      totalTokens: acc.totalTokens + item.totalTokens,
      requestCount: acc.requestCount + item.requestCount,
      cacheReadTokens: acc.cacheReadTokens + item.cacheReadTokens,
      cacheWriteTokens: acc.cacheWriteTokens + item.cacheWriteTokens,
      costMicro: addCost(acc.costMicro, item.costMicro),
    }),
    { promptTokens: 0, completionTokens: 0, totalTokens: 0, requestCount: 0, cacheReadTokens: 0, cacheWriteTokens: 0, costMicro: null }
  );
}

/** groupTotals sums rows per id (a user id or a key id), largest total first.
 * An empty id is kept as its own group: unattributed usage.
 *
 * groupTotals 按 id（用户 id 或 key id）汇总各行，按总量从大到小排列。空 id 单独成组，
 * 即未归属的用量。 */
function groupTotals(items: UsageSummaryEntry[], pick: (item: UsageSummaryEntry) => string) {
  const groups = new Map<string, UsageSummaryEntry[]>();
  for (const item of items) {
    const id = pick(item);
    groups.set(id, [...(groups.get(id) ?? []), item]);
  }
  return [...groups.entries()]
    .map(([id, rows]) => ({ id, rows, totals: totalsOf(rows) }))
    .sort((a, b) => b.totals.totalTokens - a.totals.totalTokens);
}

function tokensByModelOption(items: { model: string; promptTokens: number; completionTokens: number; totalTokens: number }[], t: Translator, locale: string) {
  const compact = new Intl.NumberFormat(locale, { notation: "compact" });
  const sorted = [...items].sort((a, b) => b.totalTokens - a.totalTokens);
  return {
    color: CHART_COLORS,
    tooltip: { trigger: "axis", axisPointer: { type: "shadow" } },
    legend: { bottom: 0 },
    grid: { left: 8, right: 24, top: 8, bottom: 32, containLabel: true },
    xAxis: { type: "value", splitNumber: 3, axisLabel: { formatter: (value: number) => compact.format(value) } },
    yAxis: { type: "category", data: sorted.map((item) => item.model), inverse: true },
    series: [
      { type: "bar", stack: "tokens", name: t("tenant.inputTokens"), data: sorted.map((item) => item.promptTokens) },
      { type: "bar", stack: "tokens", name: t("tenant.outputTokens"), data: sorted.map((item) => item.completionTokens) },
    ],
  };
}

type Range = "all" | "7d" | "30d" | "custom";
type UsageTab = UsageLink["tab"];

/** daysAgoIso is the instant `days` days before now, for a preset range.
 * daysAgoIso 是距今 `days` 天前的时刻，用于预设时间范围。 */
function daysAgoIso(days: number): string {
  return new Date(Date.now() - days * 86_400_000).toISOString();
}

/**
 * UsageView shows this tenant's token consumption over an optional time
 * window: totals, then by model, by user and by API key, and every request
 * one by one. With billing on it also shows the credits used.
 *
 * The window is unbounded by default: an empty `since`/`until` is a valid
 * request the backend answers by summing every record, not an error state —
 * so a tenant who has never set a window still sees their full usage rather
 * than an empty page. A link (from a usage ledger entry) can open a tab with
 * a custom window already set.
 *
 * UsageView 展示本租户在可选时间窗口内的 token 消耗：先是合计，再按模型、用户与
 * API Key 分别展示，以及逐笔请求。开启计费时另显示消耗的点数。
 *
 * 窗口默认不设边界：空的 `since`/`until` 是后端会正常回答（对全部记录求和）的
 * 合法请求，不是一种错误状态——因此从未设置过窗口的租户看到的是完整用量，而不是
 * 一个空页面。链接（来自一条用量账目）可以直接打开某个标签页并带上自定义窗口。
 */
export function UsageView({ link }: { link: UsageLink }) {
  const { t } = useI18n();
  const linked = link.since !== "" || link.until !== "";
  const [range, setRange] = React.useState<Range>(linked ? "custom" : "all");
  const [sinceInput, setSinceInput] = React.useState(() => isoToLocalInput(link.since));
  const [untilInput, setUntilInput] = React.useState(() => isoToLocalInput(link.until));
  const [since, setSince] = React.useState(link.since);
  const [until, setUntil] = React.useState(link.until);

  const query: Record<string, string> = {};
  if (since) query.since = since;
  if (until) query.until = until;

  const resource = useResource<UsageSummary>({
    method: "GET",
    path: "/admin/v1/usage/summary",
    query: { ...query, group_by: "key" },
    parse: parseUsageSummary,
  });

  function pickRange(next: Range) {
    setRange(next);
    if (next === "custom") return;
    const days = next === "7d" ? 7 : next === "30d" ? 30 : 0;
    setSince(days ? daysAgoIso(days) : "");
    setUntil("");
  }

  function applyWindow(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSince(localToIso(sinceInput));
    setUntil(localToIso(untilInput));
  }

  const ranges: { value: Range; label: string }[] = [
    { value: "all", label: t("tenant.rangeAll") },
    { value: "7d", label: t("tenant.rangeLast7Days") },
    { value: "30d", label: t("tenant.rangeLast30Days") },
    { value: "custom", label: t("tenant.rangeCustom") },
  ];

  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight"><Message id="tenant.usage" /></h1>
          <p className="text-sm text-muted-foreground">
            <Message id="tenant.tokenConsumptionByModelFromThePersistentUsage" /></p>
        </div>
        <div className="flex flex-wrap gap-1" role="group" aria-label={t("tenant.applyTimeRange")}>
          {ranges.map((option) => (
            <Button
              key={option.value}
              type="button"
              size="sm"
              variant={range === option.value ? "default" : "outline"}
              aria-pressed={range === option.value}
              onClick={() => pickRange(option.value)}
            >
              {option.label}
            </Button>
          ))}
        </div>
      </div>

      {range === "custom" ? (
        <form onSubmit={applyWindow} className="flex flex-wrap items-end gap-3">
          <div className="grid gap-1">
            <label htmlFor="usage-since" className="text-xs text-muted-foreground"><Message id="tenant.startTimeOptional" /></label>
            <Input id="usage-since" type="datetime-local" value={sinceInput} onChange={(event) => setSinceInput(event.target.value)} />
          </div>
          <div className="grid gap-1">
            <label htmlFor="usage-until" className="text-xs text-muted-foreground"><Message id="tenant.endTimeOptional" /></label>
            <Input id="usage-until" type="datetime-local" value={untilInput} onChange={(event) => setUntilInput(event.target.value)} />
          </div>
          <Button type="submit" size="sm"><Message id="tenant.applyTimeRange" /></Button>
        </form>
      ) : null}

      {resource.error ? (
        <ErrorState message={resource.error} onRetry={resource.reload} />
      ) : resource.loading || !resource.data ? (
        <LoadingState label={t("tenant.loadingUsage")} rows={5} />
      ) : resource.data.items.length === 0 ? (
        <EmptyState title={t("tenant.noUsageRecordsInTheSelectedTimeRange")} description={t("tenant.tryADifferentTimeRangeOrCheckWhether")} />
      ) : (
        <UsageBody items={resource.data.items} since={since} until={until} initialTab={link.tab} />
      )}
    </div>
  );
}

function UsageBody({ items, since, until, initialTab }: { items: UsageSummaryEntry[]; since: string; until: string; initialTab: UsageTab }) {
  const { locale, t } = useI18n();
  const [tab, setTab] = React.useState<UsageTab>(initialTab);
  const users = useResource<Page<User>>({ method: "GET", path: "/admin/v1/users", query: { limit: "200" }, parse: parseUsers });
  const keys = useResource<Page<ApiKeySummary>>({ method: "GET", path: "/admin/v1/apikeys", query: { limit: "200" }, parse: parseApiKeys });
  const totals = totalsOf(items);
  const billing = billingShown(items);
  const tiles = [
    { label: t("tenant.inputTokens"), value: totals.promptTokens.toLocaleString(locale) },
    { label: t("tenant.outputTokens"), value: totals.completionTokens.toLocaleString(locale) },
    { label: t("tenant.totalTokens"), value: totals.totalTokens.toLocaleString(locale) },
    { label: t("tenant.requests"), value: totals.requestCount.toLocaleString(locale) },
    ...(billing ? [{ label: t("tenant.usageCost"), value: formatCredits(totals.costMicro ?? 0, locale), note: t("tenant.usageCreditsNote") }] : []),
  ];
  const models = [...new Set(items.map((item) => item.model))].sort();
  return (
    <>
      <div className={`grid grid-cols-2 gap-4 ${billing ? "lg:grid-cols-5" : "lg:grid-cols-4"}`}>
        {tiles.map((tile) => (
          <Card key={tile.label}>
            <CardContent className="grid gap-1">
              <span className="text-xs text-muted-foreground">{tile.label}</span>
              <span className="text-2xl font-semibold tabular-nums break-all">{tile.value}</span>
              {"note" in tile && tile.note ? <span className="text-xs text-muted-foreground">{tile.note}</span> : null}
            </CardContent>
          </Card>
        ))}
      </div>

      <Tabs value={tab} onValueChange={(next) => { if (next === "model" || next === "user" || next === "key" || next === "records") setTab(next); }}>
        <TabsList className="h-auto! w-full flex-wrap justify-start sm:w-fit">
          <TabsTrigger value="model" className="flex-none px-3"><Message id="tenant.byModel" /></TabsTrigger>
          <TabsTrigger value="user" className="flex-none px-3"><Message id="tenant.byUser" /></TabsTrigger>
          <TabsTrigger value="key" className="flex-none px-3"><Message id="tenant.byKey" /></TabsTrigger>
          <TabsTrigger value="records" className="flex-none px-3"><Message id="tenant.usageRecords" /></TabsTrigger>
        </TabsList>
        <TabsContent value="model" className="pt-2">
          <ByModel items={items} billing={billing} />
        </TabsContent>
        <TabsContent value="user" className="pt-2">
          <ByUserOrKey items={items} dimension="user" billing={billing} users={users.data?.items ?? []} keys={keys.data?.items ?? []} />
        </TabsContent>
        <TabsContent value="key" className="pt-2">
          <ByUserOrKey items={items} dimension="key" billing={billing} users={users.data?.items ?? []} keys={keys.data?.items ?? []} />
        </TabsContent>
        <TabsContent value="records" className="pt-2">
          <UsageRecords since={since} until={until} models={models} keys={keys.data?.items ?? []} />
        </TabsContent>
      </Tabs>
    </>
  );
}

function TotalsCells({ totals, billing }: { totals: Totals; billing: boolean }) {
  const { locale } = useI18n();
  return (
    <>
      <TableCell className="text-right tabular-nums">{totals.promptTokens.toLocaleString(locale)}</TableCell>
      <TableCell className="text-right tabular-nums">{totals.cacheReadTokens.toLocaleString(locale)}</TableCell>
      <TableCell className="text-right tabular-nums">{totals.cacheWriteTokens.toLocaleString(locale)}</TableCell>
      <TableCell className="text-right tabular-nums">{totals.completionTokens.toLocaleString(locale)}</TableCell>
      <TableCell className="text-right tabular-nums">{totals.totalTokens.toLocaleString(locale)}</TableCell>
      <TableCell className="text-right tabular-nums">{totals.requestCount.toLocaleString(locale)}</TableCell>
      {billing ? <TableCell className="text-right font-mono whitespace-nowrap">{formatCredits(totals.costMicro ?? 0, locale)}</TableCell> : null}
    </>
  );
}

function TotalsHeads({ billing }: { billing: boolean }) {
  return (
    <>
      <TableHead className="text-right"><Message id="tenant.inputTokens" /></TableHead>
      <TableHead className="text-right"><Message id="tenant.usageCacheRead" /></TableHead>
      <TableHead className="text-right"><Message id="tenant.usageCacheWrite" /></TableHead>
      <TableHead className="text-right"><Message id="tenant.outputTokens" /></TableHead>
      <TableHead className="text-right"><Message id="tenant.totalTokens" /></TableHead>
      <TableHead className="text-right"><Message id="tenant.requests" /></TableHead>
      {billing ? <TableHead className="text-right"><Message id="tenant.usageCost" /></TableHead> : null}
    </>
  );
}

function ByModel({ items, billing }: { items: UsageSummaryEntry[]; billing: boolean }) {
  const { locale, t } = useI18n();
  const models = groupTotals(items, (item) => item.model);
  return (
    <div className="grid gap-4 xl:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium"><Message id="tenant.totalTokensByModel" /></CardTitle>
        </CardHeader>
        <CardContent>
          <ReactECharts
            option={tokensByModelOption(models.map((m) => ({ ...m.totals, model: m.id })), t, locale)}
            style={{ height: Math.max(200, models.length * 40 + 60) }}
            notMerge
          />
        </CardContent>
      </Card>
      <Card>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead><Message id="tenant.models_98fd0c" /></TableHead>
                <TotalsHeads billing={billing} />
              </TableRow>
            </TableHeader>
            <TableBody>
              {models.map((model) => (
                <TableRow key={model.id}>
                  <TableCell><code className="text-xs">{model.id}</code></TableCell>
                  <TotalsCells totals={model.totals} billing={billing} />
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  );
}

/** ByUserOrKey lists usage per user or per key, with each row's per-model
 * breakdown. Names come from the first page of users and keys; an id not found
 * there (a deleted key, or beyond that page) is shown as the id itself.
 *
 * ByUserOrKey 按用户或按 key 列出用量，每行附各模型明细。名称取自用户与 key 列表的
 * 第一页；找不到的 id（已删除的 key，或不在该页内）直接显示 id。 */
function ByUserOrKey({ items, dimension, billing, users, keys }: {
  items: UsageSummaryEntry[];
  dimension: "user" | "key";
  billing: boolean;
  users: User[];
  keys: ApiKeySummary[];
}) {
  const { t } = useI18n();
  const pick = dimension === "user" ? (item: UsageSummaryEntry) => item.userId : (item: UsageSummaryEntry) => item.keyId;
  const groups = groupTotals(items, pick);
  const userLabel = (id: string) => {
    if (!id) return t("tenant.unattributed");
    const user = users.find((u) => u.id === id);
    return user ? user.name || user.email : id;
  };
  const keyLabel = (id: string) => {
    if (!id) return t("tenant.unattributed");
    const key = keys.find((k) => k.id === id);
    return key ? `${key.name} (${key.display})` : id;
  };
  return (
    <Card>
      <CardContent className="grid gap-3">
        <p className="text-xs text-muted-foreground"><Message id="tenant.usageByUserAndKeyHint" /></p>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{dimension === "user" ? <Message id="tenant.users" /> : "API Key"}</TableHead>
              {dimension === "key" ? <TableHead><Message id="tenant.users" /></TableHead> : null}
              <TableHead><Message id="tenant.tokenUsage" /></TableHead>
              <TotalsHeads billing={billing} />
            </TableRow>
          </TableHeader>
          <TableBody>
            {groups.map((group) => (
              <TableRow key={group.id || "-"} className="align-top">
                <NameCell label={dimension === "user" ? userLabel(group.id) : keyLabel(group.id)} muted={!group.id} />
                {dimension === "key" ? <NameCell label={userLabel(group.rows[0]?.userId ?? "")} muted={!group.rows[0]?.userId} /> : null}
                <TableCell>
                  <UsageByModelCell usage={usageByModel(group.rows, () => "all").get("all")} />
                </TableCell>
                <TotalsCells totals={group.totals} billing={billing} />
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

/** NameCell truncates a user or key name so one long name cannot widen the
 * table; the full name stays in the tooltip.
 *
 * NameCell 截断过长的用户或 key 名称，避免一个长名称撑宽整张表；完整名称保留在提示中。 */
function NameCell({ label, muted }: { label: string; muted: boolean }) {
  return (
    <TableCell className={muted ? "text-muted-foreground" : undefined}>
      <span className="block max-w-48 truncate" title={label}>{label}</span>
    </TableCell>
  );
}

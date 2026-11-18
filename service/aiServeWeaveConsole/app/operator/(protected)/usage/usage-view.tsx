"use client";

import * as React from "react";
import { XIcon } from "lucide-react";
import { cn } from "cn";

import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableFooter, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatCredits } from "@/lib/console/billing";
import { parseTenants } from "@/lib/console/contract";
import { groupUsage, rangeSince, usageTotals, type UsageGroup, type UsageRange, type UsageTotals } from "@/lib/console/operator-usage";
import { billingShown, parseUsageSummary, type UsageSummary } from "@/lib/console/usage";
import type { Locale } from "@/lib/i18n/locale";

const ALL_TENANTS = "__all__";

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

/**
 * UsageView shows token consumption across tenants and models for platform
 * operators: headline totals, then one table by tenant and one by model.
 * Picking a tenant narrows every figure to it. The summary is read once per
 * time window without `tenant_id` and narrowed here, which gives the same
 * figures as the server's own filter (see lib/console/operator-usage.ts) while
 * letting the picker list every tenant with usage in the window. Tenant names
 * come from the first page of the tenant list; a tenant beyond it is shown by ID.
 *
 * UsageView 向平台运维展示跨租户、跨模型的 token 消耗：先是合计，再分别按租户与按模型
 * 列表。选中某个租户后，全部数字收窄到该租户。每个时间窗口只读取一次不带 `tenant_id`
 * 的汇总，在这里收窄，数字与服务端自己的筛选相同（见 lib/console/operator-usage.ts），
 * 同时选择器能列出窗口内有用量的全部租户。租户名称取自租户列表第一页，不在其中的租户
 * 显示其 ID。
 */
export function UsageView() {
  const { t } = useI18n();
  const [range, setRange] = React.useState<UsageRange>("all");
  const [sinceInput, setSinceInput] = React.useState("");
  const [untilInput, setUntilInput] = React.useState("");
  const [since, setSince] = React.useState("");
  const [until, setUntil] = React.useState("");
  // The tenant and tab survive a change of window, which remounts the body.
  // 租户与标签页在切换时间窗口（会重新挂载主体）后保留。
  const [tenantId, setTenantId] = React.useState("");
  const [tab, setTab] = React.useState<"tenant" | "model">("tenant");
  const tenants = useResource({ method: "GET", surface: "operator", path: "/operator/v1/tenants", query: { limit: "200" }, parse: parseTenants });
  const names = React.useMemo(() => new Map(tenants.data?.items.map((tenant) => [tenant.id, tenant.name])), [tenants.data]);

  const query: Record<string, string> = {};
  if (since) query.since = since;
  if (until) query.until = until;

  const summary = useResource<UsageSummary>({
    method: "GET",
    surface: "operator",
    path: "/operator/v1/usage/summary",
    query,
    parse: parseUsageSummary,
  });

  function pickRange(next: UsageRange) {
    setRange(next);
    if (next === "custom") return;
    setSince(rangeSince(next));
    setUntil("");
  }

  function applyWindow(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSince(localToIso(sinceInput));
    setUntil(localToIso(untilInput));
  }

  const ranges: { value: UsageRange; label: string }[] = [
    { value: "all", label: t("tenant.rangeAll") },
    { value: "7d", label: t("tenant.rangeLast7Days") },
    { value: "30d", label: t("tenant.rangeLast30Days") },
    { value: "custom", label: t("tenant.rangeCustom") },
  ];

  return (
    <div className="grid gap-6">
      <header className="flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
        <div className="grid min-w-0 flex-1 basis-80 gap-1">
          <h1 className="font-heading text-xl font-semibold tracking-tight"><Message id="tenant.usage" /></h1>
          <p className="max-w-3xl text-sm text-muted-foreground"><Message id="operator.tokenConsumptionByModelAcrossTenantsFromThe" /></p>
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
      </header>

      {range === "custom" ? (
        <form onSubmit={applyWindow} className="flex flex-wrap items-end gap-3 rounded-xl border bg-card p-3 shadow-xs">
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

      {summary.error ? (
        <ErrorState message={summary.error} onRetry={summary.reload} />
      ) : summary.loading || !summary.data ? (
        <LoadingState label={t("tenant.loadingUsage")} rows={5} />
      ) : summary.data.items.length === 0 ? (
        <EmptyState title={t("operator.noUsageRecordsMatchTheSelectedCriteria")} description={t("operator.tryADifferentTimeRangeOrTenantId")} />
      ) : (
        <UsageBody summary={summary.data} names={names} tenantId={tenantId} onTenant={setTenantId} tab={tab} onTab={setTab} />
      )}
    </div>
  );
}

/** UsageBody narrows the summary to the picked tenant and lays out the tiles
 * and the two tables.
 * UsageBody 把汇总收窄到所选租户，并排布统计卡片与两张表。 */
function UsageBody({ summary, names, tenantId, onTenant: setTenantId, tab, onTab: setTab }: {
  summary: UsageSummary;
  names: ReadonlyMap<string, string>;
  tenantId: string;
  onTenant: (id: string) => void;
  tab: "tenant" | "model";
  onTab: (tab: "tenant" | "model") => void;
}) {
  const { locale, t } = useI18n();
  const tenantGroups = React.useMemo(() => groupUsage(summary.items, "tenant"), [summary]);
  const scoped = React.useMemo(() => (tenantId ? summary.items.filter((item) => item.tenantId === tenantId) : summary.items), [summary, tenantId]);
  const modelGroups = React.useMemo(() => groupUsage(scoped, "model"), [scoped]);
  const totals = usageTotals(scoped);
  const billing = billingShown(summary.items);
  const shownTab = tenantId ? "model" : tab;
  const tenantLabel = (id: string) => (id ? names.get(id) || id : t("tenant.unattributed"));
  const options = tenantId && !tenantGroups.some((group) => group.id === tenantId) ? [tenantId, ...tenantGroups.map((group) => group.id)] : tenantGroups.map((group) => group.id);

  return (
    <>
      <div className={cn("grid grid-cols-2 gap-3", billing ? "lg:grid-cols-5" : "lg:grid-cols-4")}>
        <Tile
          className="col-span-2 lg:col-span-1"
          label={t("tenant.totalTokens")}
          value={totals.totalTokens.toLocaleString(locale)}
          notes={[
            [tenantId ? null : t("operator.usageTenantCount", { count: tenantGroups.length }), t("tenant.models_4678e8", { count: modelGroups.length })].filter(Boolean).join(" · "),
          ]}
        />
        <Tile
          label={t("tenant.inputTokens")}
          value={totals.promptTokens.toLocaleString(locale)}
          notes={[
            `${t("tenant.usageCacheRead")} ${totals.cacheReadTokens.toLocaleString(locale)}`,
            `${t("tenant.usageCacheWrite")} ${totals.cacheWriteTokens.toLocaleString(locale)}`,
          ]}
        />
        <Tile label={t("tenant.outputTokens")} value={totals.completionTokens.toLocaleString(locale)} />
        <Tile label={t("tenant.requests")} value={totals.requestCount.toLocaleString(locale)} />
        {billing ? <Tile label={t("tenant.usageCost")} value={formatCredits(totals.costMicro ?? 0, locale)} notes={[t("tenant.usageCreditsNote")]} /> : null}
      </div>

      <Tabs value={shownTab} onValueChange={(next) => { if (next === "tenant" || next === "model") setTab(next); }} className="gap-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <TabsList className="h-auto! flex-wrap justify-start">
            {tenantId ? null : (
              <TabsTrigger value="tenant" className="flex-none gap-1.5 px-3">
                <Message id="operator.usageByTenant" />
                <span className="text-xs text-muted-foreground tabular-nums">{tenantGroups.length}</span>
              </TabsTrigger>
            )}
            <TabsTrigger value="model" className="flex-none gap-1.5 px-3">
              <Message id="tenant.byModel" />
              <span className="text-xs text-muted-foreground tabular-nums">{modelGroups.length}</span>
            </TabsTrigger>
          </TabsList>
          <div className="flex w-full items-center gap-1.5 sm:w-auto">
            <Select value={tenantId || ALL_TENANTS} onValueChange={(next) => { if (next !== null) setTenantId(next === ALL_TENANTS ? "" : next); }}>
              <SelectTrigger className="w-full min-w-0 sm:w-72" aria-label={t("tenant.tenant")}>
                <SelectValue>{tenantId ? tenantLabel(tenantId) : t("operator.usageAllTenants")}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL_TENANTS}><Message id="operator.usageAllTenants" /></SelectItem>
                {options.map((id) => (
                  <SelectItem key={id} value={id}>
                    <span className="grid min-w-0">
                      <span className="truncate">{tenantLabel(id)}</span>
                      {names.get(id) ? <span className="truncate font-mono text-xs text-muted-foreground">{id}</span> : null}
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {tenantId ? (
              <Button type="button" variant="ghost" size="icon-sm" aria-label={t("tenant.clear_7b15e5")} onClick={() => setTenantId("")}><XIcon /></Button>
            ) : null}
          </div>
        </div>

        {tenantId ? null : (
          <TabsContent value="tenant" className="grid gap-2">
            <p className="text-xs text-muted-foreground"><Message id="operator.usageDrillHint" /></p>
            <UsageTable
              heading={t("tenant.tenant")}
              groups={tenantGroups}
              totals={usageTotals(summary.items)}
              billing={billing}
              locale={locale}
              name={(group) => (
                <button
                  type="button"
                  disabled={!group.id}
                  onClick={() => setTenantId(group.id)}
                  className="grid max-w-64 min-w-36 rounded-sm text-left outline-none focus-visible:ring-3 focus-visible:ring-ring/50 enabled:hover:[&>span:first-child]:underline"
                >
                  <span className="truncate font-medium underline-offset-4" title={tenantLabel(group.id)}>{tenantLabel(group.id)}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {names.get(group.id) ? <span className="font-mono">{group.id} · </span> : null}
                    {t("tenant.models_4678e8", { count: group.breadth })}
                  </span>
                </button>
              )}
            />
          </TabsContent>
        )}
        <TabsContent value="model">
          <UsageTable
            heading={t("tenant.models_98fd0c")}
            groups={modelGroups}
            totals={totals}
            billing={billing}
            locale={locale}
            name={(group) => (
              <span className="grid max-w-64 min-w-36">
                <code className="truncate font-mono text-xs font-medium" title={group.id}>{group.id}</code>
                {tenantId ? null : <span className="truncate text-xs text-muted-foreground">{t("operator.usageTenantCount", { count: group.breadth })}</span>}
              </span>
            )}
          />
          {modelGroups.length === 0 ? (
            <EmptyState
              title={t("operator.noUsageRecordsMatchTheSelectedCriteria")}
              description={t("operator.tryADifferentTimeRangeOrTenantId")}
              action={<Button size="sm" variant="outline" onClick={() => setTenantId("")}><Message id="operator.usageAllTenants" /></Button>}
            />
          ) : null}
        </TabsContent>
      </Tabs>
    </>
  );
}

function Tile({ label, value, notes = [], className }: { label: string; value: string; notes?: string[]; className?: string }) {
  return (
    <Card size="sm" className={cn("gap-1 px-4", className)}>
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="font-heading text-2xl font-semibold tabular-nums break-all">{value}</span>
      {notes.filter(Boolean).map((note) => <span key={note} className="text-xs text-muted-foreground tabular-nums">{note}</span>)}
    </Card>
  );
}

/** UsageTable lists groups largest first with their share of all tokens, and
 * ends with the totals of what it lists.
 * UsageTable 按总量从大到小列出各组及其占全部 token 的比例，末行为所列内容的合计。 */
function UsageTable({ heading, groups, totals, billing, locale, name }: {
  heading: string;
  groups: UsageGroup[];
  totals: UsageTotals;
  billing: boolean;
  locale: Locale;
  name: (group: UsageGroup) => React.ReactNode;
}) {
  const { t } = useI18n();
  if (groups.length === 0) return null;
  return (
    <Card className="gap-0 py-0">
      <Table>
        <TableHeader className="bg-muted/40">
          <TableRow>
            <TableHead className="pl-4">{heading}</TableHead>
            <TableHead><Message id="operator.usageShare" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.inputTokens" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.usageCacheRead" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.usageCacheWrite" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.outputTokens" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.totalTokens" /></TableHead>
            <TableHead className={cn("text-right", billing ? undefined : "pr-4")}><Message id="tenant.requests" /></TableHead>
            {billing ? <TableHead className="pr-4 text-right"><Message id="tenant.usageCost" /></TableHead> : null}
          </TableRow>
        </TableHeader>
        <TableBody>
          {groups.map((group) => (
            <TableRow key={group.id || "-"}>
              <TableCell className="py-2.5 pl-4">{name(group)}</TableCell>
              <TableCell><ShareBar share={group.share} locale={locale} /></TableCell>
              <NumberCells totals={group.totals} billing={billing} locale={locale} />
            </TableRow>
          ))}
        </TableBody>
        {groups.length > 1 ? (
          <TableFooter>
            <TableRow>
              <TableCell className="pl-4 font-medium">{t("tenant.total_92bcbf")}</TableCell>
              <TableCell />
              <NumberCells totals={totals} billing={billing} locale={locale} />
            </TableRow>
          </TableFooter>
        ) : null}
      </Table>
    </Card>
  );
}

function NumberCells({ totals, billing, locale }: { totals: UsageTotals; billing: boolean; locale: Locale }) {
  return (
    <>
      <TableCell className="text-right tabular-nums">{totals.promptTokens.toLocaleString(locale)}</TableCell>
      <TableCell className="text-right text-muted-foreground tabular-nums">{totals.cacheReadTokens.toLocaleString(locale)}</TableCell>
      <TableCell className="text-right text-muted-foreground tabular-nums">{totals.cacheWriteTokens.toLocaleString(locale)}</TableCell>
      <TableCell className="text-right tabular-nums">{totals.completionTokens.toLocaleString(locale)}</TableCell>
      <TableCell className="text-right font-medium tabular-nums">{totals.totalTokens.toLocaleString(locale)}</TableCell>
      <TableCell className={cn("text-right tabular-nums", billing ? undefined : "pr-4")}>{totals.requestCount.toLocaleString(locale)}</TableCell>
      {billing ? <TableCell className="pr-4 text-right font-mono whitespace-nowrap">{formatCredits(totals.costMicro ?? 0, locale)}</TableCell> : null}
    </>
  );
}

/** ShareBar is one row of a ranked bar list: a single hue, the figure printed
 * beside it, so the bar never carries the value alone.
 * ShareBar 是排名条形列表中的一行：单一色相，数值印在旁边，条形从不单独承载数值。 */
function ShareBar({ share, locale }: { share: number; locale: Locale }) {
  const figure = share > 0 && share < 0.001
    ? `<${new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 1 }).format(0.001)}`
    : new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 1 }).format(share);
  return (
    <div className="flex items-center gap-2">
      <div aria-hidden className="h-1.5 w-20 shrink-0 overflow-hidden rounded-full bg-[#B8860B]/15">
        <div className="h-full rounded-full bg-[#B8860B]" style={{ width: `${Math.min(100, share * 100)}%` }} />
      </div>
      <span className="w-12 text-xs text-muted-foreground tabular-nums">{figure}</span>
    </div>
  );
}

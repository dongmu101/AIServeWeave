"use client";

import * as React from "react";
import { toast } from "sonner";

import { SELECT_CLASS } from "@/components/console/account-sales";
import { BillingDisabled } from "@/components/console/billing-tables";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { EmptyState, ErrorState, FormError, LoadingState } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { Pager } from "@/components/console/pager";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { useResource } from "@/components/console/use-resource";
import { ErrorMessage } from "@/components/i18n/error-message";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatCredits } from "@/lib/console/billing";
import { CATALOG_HISTORY_ACTIONS, parseCatalogHistoryDetail, type CatalogHistoryAction } from "@/lib/console/catalog-history";
import { parseAuditEntries, parsePlatformOperators, type AuditEntry } from "@/lib/console/contract";
import { formatDateTime } from "@/lib/console/format";
import { parseGatewaySnapshot, type FleetSnapshot } from "@/lib/console/fleet";
import { parseRouteSnapshot } from "@/lib/console/model-routes";
import {
  catalogItems,
  EDITED_METERS,
  emptyPlanDraft,
  emptyPlanTerm,
  emptyPriceDraft,
  findViewItem,
  formatMaxChange,
  formatRate,
  isConflict,
  parseCatalogDecision,
  parseCatalogPage,
  parseCatalogPendingPage,
  parseCatalogSyncResult,
  parseEffectivePrices,
  parsePlanList,
  parsePlanResult,
  parsePricingConfig,
  parseRateInput,
  parseRevisionResult,
  planDraft,
  planRequest,
  priceChanges,
  priceDraft,
  priceItemsBody,
  priceListFromDraft,
  RATE_ONE,
  type CatalogPage,
  type CatalogPending,
  type EditedMeter,
  type EffectivePrices,
  type PlanDraft,
  type PlanProblem,
  type PriceDraft,
  type PriceItem,
  type PriceListProblem,
  type PricePlan,
  type PriceSource,
  type PricingConfig,
  type PricingModel,
} from "@/lib/console/pricing";
import { parseNoContent, type Page } from "@/lib/console/contract";
import type { MessageKey } from "@/lib/i18n/messages";
import type { Translator } from "@/lib/i18n/translate";

const METER_LABELS: Record<EditedMeter, MessageKey> = {
  input: "operator.meterInput",
  output: "operator.meterOutput",
  "input.cache_read": "operator.meterCacheRead",
  "input.cache_write": "operator.meterCacheWrite",
  "input.cache_write.1h": "operator.meterCacheWrite1h",
};

const SOURCE_LABELS: Record<PriceSource, MessageKey> = {
  override: "operator.pricingSourceOverride",
  catalog: "operator.pricingSourceCatalog",
  default: "operator.pricingSourceDefault",
  plan_fixed: "operator.pricingSourcePlanFixed",
  none: "operator.pricingSourceNone",
};

/** CATALOG_PENDING_LIMIT is how many reviews one read shows; the queue is
 * normally a handful, and the badge says "N+" beyond it.
 *
 * CATALOG_PENDING_LIMIT 是一次读取显示的审核数；队列通常只有几条，超出时角标显示 "N+"。 */
const CATALOG_PENDING_LIMIT = 200;

/** REFERENCE_CHOICES is how many catalog matches the reference pick list
 * shows; typing more of the name narrows a longer list.
 *
 * REFERENCE_CHOICES 是参照模型选择列表显示的价目表匹配数；更长的列表靠多输入几个字收窄。 */
const REFERENCE_CHOICES = 50;

function meterLabel(meter: string, t: Translator): string {
  return meter in METER_LABELS ? t(METER_LABELS[meter as EditedMeter]) : meter;
}

function tierLabel(tier: number, t: Translator, locale: string): string {
  return tier === 0 ? t("operator.pricingTierBase") : t("operator.pricingTierAbove", { tokens: new Intl.NumberFormat(locale).format(tier) });
}

function priceListProblem(problem: PriceListProblem, t: Translator): React.ReactNode {
  const meter = meterLabel(problem.meter, t);
  return problem.kind === "missing" ? t("operator.pricingProblemMissing", { meter }) : t("operator.pricingProblemInvalid", { meter });
}

/** writeFailure renders a failed pricing write: a 409 means the
 * configuration moved on, so the page re-reads and says so.
 *
 * writeFailure 渲染失败的定价写操作：409 表示配置已被修改，页面重新读取并提示。 */
function writeFailure(failure: unknown, reload: () => void): React.ReactNode {
  if (isConflict(failure)) {
    reload();
    return <Message id="operator.pricingConflict" />;
  }
  return <ErrorMessage error={failure} />;
}

/** useLastLoaded returns value, or while it is null (a re-read in flight)
 * the last non-null value seen, so a re-read does not unmount what is on
 * screen. An error still replaces it, since the caller checks the error
 * first. value must keep its identity between renders.
 *
 * useLastLoaded 返回 value；value 为 null（正在重新读取）时返回见过的最后一个非 null 值，
 * 使重新读取不会卸载屏幕上的内容。出错时仍会被替换，因为调用方先检查错误。value 在
 * 两次渲染之间必须保持同一引用。 */
function useLastLoaded<T>(value: T | null | undefined): T | null {
  const [last, setLast] = React.useState<T | null>(value ?? null);
  if (value != null && value !== last) setLast(value);
  return value ?? last;
}

/** usePendingReviews reads the catalog review queue for the tab badge and the
 * review section.
 *
 * usePendingReviews 读取价目表审核队列，供标签角标与审核分区使用。 */
export function usePendingReviews() {
  return useResource<Page<CatalogPending>>({
    method: "GET", surface: "operator", path: "/operator/v1/billing/catalog/pending",
    query: { limit: String(CATALOG_PENDING_LIMIT) }, parse: parseCatalogPendingPage,
  });
}

/** PendingBadge is the review count next to the tab label, hidden when the
 * queue is empty or unreadable.
 *
 * PendingBadge 是标签名旁的审核数量；队列为空或读取失败时不显示。 */
export function PendingBadge({ pending }: { pending: ReturnType<typeof usePendingReviews> }) {
  const { t } = useI18n();
  const n = pending.data?.items.length ?? 0;
  if (n === 0) return null;
  const label = pending.data?.nextCursor ? `${n}+` : String(n);
  return <Badge variant="destructive" className="ml-1 h-5 px-1.5" aria-label={t("operator.reviewPendingCount", { count: label })}>{label}</Badge>;
}

/**
 * PricingSection is the "model pricing" tab: every alias's price source and
 * prices at the default plan, the default price, the catalog's sync state,
 * and the per-alias editor. Writes carry the configuration revision they
 * were read at and are sent once.
 *
 * PricingSection 是「模型定价」标签页：每个别名的价格来源与默认方案下的单价、默认价、
 * 价目表同步状态，以及按别名的编辑。写操作携带读取时的配置版本，只发送一次。
 */
export function PricingSection() {
  const { t, locale } = useI18n();
  const config = useResource<PricingConfig>({ method: "GET", surface: "operator", path: "/operator/v1/billing/pricing", parse: parsePricingConfig });
  const prices = useResource<EffectivePrices>({ method: "GET", surface: "operator", path: "/operator/v1/billing/effective-prices", parse: parseEffectivePrices });
  const [editing, setEditing] = React.useState<{ alias: string | null; nonce: number } | null>(null);
  const [editingDefault, setEditingDefault] = React.useState(false);
  const [expanded, setExpanded] = React.useState<string | null>(null);

  const reload = React.useCallback(() => {
    config.reload();
    prices.reload();
  }, [config, prices]);
  // A re-read (after a save or a 409) keeps the last answer on screen, so an
  // open dialog and its message survive it.
  // 重新读取（保存之后或 409 之后）期间保留上一次的答复，使打开的对话框及其提示不被卸载。
  const loaded = React.useMemo(() => (config.data && prices.data ? { cfg: config.data, view: prices.data } : null), [config.data, prices.data]);
  const shown = useLastLoaded(loaded);

  if (config.errorKind === "not_found" || prices.errorKind === "not_found") return <BillingDisabled />;
  const error = config.error ?? prices.error;
  if (error) return <ErrorState message={error} onRetry={reload} />;
  if (!shown) return <LoadingState label={t("operator.loading")} rows={3} />;
  const { cfg, view } = shown;
  const settings = new Map(cfg.models.map((m) => [m.model, m]));

  return <div className="grid gap-4">
    <UnpricedBanner prices={view} />
    <CatalogStatus onSynced={reload} />
    <Card>
      <CardHeader><CardTitle><Message id="operator.billingDefaultPrice" /></CardTitle></CardHeader>
      <CardContent className="grid gap-3">
        <p className="text-sm text-muted-foreground"><Message id="operator.pricingDefaultPriceHint" /></p>
        {cfg.defaultPrices.length === 0
          ? <p className="text-sm"><Message id="operator.pricingNoDefault" /></p>
          : <PriceSummary items={cfg.defaultPrices.map((p) => ({ ...p, baseMicroPerM: p.priceMicroPerM, saleMicroPerM: p.priceMicroPerM }))} sale={false} />}
        <div><Button size="sm" variant="outline" onClick={() => setEditingDefault(true)}><Message id="operator.pricingEditDefault" /></Button></div>
      </CardContent>
    </Card>
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle><Message id="operator.billingPrices" /></CardTitle>
        <Button size="sm" onClick={() => setEditing({ alias: null, nonce: Date.now() })}><Message id="operator.pricingAddModel" /></Button>
      </CardHeader>
      <CardContent className="grid gap-3">
        <p className="text-sm text-muted-foreground"><Message id="operator.pricingUnitHint" /></p>
        {view.items.length === 0 ? <EmptyState title={t("operator.pricingNoAliases")} /> : <Table>
          <TableHeader><TableRow>
            <TableHead><Message id="operator.billingModel" /></TableHead>
            <TableHead><Message id="operator.pricingSource" /></TableHead>
            <TableHead className="text-right"><Message id="operator.pricingBaseInputOutput" /></TableHead>
            <TableHead className="text-right"><Message id="operator.pricingModelRate" /></TableHead>
            <TableHead className="text-right"><Message id="operator.pricingSaleInputOutput" /></TableHead>
            <TableHead><span className="sr-only"><Message id="tenant.action" /></span></TableHead>
          </TableRow></TableHeader>
          <TableBody>{view.items.map((item) => {
            const input = findViewItem(item.items, "input");
            const output = findViewItem(item.items, "output");
            const open = expanded === item.model;
            return <React.Fragment key={item.model}>
              <TableRow>
                <TableCell className="max-w-64"><span className="flex flex-wrap items-center gap-2"><code className="text-xs break-all">{item.model}</code>{!item.routed ? <Badge variant="outline"><Message id="operator.pricingNotRouted" /></Badge> : null}</span></TableCell>
                <TableCell className="max-w-56"><span className="grid gap-0.5">
                  <Badge variant={item.source === "none" ? "destructive" : "secondary"} className="w-fit">{t(SOURCE_LABELS[item.source])}</Badge>
                  {item.ref ? <code className="text-xs text-muted-foreground break-all">{item.ref}</code> : null}
                </span></TableCell>
                <TableCell className="text-right font-mono whitespace-nowrap">{input ? formatCredits(input.baseMicroPerM, locale) : "—"} / {output ? formatCredits(output.baseMicroPerM, locale) : "—"}</TableCell>
                <TableCell className="text-right font-mono">{formatRate(item.modelRatePPM ?? RATE_ONE)}</TableCell>
                <TableCell className="text-right font-mono whitespace-nowrap">{input ? formatCredits(input.saleMicroPerM, locale) : "—"} / {output ? formatCredits(output.saleMicroPerM, locale) : "—"}</TableCell>
                <TableCell><span className="flex flex-wrap gap-2">
                  <Button size="sm" variant="ghost" aria-expanded={open} onClick={() => setExpanded(open ? null : item.model)}>{open ? t("operator.pricingHideDetails") : t("operator.pricingDetails")}</Button>
                  <Button size="sm" variant="outline" aria-label={t("operator.pricingEditModel", { model: item.model })} onClick={() => setEditing({ alias: item.model, nonce: Date.now() })}><Message id="common.edit" /></Button>
                </span></TableCell>
              </TableRow>
              {open ? <TableRow><TableCell colSpan={6} className="bg-muted/30"><PriceSummary items={item.items} sale /></TableCell></TableRow> : null}
            </React.Fragment>;
          })}</TableBody>
        </Table>}
      </CardContent>
    </Card>
    {editing ? <ModelPricingDialog
      key={editing.nonce}
      alias={editing.alias}
      setting={editing.alias ? settings.get(editing.alias) ?? null : null}
      configured={settings}
      revision={cfg.revision.id}
      onClose={() => setEditing(null)}
      onSaved={reload}
    /> : null}
    {editingDefault ? <DefaultPriceDialog prices={cfg.defaultPrices} revision={cfg.revision.id} onClose={() => setEditingDefault(false)} onSaved={reload} /> : null}
  </div>;
}

/** PriceSummary lists every resolved price of an alias by tier and meter.
 *
 * PriceSummary 按档位与计量项列出某个别名解析后的全部单价。 */
function PriceSummary({ items, sale }: { items: readonly { meter: string; variant: string; aboveInputTokens: number; baseMicroPerM: number; saleMicroPerM: number }[]; sale: boolean }) {
  const { t, locale } = useI18n();
  const rows = [...items].filter((item) => item.variant === "").sort((a, b) => a.aboveInputTokens - b.aboveInputTokens || a.meter.localeCompare(b.meter));
  return <Table>
    <TableHeader><TableRow>
      <TableHead><Message id="operator.pricingTier" /></TableHead>
      <TableHead><Message id="operator.pricingMeter" /></TableHead>
      <TableHead className="text-right"><Message id="operator.pricingBase" /></TableHead>
      {sale ? <TableHead className="text-right"><Message id="operator.pricingSale" /></TableHead> : null}
    </TableRow></TableHeader>
    <TableBody>{rows.map((item) => <TableRow key={`${item.aboveInputTokens}/${item.meter}`}>
      <TableCell className="whitespace-nowrap text-xs">{tierLabel(item.aboveInputTokens, t, locale)}</TableCell>
      <TableCell className="text-xs">{meterLabel(item.meter, t)}</TableCell>
      <TableCell className="text-right font-mono">{formatCredits(item.baseMicroPerM, locale)}</TableCell>
      {sale ? <TableCell className="text-right font-mono">{formatCredits(item.saleMicroPerM, locale)}</TableCell> : null}
    </TableRow>)}</TableBody>
  </Table>;
}

/** UnpricedBanner warns that some aliases are being charged nothing.
 *
 * UnpricedBanner 警告有别名正在以 0 价计费。 */
function UnpricedBanner({ prices }: { prices: EffectivePrices }) {
  const { t } = useI18n();
  const aliases = prices.unpricedAliases;
  if (aliases.length === 0 && prices.unpricedRequests24h === 0) return null;
  return <div role="alert" className="rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm">
    <p className="font-medium">{t("operator.pricingUnpriced", { count: aliases.length })}</p>
    <p className="break-all text-muted-foreground">{t("operator.pricingUnpricedDescription", { aliases: aliases.length > 0 ? aliases.join(", ") : "—", requests: prices.unpricedRequests24h })}</p>
  </div>;
}

/** CatalogStatus shows the catalog's sync state and syncs on request.
 *
 * CatalogStatus 显示价目表的同步状态，并按需立即同步。 */
function CatalogStatus({ onSynced }: { onSynced: () => void }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const catalog = useResource<CatalogPage>({ method: "GET", surface: "operator", path: "/operator/v1/billing/catalog", query: { limit: "1" }, parse: parseCatalogPage });
  const [syncing, setSyncing] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  async function sync() {
    if (syncing) return;
    setSyncing(true);
    setError(null);
    try {
      const result = await run({ method: "POST", surface: "operator", path: "/operator/v1/billing/catalog/sync", parse: parseCatalogSyncResult });
      toast.success(result.skipped ? t("operator.pricingSyncSkipped") : t("operator.pricingSynced", { added: result.added, applied: result.applied, reviewed: result.reviewed, removed: result.removed }));
      onSynced();
    } catch (failure) {
      setError(<ErrorMessage error={failure} />);
    } finally {
      setSyncing(false);
      catalog.reload();
    }
  }

  return <Card>
    <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
      <CardTitle><Message id="operator.pricingCatalog" /></CardTitle>
      <Button size="sm" variant="outline" disabled={syncing} onClick={sync}><Message id="operator.pricingSyncNow" /></Button>
    </CardHeader>
    <CardContent className="grid gap-1 text-sm">
      {catalog.error ? <ErrorState message={catalog.error} onRetry={catalog.reload} /> : !catalog.data ? <LoadingState label={t("operator.loading")} rows={1} /> : <>
        <p className="break-all">{t("operator.pricingCatalogSummary", { count: new Intl.NumberFormat(locale).format(catalog.data.sync.entryCount), source: catalog.data.sync.source || "—" })}</p>
        <p className="text-muted-foreground">{catalog.data.sync.syncedAt ? t("operator.pricingCatalogSynced", { time: formatDateTime(catalog.data.sync.syncedAt, undefined, locale) }) : t("operator.pricingCatalogNever")}</p>
        {catalog.data.sync.lastErrorAt ? <p className="text-destructive">{t("operator.pricingCatalogError", { time: formatDateTime(catalog.data.sync.lastErrorAt, undefined, locale) })}</p> : null}
      </>}
      <FormError message={error} />
    </CardContent>
  </Card>;
}

/** PriceFields edits a price form, one field per edited meter.
 *
 * PriceFields 编辑价格表单，每个被编辑的计量项一个字段。 */
function PriceFields({ id, draft, onChange, disabled }: { id: string; draft: PriceDraft; onChange: (next: PriceDraft) => void; disabled: boolean }) {
  const { t } = useI18n();
  return <div className="grid gap-3 sm:grid-cols-2">
    {EDITED_METERS.map((meter) => <div key={meter} className="grid gap-1">
      <label htmlFor={`${id}-${meter}`} className="text-sm font-medium">{t("operator.pricingPriceField", { meter: meterLabel(meter, t) })}</label>
      <Input id={`${id}-${meter}`} inputMode="decimal" value={draft[meter]} disabled={disabled} onChange={(e) => onChange({ ...draft, [meter]: e.target.value })} />
    </div>)}
  </div>;
}

/** DefaultPriceDialog sets or clears the default price.
 *
 * DefaultPriceDialog 设置或清除默认价。 */
function DefaultPriceDialog({ prices, revision, onClose, onSaved }: { prices: PriceItem[]; revision: number; onClose: () => void; onSaved: () => void }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const [draft, setDraft] = React.useState(() => priceDraft(prices));
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  async function save(body: PriceItem[]) {
    setPending(true);
    setError(null);
    try {
      await run({ method: "PUT", surface: "operator", path: "/operator/v1/billing/pricing/default-prices", body: { prices: priceItemsBody(body), base_revision: revision }, parse: parsePricingConfig });
      toast.success(<Message id="operator.pricingSaved" />);
      onSaved();
      onClose();
    } catch (failure) {
      setError(writeFailure(failure, onSaved));
      setPending(false);
    }
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const built = priceListFromDraft(draft, prices);
    if (built.problem !== null) {
      setError(priceListProblem(built.problem, t));
      return;
    }
    await save(built.prices);
  }

  return <Dialog open onOpenChange={(next) => { if (!next && !pending) onClose(); }}>
    <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
      <form onSubmit={submit} className="grid gap-4" noValidate>
        <DialogHeader>
          <DialogTitle><Message id="operator.pricingEditDefault" /></DialogTitle>
          <DialogDescription><Message id="operator.pricingDefaultPriceHint" /></DialogDescription>
        </DialogHeader>
        <PriceFields id="default-price" draft={draft} onChange={setDraft} disabled={pending} />
        <FormError message={error} />
        <DialogFooter className="flex-wrap gap-2">
          {prices.length > 0 ? <Button type="button" variant="ghost" disabled={pending} onClick={() => save([])}><Message id="operator.pricingClearDefault" /></Button> : null}
          <Button type="button" variant="outline" disabled={pending} onClick={onClose}><Message id="common.cancel" /></Button>
          <SubmitButton pending={pending}><Message id="common.save" /></SubmitButton>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>;
}

/** ModelPricingDialog sets one alias's reference model, override price and
 * model rate, or clears its settings.
 *
 * ModelPricingDialog 设置一个别名的参照模型、覆盖价与模型倍率，或清除其设置。 */
function ModelPricingDialog({ alias, setting, configured, revision, onClose, onSaved }: {
  alias: string | null;
  setting: PricingModel | null;
  configured: ReadonlyMap<string, PricingModel>;
  revision: number;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const [model, setModel] = React.useState(alias ?? "");
  const [reference, setReference] = React.useState(setting?.catalogModel ?? "");
  const [override, setOverride] = React.useState((setting?.prices.length ?? 0) > 0);
  const [draft, setDraft] = React.useState(() => (setting && setting.prices.length > 0 ? priceDraft(setting.prices) : emptyPriceDraft()));
  const [kept, setKept] = React.useState<PriceItem[]>(setting?.prices ?? []);
  const [rate, setRate] = React.useState(setting ? formatRate(setting.ratePPM).slice(0, -1) : "1");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const [removing, setRemoving] = React.useState(false);
  // The reference pick list is a catalog prefix search on what was typed
  // into the reference box, or on the alias while nothing is typed. Picking
  // from the list does not change the search, so the other matches stay
  // listed to pick from again.
  // 参照模型的选择列表按参照输入框里手动输入的内容做价目表前缀搜索，未输入时按别名搜索。
  // 从列表选择不改变搜索，其他匹配项仍留在列表里可以重新选择。
  const [query, setQuery] = React.useState("");
  const search = React.useDeferredValue(query.trim() || model.trim());
  // Only a new alias offers a pick list: published route aliases that have
  // no pricing settings yet. Picking an already configured one would
  // overwrite it with blank fields, so those are edited from the table.
  // 只有新建别名时提供选择列表：已发布、尚未有定价设置的路由别名。选择已配置的
  // 别名会用空白字段覆盖原设置，因此它们只从表格里编辑。
  const routes = useResource({ method: "GET", surface: "operator", path: "/operator/v1/routes", parse: parseRouteSnapshot });
  // Node-reported model ids are offered too: a request may name one
  // directly when no route covers it.
  // 也提供节点上报的模型 id：没有路由覆盖时，请求可以直接使用它。
  const fleet = useResource<FleetSnapshot>({ method: "GET", surface: "operator", path: "/operator/v1/nodes", query: { gateway: "default" }, parse: parseGatewaySnapshot });
  const routeChoices = React.useMemo(() => [...new Set((routes.data?.routes ?? []).map((route) => route.model))].filter((name) => !configured.has(name)).sort(), [routes.data, configured]);
  const nodeChoices = React.useMemo(() => {
    const runtimes = [...(fleet.data?.replicas[0]?.localRuntimes ?? []), ...(fleet.data?.nodes ?? []).flatMap((node) => node.runtimes)];
    const taken = new Set(routeChoices);
    return [...new Set(runtimes.flatMap((rt) => rt.models.map((m) => m.id)))].filter((name) => name !== "" && !taken.has(name) && !configured.has(name)).sort();
  }, [fleet.data, routeChoices, configured]);
  const choices = React.useMemo(() => [...routeChoices, ...nodeChoices], [routeChoices, nodeChoices]);
  const choicesLoaded = routes.data !== null || routes.error !== null;
  const matches = useResource<CatalogPage>({
    method: "GET", surface: "operator", path: "/operator/v1/billing/catalog",
    query: { q: search, limit: search.length >= 2 ? String(REFERENCE_CHOICES) : "1" }, parse: parseCatalogPage,
  });

  async function copyOfficial() {
    const name = reference.trim() || model.trim();
    setError(null);
    try {
      const page = await run({ method: "GET", surface: "operator", path: "/operator/v1/billing/catalog", query: { q: name, limit: "1" }, parse: parseCatalogPage });
      const entry = page.items.find((item) => item.model === name);
      if (!entry) {
        setError(t("operator.pricingCopyMissing", { model: name || "—" }));
        return;
      }
      const items = catalogItems(entry.prices);
      setDraft(priceDraft(items));
      setKept(items);
      setOverride(true);
    } catch (failure) {
      setError(<ErrorMessage error={failure} />);
    }
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    // A listed alias is shown read-only and sent as it is; only a typed one
    // is checked here. The control plane has the final word on both.
    // 已列出的别名只读、原样发送；这里只检查手动输入的别名。两者都以控制面的校验为准。
    const name = alias ?? model.trim();
    if (alias === null && (name === "" || new TextEncoder().encode(name).length > 128)) {
      setError(<Message id="operator.billingProblemModel" />);
      return;
    }
    // The control plane refuses a reference the catalog lacks with a bare
    // 400, so it is checked here first to name the cause.
    // 控制面对价目表中不存在的参照模型只返回笼统的 400，因此先在这里检查，以便说明原因。
    const ref = reference.trim();
    if (ref !== "") {
      try {
        const page = await run({ method: "GET", surface: "operator", path: "/operator/v1/billing/catalog", query: { q: ref, limit: "1" }, parse: parseCatalogPage });
        if (!page.items.some((item) => item.model === ref)) {
          setError(t("operator.pricingReferenceMissing", { model: ref }));
          return;
        }
      } catch {
        // Left to the control plane, which checks it again on save.
        // 交给控制面，它在保存时会再检查一次。
      }
    }
    const ratePPM = parseRateInput(rate);
    if (ratePPM === null) {
      setError(<Message id="operator.pricingProblemRate" />);
      return;
    }
    let prices: PriceItem[] = [];
    if (override) {
      const built = priceListFromDraft(draft, kept);
      if (built.problem !== null) {
        setError(priceListProblem(built.problem, t));
        return;
      }
      prices = built.prices;
    }
    setPending(true);
    setError(null);
    try {
      await run({
        method: "PUT", surface: "operator", path: "/operator/v1/billing/pricing/models",
        body: { model: name, catalog_model: reference.trim(), prices: priceItemsBody(prices), rate_ppm: ratePPM, base_revision: revision },
        parse: parsePricingConfig,
      });
      toast.success(<Message id="operator.pricingSaved" />);
      onSaved();
      onClose();
    } catch (failure) {
      setError(writeFailure(failure, onSaved));
      setPending(false);
    }
  }

  const found = search.length >= 2 ? (matches.data?.items ?? []).filter((item) => item.status !== "removed") : [];
  // The saved reference stays listed even when the alias search does not
  // reach it, so an existing setting shows as picked.
  // 已保存的参照模型即使不在按别名搜索的结果里也保留在列表中，现有设置因此显示为已选中。
  const saved = setting?.catalogModel ?? "";
  const options = saved !== "" && reference.trim() === saved && !found.some((item) => item.model === saved) ? [{ model: saved, provider: "" }, ...found] : found;
  const referencePlaceholder = search.length < 2 ? t("operator.pricingReferenceTypeMore")
    : matches.loading ? t("operator.loading")
    : options.length > 0 ? t("operator.pricingReferencePick")
    : t("operator.pricingSearchNoMatch", { prefix: search });
  return <Dialog open onOpenChange={(next) => { if (!next && !pending) onClose(); }}>
    <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
      <form onSubmit={submit} className="grid gap-4" noValidate>
        <DialogHeader>
          <DialogTitle className="break-all">{alias ? t("operator.pricingEditModel", { model: alias }) : t("operator.pricingAddModel")}</DialogTitle>
          <DialogDescription><Message id="operator.pricingUnitHint" /></DialogDescription>
        </DialogHeader>
        <div className="grid gap-1">
          <label htmlFor="pricing-model" className="text-sm font-medium"><Message id="operator.billingModel" /></label>
          {alias === null ? <select aria-label={t("operator.pricingAliasPick")} className={SELECT_CLASS} value={choices.includes(model.trim()) ? model.trim() : ""} disabled={pending || choices.length === 0} onChange={(e) => setModel(e.target.value)}>
            <option value="">{!choicesLoaded ? t("operator.loading") : choices.length > 0 ? t("operator.pricingAliasPick") : t("operator.pricingAliasNoChoices")}</option>
            {routeChoices.length > 0 ? <optgroup label={t("operator.pricingAliasGroupRoutes")}>{routeChoices.map((name) => <option key={name} value={name}>{name}</option>)}</optgroup> : null}
            {nodeChoices.length > 0 ? <optgroup label={t("operator.pricingAliasGroupNodes")}>{nodeChoices.map((name) => <option key={name} value={name}>{name}</option>)}</optgroup> : null}
          </select> : null}
          <Input id="pricing-model" value={model} onChange={(e) => setModel(e.target.value)} disabled={pending || alias !== null} autoComplete="off" spellCheck={false} />
          {alias === null && choicesLoaded ? <p className="text-xs text-muted-foreground"><Message id={choices.length > 0 ? "operator.pricingAliasHint" : "operator.pricingAliasNone"} /></p> : null}
        </div>
        <div className="grid gap-1">
          <label htmlFor="pricing-reference" className="text-sm font-medium"><Message id="operator.pricingReference" /></label>
          <select aria-label={t("operator.pricingReferencePick")} className={SELECT_CLASS} value={options.some((item) => item.model === reference.trim()) ? reference.trim() : ""} disabled={pending || options.length === 0} onChange={(e) => setReference(e.target.value)}>
            <option value="">{referencePlaceholder}</option>
            {options.map((item) => <option key={item.model} value={item.model}>{item.provider ? `${item.model} · ${item.provider}` : item.model}</option>)}
            {search.length >= 2 && matches.data?.nextCursor ? <option value="" disabled>{t("operator.pricingReferenceMore", { count: REFERENCE_CHOICES })}</option> : null}
          </select>
          <Input id="pricing-reference" value={reference} onChange={(e) => { setReference(e.target.value); setQuery(e.target.value); }} disabled={pending} autoComplete="off" spellCheck={false} />
          <p className="text-xs text-muted-foreground"><Message id="operator.pricingReferenceHint" /></p>
        </div>
        <div className="grid gap-1">
          <label htmlFor="pricing-rate" className="text-sm font-medium"><Message id="operator.pricingModelRate" /></label>
          <Input id="pricing-rate" inputMode="decimal" value={rate} onChange={(e) => setRate(e.target.value)} disabled={pending} />
          <p className="text-xs text-muted-foreground"><Message id="operator.pricingRateHint" /></p>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={override} onChange={(e) => setOverride(e.target.checked)} disabled={pending} />
          <Message id="operator.pricingUseOverride" />
        </label>
        {override ? <PriceFields id="pricing-override" draft={draft} onChange={setDraft} disabled={pending} /> : null}
        <div><Button type="button" size="sm" variant="outline" disabled={pending} onClick={copyOfficial}><Message id="operator.pricingCopyOfficial" /></Button></div>
        <FormError message={error} />
        <DialogFooter className="flex-wrap gap-2">
          {setting ? <Button type="button" variant="ghost" disabled={pending} onClick={() => setRemoving(true)}><Message id="operator.pricingRemoveModel" /></Button> : null}
          <Button type="button" variant="outline" disabled={pending} onClick={onClose}><Message id="common.cancel" /></Button>
          <SubmitButton pending={pending}><Message id="common.save" /></SubmitButton>
        </DialogFooter>
      </form>
    </DialogContent>
    <ConfirmDialog
      open={removing}
      onOpenChange={setRemoving}
      title={t("operator.pricingRemoveModel")}
      destructive
      confirmLabel={t("operator.pricingRemoveModel")}
      description={t("operator.pricingRemoveConfirm", { model: alias ?? "" })}
      onConfirm={async () => {
        try {
          await run({ method: "DELETE", surface: "operator", path: "/operator/v1/billing/pricing/models", query: { model: alias ?? "", base_revision: String(revision) }, parse: parsePricingConfig });
        } catch (failure) {
          if (isConflict(failure)) onSaved();
          throw failure;
        }
        toast.success(<Message id="operator.pricingRemoved" />);
        onSaved();
        onClose();
      }}
    />
  </Dialog>;
}

/**
 * CatalogReviewSection lists the upstream catalog changes waiting for an
 * operator, with the old and new prices side by side, and approves or
 * rejects them one at a time.
 *
 * CatalogReviewSection 列出等待运维处理的上游价目表变动，并排显示新旧单价，逐条批准或
 * 拒绝。
 */
export function CatalogReviewSection({ pending }: { pending: ReturnType<typeof usePendingReviews> }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [deciding, setDeciding] = React.useState<{ item: CatalogPending; approve: boolean } | null>(null);

  const [historyNonce, setHistoryNonce] = React.useState(0);

  if (pending.errorKind === "not_found") return <BillingDisabled />;
  return <div className="grid gap-4"><Card>
    <CardHeader><CardTitle><Message id="operator.billingCatalogReview" /></CardTitle></CardHeader>
    <CardContent className="grid gap-3">
      <p className="text-sm text-muted-foreground"><Message id="operator.reviewHint" /></p>
      {pending.error ? <ErrorState message={pending.error} onRetry={pending.reload} /> : pending.loading || !pending.data ? <LoadingState label={t("operator.loading")} rows={2} /> : pending.data.items.length === 0 ? <EmptyState title={t("operator.reviewEmpty")} /> : <ul className="grid gap-3">
        {pending.data.items.map((item) => {
          const change = formatMaxChange(item.maxChangeBP);
          const rows = priceChanges(item.current, item.proposed);
          return <li key={item.model} className="grid gap-2 rounded-md border p-3">
            <div className="flex flex-wrap items-center gap-2">
              <code className="text-sm break-all">{item.model}</code>
              <Badge variant={item.kind === "remove" ? "destructive" : "secondary"}>{item.kind === "remove" ? t("operator.reviewKindRemove") : t("operator.reviewKindChange")}</Badge>
              <span className="text-xs text-muted-foreground">{change ? t("operator.reviewMaxChange", { change }) : t("operator.reviewUnbounded")}</span>
              <span className="text-xs text-muted-foreground">{t("operator.reviewDetected", { time: formatDateTime(item.detectedAt, undefined, locale) })}</span>
            </div>
            {item.kind === "remove" ? null : <Table>
              <TableHeader><TableRow>
                <TableHead><Message id="operator.pricingTier" /></TableHead>
                <TableHead><Message id="operator.pricingMeter" /></TableHead>
                <TableHead className="text-right"><Message id="operator.reviewBefore" /></TableHead>
                <TableHead className="text-right"><Message id="operator.reviewAfter" /></TableHead>
                <TableHead className="text-right"><Message id="operator.reviewChange" /></TableHead>
              </TableRow></TableHeader>
              <TableBody>{rows.map((row) => <TableRow key={`${row.variant}/${row.aboveInputTokens}/${row.meter}`}>
                <TableCell className="whitespace-nowrap text-xs">{tierLabel(row.aboveInputTokens, t, locale)}{row.variant ? ` · ${row.variant}` : ""}</TableCell>
                <TableCell className="text-xs">{meterLabel(row.meter, t)}</TableCell>
                <TableCell className="text-right font-mono">{row.before === null ? "—" : formatCredits(row.before, locale)}</TableCell>
                <TableCell className="text-right font-mono">{row.after === null ? "—" : formatCredits(row.after, locale)}</TableCell>
                <TableCell className="text-right font-mono">{row.changeBP === null ? "—" : `${row.changeBP > 0 ? "+" : ""}${formatMaxChange(row.changeBP)}`}</TableCell>
              </TableRow>)}</TableBody>
            </Table>}
            <div className="flex flex-wrap gap-2">
              <Button size="sm" onClick={() => setDeciding({ item, approve: true })}><Message id="operator.reviewApprove" /></Button>
              <Button size="sm" variant="outline" onClick={() => setDeciding({ item, approve: false })}><Message id="operator.reviewReject" /></Button>
            </div>
          </li>;
        })}
      </ul>}
    </CardContent>
    <ConfirmDialog
      open={deciding !== null}
      onOpenChange={(open) => { if (!open) setDeciding(null); }}
      title={deciding?.approve ? t("operator.reviewApprove") : t("operator.reviewReject")}
      destructive={deciding?.item.kind === "remove" && deciding.approve}
      confirmLabel={deciding?.approve ? t("operator.reviewApprove") : t("operator.reviewReject")}
      description={!deciding ? "" : !deciding.approve ? t("operator.reviewRejectConfirm") : deciding.item.kind === "remove" ? t("operator.reviewRemoveConfirm", { model: deciding.item.model }) : t("operator.reviewApproveConfirm", { model: deciding.item.model })}
      onConfirm={async () => {
        if (!deciding) return;
        const path = deciding.approve ? "/operator/v1/billing/catalog/pending/approve" : "/operator/v1/billing/catalog/pending/reject";
        try {
          await run({ method: "POST", surface: "operator", path, body: { model: deciding.item.model }, parse: parseCatalogDecision });
        } finally {
          pending.reload();
          setHistoryNonce((n) => n + 1);
        }
        toast.success(<Message id={deciding.approve ? "operator.reviewApproved" : "operator.reviewRejected"} />);
        setDeciding(null);
      }}
    />
  </Card>
  <CatalogHistorySection key={historyNonce} />
  </div>;
}

const HISTORY_LABELS: Record<CatalogHistoryAction, MessageKey> = {
  "billing.catalog.approve": "operator.reviewHistoryApproved",
  "billing.catalog.reject": "operator.reviewHistoryRejected",
  "billing.catalog.auto_apply": "operator.reviewHistoryAutoApplied",
};

/**
 * CatalogHistorySection lists past catalog decisions of one kind from the
 * platform audit log, with who made them. Operator ids are shown by name
 * when they are on the first page of operator accounts.
 *
 * CatalogHistorySection 从平台审计日志列出某一类过往的价目表决定及其操作人。操作人 id
 * 在运维账户第一页中能找到时显示为名称。
 */
function CatalogHistorySection() {
  const { t, locale } = useI18n();
  const [action, setAction] = React.useState<CatalogHistoryAction>("billing.catalog.approve");
  const history = usePagedResource<AuditEntry>({ path: "/operator/v1/audit", surface: "operator", filters: { action }, pageSize: 25, parse: parseAuditEntries });
  const operators = useResource({ method: "GET", surface: "operator", path: "/operator/v1/operators", query: { limit: "200" }, parse: parsePlatformOperators });
  const names = React.useMemo(() => new Map((operators.data?.items ?? []).map((op) => [op.id, op.name ? `${op.name} (${op.email})` : op.email])), [operators.data]);
  const credits = (value: number | null) => (value === null ? "—" : formatCredits(value, locale));

  return <Card>
    <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
      <CardTitle><Message id="operator.reviewHistory" /></CardTitle>
      <select aria-label={t("operator.reviewHistory")} className={`${SELECT_CLASS} h-8 w-auto`} value={action} onChange={(e) => setAction(e.target.value as CatalogHistoryAction)}>
        {CATALOG_HISTORY_ACTIONS.map((value) => <option key={value} value={value}>{t(HISTORY_LABELS[value])}</option>)}
      </select>
    </CardHeader>
    <CardContent className="grid gap-3">
      <p className="text-sm text-muted-foreground"><Message id="operator.reviewHistoryHint" /></p>
      {history.error ? <ErrorState message={history.error} onRetry={history.reload} /> : !history.items ? <LoadingState label={t("operator.loading")} rows={2} /> : history.items.length === 0 ? <EmptyState title={t("operator.reviewHistoryEmpty")} /> : <Table>
        <TableHeader><TableRow>
          <TableHead><Message id="tenant.time" /></TableHead>
          <TableHead><Message id="operator.billingModel" /></TableHead>
          <TableHead><Message id="operator.reviewHistoryActor" /></TableHead>
          <TableHead className="text-right"><Message id="operator.reviewHistoryInput" /></TableHead>
          <TableHead className="text-right"><Message id="operator.reviewHistoryOutput" /></TableHead>
          <TableHead>IP</TableHead>
        </TableRow></TableHeader>
        <TableBody>{history.items.map((entry) => {
          const detail = parseCatalogHistoryDetail(entry.detail);
          const priced = action === "billing.catalog.approve";
          return <TableRow key={entry.id}>
            <TableCell className="whitespace-nowrap text-xs">{formatDateTime(entry.createdAt, undefined, locale)}</TableCell>
            <TableCell className="max-w-64"><span className="flex flex-wrap items-center gap-2">
              <code className="text-xs break-all">{entry.target}</code>
              {detail.kind === "remove" ? <Badge variant="destructive">{t("operator.reviewKindRemove")}</Badge> : detail.kind === "change" ? <Badge variant="secondary">{t("operator.reviewKindChange")}</Badge> : null}
              {detail.maxChange ? <span className="text-xs text-muted-foreground">{t("operator.reviewMaxChange", { change: detail.maxChange })}</span> : null}
            </span></TableCell>
            <TableCell className="max-w-56 text-xs break-all">{entry.actorId === "" ? t("operator.reviewHistorySystem") : names.get(entry.actorId) ?? <code>{entry.actorId}</code>}</TableCell>
            <TableCell className="text-right font-mono whitespace-nowrap text-xs">{priced ? `${credits(detail.inputBefore)} → ${credits(detail.inputAfter)}` : "—"}</TableCell>
            <TableCell className="text-right font-mono whitespace-nowrap text-xs">{priced ? `${credits(detail.outputBefore)} → ${credits(detail.outputAfter)}` : "—"}</TableCell>
            <TableCell className="font-mono text-xs">{entry.ip || "—"}</TableCell>
          </TableRow>;
        })}</TableBody>
      </Table>}
      <Pager resource={history} loadedCount={history.items?.length ?? 0} />
    </CardContent>
  </Card>;
}

/**
 * PlansSection lists the price plans with their tenant counts and edits
 * them; the default plan cannot be deleted.
 *
 * PlansSection 列出价格方案及其租户数并编辑它们；默认方案不能删除。
 */
export function PlansSection() {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const plans = usePricePlans();
  const shown = useLastLoaded(plans.data);
  const [editor, setEditor] = React.useState<{ plan: PricePlan | null; nonce: number } | null>(null);
  const [removing, setRemoving] = React.useState<PricePlan | null>(null);

  if (plans.errorKind === "not_found") return <BillingDisabled />;
  return <Card>
    <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
      <CardTitle><Message id="operator.billingPlans" /></CardTitle>
      <Button size="sm" disabled={!shown} onClick={() => setEditor({ plan: null, nonce: Date.now() })}><Message id="operator.planNew" /></Button>
    </CardHeader>
    <CardContent className="grid gap-3">
      <p className="text-sm text-muted-foreground"><Message id="operator.plansHint" /></p>
      {plans.error ? <ErrorState message={plans.error} onRetry={plans.reload} /> : !shown ? <LoadingState label={t("operator.loading")} rows={2} /> : <Table>
        <TableHeader><TableRow>
          <TableHead><Message id="operator.planName" /></TableHead>
          <TableHead className="text-right"><Message id="operator.planDefaultRate" /></TableHead>
          <TableHead className="text-right"><Message id="operator.planTerms" /></TableHead>
          <TableHead className="text-right"><Message id="operator.planTenants" /></TableHead>
          <TableHead><span className="sr-only"><Message id="tenant.action" /></span></TableHead>
        </TableRow></TableHeader>
        <TableBody>{shown.items.map((plan) => <TableRow key={plan.id}>
          <TableCell className="max-w-64"><span className="flex flex-wrap items-center gap-2 break-all">{plan.name}{plan.isDefault ? <Badge variant="secondary"><Message id="operator.planDefaultBadge" /></Badge> : null}</span></TableCell>
          <TableCell className="text-right font-mono">{formatRate(plan.defaultRatePPM)}</TableCell>
          <TableCell className="text-right font-mono">{plan.models.length}</TableCell>
          <TableCell className="text-right">{plan.tenantCount === null ? <span className="text-xs text-muted-foreground"><Message id="operator.planAllTenants" /></span> : <span className="font-mono">{plan.tenantCount}</span>}</TableCell>
          <TableCell><span className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" aria-label={t("operator.planEdit", { name: plan.name })} onClick={() => setEditor({ plan, nonce: Date.now() })}><Message id="common.edit" /></Button>
            {plan.isDefault ? null : <Button size="sm" variant="ghost" onClick={() => setRemoving(plan)}><Message id="common.delete" /></Button>}
          </span></TableCell>
        </TableRow>)}</TableBody>
      </Table>}
    </CardContent>
    {editor && shown ? <PlanDialog key={editor.nonce} plan={editor.plan} revision={shown.revision.id} onClose={() => setEditor(null)} onSaved={plans.reload} /> : null}
    <ConfirmDialog
      open={removing !== null}
      onOpenChange={(open) => { if (!open) setRemoving(null); }}
      title={t("common.delete")}
      destructive
      confirmLabel={t("common.delete")}
      description={removing ? t("operator.planDeleteConfirm", { name: removing.name }) : ""}
      onConfirm={async () => {
        if (!removing || !shown) return;
        try {
          await run({ method: "DELETE", surface: "operator", path: `/operator/v1/billing/plans/${encodeURIComponent(removing.id)}`, query: { base_revision: String(shown.revision.id) }, parse: parseRevisionResult });
        } catch (failure) {
          plans.reload();
          throw failure;
        }
        toast.success(<Message id="operator.planDeleted" />);
        setRemoving(null);
        plans.reload();
      }}
    />
  </Card>;
}

function planProblem(problem: PlanProblem, t: Translator): React.ReactNode {
  switch (problem.kind) {
    case "name": return t("operator.planProblemName");
    case "default_rate": return t("operator.pricingProblemRate");
    case "rate": return `${t("operator.planRow", { row: problem.row + 1 })} ${t("operator.pricingProblemRate")}`;
    case "model": return `${t("operator.planRow", { row: problem.row + 1 })} ${t("operator.billingProblemModel")}`;
    case "duplicate": return `${t("operator.planRow", { row: problem.row + 1 })} ${t("operator.planProblemDuplicate")}`;
    case "price": return `${t("operator.planRow", { row: problem.row + 1 })} ${priceListProblem(problem.problem, t)}`;
  }
}

/** PlanDialog creates or edits a plan.
 *
 * PlanDialog 新建或编辑一个方案。 */
function PlanDialog({ plan, revision, onClose, onSaved }: { plan: PricePlan | null; revision: number; onClose: () => void; onSaved: () => void }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const [draft, setDraft] = React.useState<PlanDraft>(() => (plan ? planDraft(plan) : emptyPlanDraft()));
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  function setTerm(row: number, patch: Partial<PlanDraft["terms"][number]>) {
    setDraft({ ...draft, terms: draft.terms.map((term, i) => (i === row ? { ...term, ...patch } : term)) });
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const built = planRequest(draft);
    if (built.problem !== null) {
      setError(planProblem(built.problem, t));
      return;
    }
    setPending(true);
    setError(null);
    try {
      await run({
        method: plan ? "PUT" : "POST", surface: "operator",
        path: plan ? `/operator/v1/billing/plans/${encodeURIComponent(plan.id)}` : "/operator/v1/billing/plans",
        body: { ...built.body, base_revision: revision }, parse: parsePlanResult,
      });
      toast.success(<Message id="operator.planSaved" />);
      onSaved();
      onClose();
    } catch (failure) {
      setError(writeFailure(failure, onSaved));
      setPending(false);
    }
  }

  return <Dialog open onOpenChange={(next) => { if (!next && !pending) onClose(); }}>
    <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
      <form onSubmit={submit} className="grid gap-4" noValidate>
        <DialogHeader>
          <DialogTitle className="break-all">{plan ? t("operator.planEdit", { name: plan.name }) : t("operator.planNew")}</DialogTitle>
          <DialogDescription><Message id="operator.plansHint" /></DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="grid gap-1">
            <label htmlFor="plan-name" className="text-sm font-medium"><Message id="operator.planName" /></label>
            <Input id="plan-name" value={draft.name} maxLength={128} onChange={(e) => setDraft({ ...draft, name: e.target.value })} disabled={pending} />
          </div>
          <div className="grid gap-1">
            <label htmlFor="plan-rate" className="text-sm font-medium"><Message id="operator.planDefaultRate" /></label>
            <Input id="plan-rate" inputMode="decimal" value={draft.defaultRate} onChange={(e) => setDraft({ ...draft, defaultRate: e.target.value })} disabled={pending} />
          </div>
        </div>
        <p className="text-xs text-muted-foreground"><Message id="operator.pricingRateHint" /></p>
        <div className="grid gap-3">
          <p className="text-sm font-medium"><Message id="operator.planTerms" /></p>
          {draft.terms.map((term, row) => <fieldset key={row} className="grid gap-3 rounded-md border p-3">
            <legend className="px-1 text-xs text-muted-foreground">{t("operator.planRow", { row: row + 1 })}</legend>
            <div className="grid gap-3 sm:grid-cols-3">
              <div className="grid gap-1 sm:col-span-2">
                <label htmlFor={`plan-term-${row}-model`} className="text-sm font-medium"><Message id="operator.billingModel" /></label>
                <Input id={`plan-term-${row}-model`} value={term.model} onChange={(e) => setTerm(row, { model: e.target.value })} disabled={pending} autoComplete="off" spellCheck={false} />
              </div>
              <div className="grid gap-1">
                <label htmlFor={`plan-term-${row}-mode`} className="text-sm font-medium"><Message id="operator.planTermMode" /></label>
                <select id={`plan-term-${row}-mode`} className={SELECT_CLASS} value={term.mode} disabled={pending} onChange={(e) => setTerm(row, { mode: e.target.value === "fixed" ? "fixed" : "rate" })}>
                  <option value="rate">{t("operator.planModeRate")}</option>
                  <option value="fixed">{t("operator.planModeFixed")}</option>
                </select>
              </div>
            </div>
            {term.mode === "rate"
              ? <div className="grid gap-1">
                <label htmlFor={`plan-term-${row}-rate`} className="text-sm font-medium"><Message id="operator.planModeRate" /></label>
                <Input id={`plan-term-${row}-rate`} inputMode="decimal" value={term.rate} onChange={(e) => setTerm(row, { rate: e.target.value })} disabled={pending} />
              </div>
              : <PriceFields id={`plan-term-${row}-price`} draft={term.prices} onChange={(prices) => setTerm(row, { prices })} disabled={pending} />}
            <div><Button type="button" size="sm" variant="ghost" disabled={pending} onClick={() => setDraft({ ...draft, terms: draft.terms.filter((_, i) => i !== row) })}><Message id="operator.planRemoveTerm" /></Button></div>
          </fieldset>)}
          <div><Button type="button" size="sm" variant="outline" disabled={pending} onClick={() => setDraft({ ...draft, terms: [...draft.terms, emptyPlanTerm()] })}><Message id="operator.planAddTerm" /></Button></div>
        </div>
        <FormError message={error} />
        <DialogFooter>
          <Button type="button" variant="outline" disabled={pending} onClick={onClose}><Message id="common.cancel" /></Button>
          <SubmitButton pending={pending}><Message id="common.save" /></SubmitButton>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>;
}

/** TenantPlanSelect changes one tenant's price plan; the default plan is
 * sent as null, which puts the tenant back on whatever plan is default.
 *
 * TenantPlanSelect 修改一个租户的价格方案；选默认方案时发送 null，让租户回到默认方案。 */
export function TenantPlanSelect({ tenantId, tenantName, planId, plans, onSaved }: {
  tenantId: string;
  tenantName: string;
  planId: string;
  plans: readonly PricePlan[];
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const [pending, setPending] = React.useState(false);
  const defaultPlan = plans.find((plan) => plan.isDefault);
  const current = planId || defaultPlan?.id || "";

  async function change(next: string) {
    if (pending || next === current) return;
    setPending(true);
    try {
      await run({
        method: "PUT", surface: "operator", path: `/operator/v1/billing/tenants/${encodeURIComponent(tenantId)}/plan`,
        body: { plan_id: next === defaultPlan?.id ? null : next }, parse: parseNoContent,
      });
      toast.success(<Message id="operator.tenantPlanSaved" />);
    } catch (failure) {
      toast.error(<ErrorMessage error={failure} />);
    } finally {
      setPending(false);
      onSaved();
    }
  }

  return <select aria-label={t("operator.tenantPlanLabel", { name: tenantName })} className={`${SELECT_CLASS} h-8 max-w-44`} value={current} disabled={pending || plans.length === 0} onChange={(e) => change(e.target.value)}>
    {plans.map((plan) => <option key={plan.id} value={plan.id}>{plan.name}</option>)}
  </select>;
}

/** usePricePlans reads the plans for the tenant list's plan column.
 *
 * usePricePlans 读取价格方案，供租户列表的方案列使用。 */
export function usePricePlans() {
  return useResource<{ revision: { id: number }; items: PricePlan[] }>({ method: "GET", surface: "operator", path: "/operator/v1/billing/plans", parse: parsePlanList });
}

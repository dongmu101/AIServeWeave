"use client";

import * as React from "react";
import { toast } from "sonner";

import { BillingDisabled, LedgerTable, OrdersTable, TenantIdFilter } from "@/components/console/billing-tables";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { Pager } from "@/components/console/pager";
import { EmptyState, ErrorState, FormError, LoadingState } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  adjustRequest,
  DEFAULT_PRICE_MODEL,
  emptyPackageDraft,
  formatCredits,
  formatMoney,
  formatScaled,
  modelPriceRequest,
  packageDraft,
  packageRequest,
  parseBillingTenants,
  parseCreditPackage,
  parseCreditPackages,
  parseLedgerEntry,
  parseModelPrice,
  parseModelPrices,
  type BillingMode,
  type BillingTenant,
  type CreditPackage,
  type ModelPrice,
  type PackageDraft,
  type PackageKind,
  type PackageProblem,
} from "@/lib/console/billing";
import { parseNoContent } from "@/lib/console/contract";
import { formatDateTime } from "@/lib/console/format";
import type { MessageKey } from "@/lib/i18n/messages";
import type { Translator } from "@/lib/i18n/translate";

const TENANT_PAGE_SIZE = 25;

const PACKAGE_PROBLEMS: Record<PackageProblem, MessageKey> = {
  name: "operator.billingProblemName",
  product: "operator.billingProblemProduct",
  currency: "operator.billingProblemCurrency",
  credits: "operator.billingProblemCredits",
  price: "operator.billingProblemPrice",
  rate: "operator.billingProblemRate",
  range: "operator.billingProblemRange",
  sort_order: "operator.billingProblemSortOrder",
};

function modeLabel(mode: BillingMode, t: Translator): string {
  return mode === "exempt" ? t("tenant.billingModeExempt") : t("tenant.billingModePrepaid");
}

/**
 * BillingAdminView splits billing administration into tabs. Only the open
 * tab is mounted, so only its reads are made. Every write is sent once and
 * followed by a re-read; nothing is retried automatically.
 *
 * BillingAdminView 把计费管理分成几个标签页。只有打开的标签页会挂载，因此只发起它的
 * 读取。每次写操作只发送一次，之后重新读取；不做任何自动重试。
 */
export function BillingAdminView() {
  const { t } = useI18n();
  return <div className="grid gap-4">
    <div>
      <h1 className="font-heading text-lg font-semibold"><Message id="operator.billing" /></h1>
      <p className="text-sm text-muted-foreground"><Message id="operator.billingDescription" /></p>
    </div>
    <Tabs defaultValue="packages">
      <TabsList aria-label={t("operator.billingSections")} className="h-auto! w-full flex-wrap justify-start">
        <TabsTrigger value="packages" className="flex-none"><Message id="tenant.billingPackages" /></TabsTrigger>
        <TabsTrigger value="prices" className="flex-none"><Message id="operator.billingPrices" /></TabsTrigger>
        <TabsTrigger value="tenants" className="flex-none"><Message id="operator.billingTenants" /></TabsTrigger>
        <TabsTrigger value="orders" className="flex-none"><Message id="tenant.billingOrders" /></TabsTrigger>
        <TabsTrigger value="ledger" className="flex-none"><Message id="tenant.billingLedger" /></TabsTrigger>
      </TabsList>
      <TabsContent value="packages" className="pt-2"><PackagesSection /></TabsContent>
      <TabsContent value="prices" className="pt-2"><PricesSection /></TabsContent>
      <TabsContent value="tenants" className="pt-2"><TenantsSection /></TabsContent>
      <TabsContent value="orders" className="pt-2"><FilteredHistory kind="orders" /></TabsContent>
      <TabsContent value="ledger" className="pt-2"><FilteredHistory kind="ledger" /></TabsContent>
    </Tabs>
  </div>;
}

/** FilteredHistory is the cross-tenant order or ledger list with a tenant
 * filter.
 *
 * FilteredHistory 是带租户筛选的跨租户订单或账目列表。 */
function FilteredHistory({ kind }: { kind: "orders" | "ledger" }) {
  const [tenantId, setTenantId] = React.useState("");
  return <div className="grid gap-3">
    <TenantIdFilter value={tenantId} onChange={setTenantId} />
    {kind === "orders" ? <OrdersTable surface="operator" tenantId={tenantId} /> : <LedgerTable surface="operator" tenantId={tenantId} />}
  </div>;
}

/** PackagesSection lists every package, listed or not, and edits them.
 *
 * PackagesSection 列出全部充值包（无论是否上架）并编辑它们。 */
function PackagesSection() {
  const { t, locale } = useI18n();
  const packages = useResource<CreditPackage[]>({ method: "GET", surface: "operator", path: "/operator/v1/billing/packages", parse: parseCreditPackages });
  const [dialog, setDialog] = React.useState<{ editing: CreditPackage | null; nonce: number } | null>(null);
  const nonce = React.useRef(0);
  const open = (editing: CreditPackage | null) => { nonce.current += 1; setDialog({ editing, nonce: nonce.current }); };

  if (packages.errorKind === "not_found") return <BillingDisabled />;
  return <Card>
    <CardHeader className="flex flex-wrap items-center justify-between gap-3">
      <CardTitle><Message id="tenant.billingPackages" /></CardTitle>
      <Button onClick={() => open(null)}><Message id="operator.billingNewPackage" /></Button>
    </CardHeader>
    <CardContent className="grid gap-3">
      <p className="text-xs text-muted-foreground"><Message id="operator.billingPackageFormHint" /></p>
      {packages.error ? <ErrorState message={packages.error} onRetry={packages.reload} /> : packages.loading || !packages.data ? <LoadingState label={t("operator.loading")} rows={3} /> : packages.data.length === 0 ? <EmptyState title={t("operator.billingNoPackages")} /> : <Table>
        <TableHeader><TableRow>
          <TableHead><Message id="tenant.name" /></TableHead>
          <TableHead><Message id="tenant.billingKind" /></TableHead>
          <TableHead><Message id="tenant.details" /></TableHead>
          <TableHead><Message id="operator.billingDodoProductId" /></TableHead>
          <TableHead><Message id="tenant.status" /></TableHead>
          <TableHead className="text-right">#</TableHead>
          <TableHead><span className="sr-only"><Message id="common.edit" /></span></TableHead>
        </TableRow></TableHeader>
        <TableBody>{packages.data.map((pkg) => <TableRow key={pkg.id}>
          <TableCell className="max-w-56"><span className="grid gap-0.5"><span className="break-all">{pkg.name}</span><code className="text-xs text-muted-foreground break-all">{pkg.id}</code></span></TableCell>
          <TableCell className="whitespace-nowrap">{pkg.kind === "fixed" ? t("operator.billingKindFixed") : t("operator.billingKindCustom")}</TableCell>
          <TableCell className="min-w-40">{pkg.kind === "fixed"
            ? <span>{t("tenant.billingCreditsValue", { credits: formatCredits(pkg.creditsMicro, locale) })} · {formatMoney(pkg.priceMinor, pkg.currency, locale)}</span>
            : <span>{t("operator.billingPackageRate", { credits: formatCredits(pkg.creditsPerMinorUnitMicro, locale), min: formatMoney(pkg.minAmountMinor, pkg.currency, locale), max: formatMoney(pkg.maxAmountMinor, pkg.currency, locale) })}</span>}</TableCell>
          <TableCell><code className="text-xs break-all">{pkg.dodoProductId}</code></TableCell>
          <TableCell><Badge variant={pkg.active ? "outline" : "secondary"}>{pkg.active ? t("operator.billingListed") : t("operator.billingUnlisted")}</Badge></TableCell>
          <TableCell className="text-right font-mono">{pkg.sortOrder}</TableCell>
          <TableCell><Button size="sm" variant="outline" onClick={() => open(pkg)} aria-label={t("operator.billingEditPackage", { name: pkg.name })}><Message id="common.edit" /></Button></TableCell>
        </TableRow>)}</TableBody>
      </Table>}
    </CardContent>
    {dialog ? <PackageDialog key={dialog.nonce} editing={dialog.editing} onClose={() => setDialog(null)} onSaved={packages.reload} /> : null}
  </Card>;
}

/**
 * PackageDialog creates or replaces a package. Switching the kind swaps the
 * fields shown; the hidden kind's fields are sent as zero, as the control
 * plane requires.
 *
 * PackageDialog 创建或整体替换一个充值包。切换类型会切换展示的字段；被隐藏那种类型
 * 的字段以零发送，这是控制面的要求。
 */
function PackageDialog({ editing, onClose, onSaved }: { editing: CreditPackage | null; onClose: () => void; onSaved: () => void }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const [draft, setDraft] = React.useState<PackageDraft>(() => (editing ? packageDraft(editing) : emptyPackageDraft()));
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const set = (patch: Partial<PackageDraft>) => setDraft((previous) => ({ ...previous, ...patch }));

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const built = packageRequest(draft);
    if (built.problem !== null) {
      setError(<Message id={PACKAGE_PROBLEMS[built.problem]} />);
      return;
    }
    setPending(true);
    setError(null);
    try {
      await run(editing
        ? { method: "PUT", surface: "operator", path: `/operator/v1/billing/packages/${encodeURIComponent(editing.id)}`, body: built.request, parse: parseCreditPackage }
        : { method: "POST", surface: "operator", path: "/operator/v1/billing/packages", body: built.request, parse: parseCreditPackage });
      toast.success(<Message id="operator.billingPackageSaved" />);
      onSaved();
      onClose();
    } catch (failure) {
      setError(<ErrorMessage error={failure} />);
      setPending(false);
    }
  }

  const field = (id: string, label: React.ReactNode, input: React.ReactNode) => <div className="grid gap-2">
    <label htmlFor={id} className="text-sm font-medium">{label}</label>{input}
  </div>;

  return <Dialog open onOpenChange={(next) => { if (!next && !pending) onClose(); }}>
    <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
      <form onSubmit={submit} className="grid gap-4" noValidate>
        <DialogHeader>
          <DialogTitle>{editing ? t("operator.billingEditPackage", { name: editing.name }) : t("operator.billingNewPackage")}</DialogTitle>
          <DialogDescription><Message id="operator.billingPackageFormHint" /></DialogDescription>
        </DialogHeader>
        {field("package-name", <Message id="tenant.name" />, <Input id="package-name" value={draft.name} onChange={(e) => set({ name: e.target.value })} disabled={pending} />)}
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="grid gap-2">
            <label htmlFor="package-kind" className="text-sm font-medium"><Message id="tenant.billingKind" /></label>
            <Select value={draft.kind} onValueChange={(next) => { if (next !== null) set({ kind: next as PackageKind }); }} disabled={pending}>
              <SelectTrigger id="package-kind" className="w-full"><SelectValue>{draft.kind === "fixed" ? t("operator.billingKindFixed") : t("operator.billingKindCustom")}</SelectValue></SelectTrigger>
              <SelectContent>
                <SelectItem value="fixed"><Message id="operator.billingKindFixed" /></SelectItem>
                <SelectItem value="custom"><Message id="operator.billingKindCustom" /></SelectItem>
              </SelectContent>
            </Select>
          </div>
          {field("package-currency", <Message id="operator.billingCurrency" />, <Input id="package-currency" value={draft.currency} maxLength={3} autoCapitalize="characters" onChange={(e) => set({ currency: e.target.value.toUpperCase() })} disabled={pending} />)}
        </div>
        {field("package-product", <Message id="operator.billingDodoProductId" />, <Input id="package-product" value={draft.dodoProductId} onChange={(e) => set({ dodoProductId: e.target.value })} disabled={pending} autoComplete="off" spellCheck={false} />)}
        {draft.kind === "fixed" ? <div className="grid gap-4 sm:grid-cols-2">
          {field("package-credits", <Message id="operator.billingCreditsInput" />, <Input id="package-credits" inputMode="decimal" value={draft.credits} onChange={(e) => set({ credits: e.target.value })} disabled={pending} />)}
          {field("package-price", <Message id="operator.billingPriceInput" />, <Input id="package-price" inputMode="decimal" value={draft.price} onChange={(e) => set({ price: e.target.value })} disabled={pending} />)}
        </div> : <div className="grid gap-4 sm:grid-cols-2">
          <div className="sm:col-span-2">{field("package-rate", <Message id="operator.billingRateInput" />, <Input id="package-rate" inputMode="decimal" value={draft.creditsPerMinorUnit} onChange={(e) => set({ creditsPerMinorUnit: e.target.value })} disabled={pending} />)}</div>
          {field("package-min", <Message id="operator.billingMinAmount" />, <Input id="package-min" inputMode="decimal" value={draft.minAmount} onChange={(e) => set({ minAmount: e.target.value })} disabled={pending} />)}
          {field("package-max", <Message id="operator.billingMaxAmount" />, <Input id="package-max" inputMode="decimal" value={draft.maxAmount} onChange={(e) => set({ maxAmount: e.target.value })} disabled={pending} />)}
        </div>}
        <div className="grid gap-4 sm:grid-cols-2">
          {field("package-sort", <Message id="operator.billingSortOrder" />, <Input id="package-sort" inputMode="numeric" value={draft.sortOrder} onChange={(e) => set({ sortOrder: e.target.value })} disabled={pending} />)}
          <label className="flex items-center gap-2 self-end pb-2 text-sm font-medium">
            <input type="checkbox" checked={draft.active} onChange={(e) => set({ active: e.target.checked })} disabled={pending} />
            <Message id="operator.billingListed" />
          </label>
        </div>
        <FormError message={error} />
        <DialogFooter>
          <Button type="button" variant="outline" disabled={pending} onClick={onClose}><Message id="common.cancel" /></Button>
          <SubmitButton pending={pending}>{editing ? t("common.save") : t("common.create")}</SubmitButton>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>;
}

/** PricesSection lists model prices and sets or removes them. The alias goes
 * in the body (or, for delete, the query) because aliases may contain "/".
 *
 * PricesSection 列出模型单价并设置或删除它们。别名放在请求体（删除时放在查询参数）
 * 里，因为别名可能包含 "/"。 */
function PricesSection() {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const prices = useResource<ModelPrice[]>({ method: "GET", surface: "operator", path: "/operator/v1/billing/model-prices", parse: parseModelPrices });
  const [editor, setEditor] = React.useState<{ price: ModelPrice | null; nonce: number } | null>(null);
  const [removing, setRemoving] = React.useState<ModelPrice | null>(null);
  const nonce = React.useRef(0);
  const open = (price: ModelPrice | null) => { nonce.current += 1; setEditor({ price, nonce: nonce.current }); };

  if (prices.errorKind === "not_found") return <BillingDisabled />;
  return <Card>
    <CardHeader className="flex flex-wrap items-center justify-between gap-3">
      <CardTitle><Message id="operator.billingPrices" /></CardTitle>
      <Button onClick={() => open(null)}><Message id="operator.billingSetPrice" /></Button>
    </CardHeader>
    <CardContent className="grid gap-3">
      <p className="text-xs text-muted-foreground"><Message id="operator.billingPriceHint" /></p>
      {prices.error ? <ErrorState message={prices.error} onRetry={prices.reload} /> : prices.loading || !prices.data ? <LoadingState label={t("operator.loading")} rows={3} /> : prices.data.length === 0 ? <EmptyState title={t("operator.billingNoPrices")} /> : <Table>
        <TableHeader><TableRow>
          <TableHead><Message id="operator.billingModel" /></TableHead>
          <TableHead className="text-right"><Message id="operator.billingInputPrice" /></TableHead>
          <TableHead className="text-right"><Message id="operator.billingOutputPrice" /></TableHead>
          <TableHead><Message id="tenant.time" /></TableHead>
          <TableHead><span className="sr-only"><Message id="common.edit" /></span></TableHead>
        </TableRow></TableHeader>
        <TableBody>{prices.data.map((price) => <TableRow key={price.model}>
          <TableCell className="max-w-64"><span className="flex flex-wrap items-center gap-2"><code className="text-xs break-all">{price.model}</code>{price.model === DEFAULT_PRICE_MODEL ? <Badge variant="secondary"><Message id="operator.billingDefaultPrice" /></Badge> : null}</span></TableCell>
          <TableCell className="text-right font-mono">{formatCredits(price.inputMicroPerMTok, locale)}</TableCell>
          <TableCell className="text-right font-mono">{formatCredits(price.outputMicroPerMTok, locale)}</TableCell>
          <TableCell className="text-xs text-muted-foreground"><span className="break-all">{t("operator.billingUpdated", { actor: price.updatedBy || "—", time: formatDateTime(price.updatedAt, undefined, locale) })}</span></TableCell>
          <TableCell><span className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={() => open(price)} aria-label={t("operator.billingEditPrice", { model: price.model })}><Message id="common.edit" /></Button>
            <Button size="sm" variant="ghost" onClick={() => setRemoving(price)}><Message id="common.delete" /></Button>
          </span></TableCell>
        </TableRow>)}</TableBody>
      </Table>}
    </CardContent>
    {editor ? <PriceDialog key={editor.nonce} price={editor.price} onClose={() => setEditor(null)} onSaved={prices.reload} /> : null}
    <ConfirmDialog
      open={removing !== null}
      onOpenChange={(next) => { if (!next) setRemoving(null); }}
      title={t("common.delete")}
      destructive
      confirmLabel={t("common.delete")}
      description={removing ? t("operator.billingDeletePriceConfirm", { model: removing.model }) : ""}
      onConfirm={async () => {
        if (!removing) return;
        await run({ method: "DELETE", surface: "operator", path: "/operator/v1/billing/model-prices", query: { model: removing.model }, parse: parseNoContent });
        toast.success(<Message id="operator.billingPriceDeleted" />);
        setRemoving(null);
        prices.reload();
      }}
    />
  </Card>;
}

/** PriceDialog sets one alias's price, typed in credits per million tokens.
 *
 * PriceDialog 设置一个别名的单价，以「点/百万 token」输入。 */
function PriceDialog({ price, onClose, onSaved }: { price: ModelPrice | null; onClose: () => void; onSaved: () => void }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const [model, setModel] = React.useState(price?.model ?? "");
  const [input, setInput] = React.useState(price ? formatScaled(price.inputMicroPerMTok, 6) : "");
  const [output, setOutput] = React.useState(price ? formatScaled(price.outputMicroPerMTok, 6) : "");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const built = modelPriceRequest({ model, input, output });
    if (built.problem !== null) {
      setError(<Message id={built.problem === "model" ? "operator.billingProblemModel" : "operator.billingProblemPriceValue"} />);
      return;
    }
    setPending(true);
    setError(null);
    try {
      await run({ method: "PUT", surface: "operator", path: "/operator/v1/billing/model-prices", body: built.request, parse: parseModelPrice });
      toast.success(<Message id="operator.billingPriceSaved" />);
      onSaved();
      onClose();
    } catch (failure) {
      setError(<ErrorMessage error={failure} />);
      setPending(false);
    }
  }

  return <Dialog open onOpenChange={(next) => { if (!next && !pending) onClose(); }}>
    <DialogContent className="sm:max-w-md">
      <form onSubmit={submit} className="grid gap-4" noValidate>
        <DialogHeader>
          <DialogTitle>{price ? t("operator.billingEditPrice", { model: price.model }) : t("operator.billingSetPrice")}</DialogTitle>
          <DialogDescription><Message id="operator.billingPriceHint" /></DialogDescription>
        </DialogHeader>
        <div className="grid gap-2">
          <label htmlFor="price-model" className="text-sm font-medium"><Message id="operator.billingModel" /></label>
          <Input id="price-model" value={model} onChange={(e) => setModel(e.target.value)} disabled={pending || price !== null} placeholder={DEFAULT_PRICE_MODEL} autoComplete="off" spellCheck={false} />
        </div>
        <div className="grid gap-2">
          <label htmlFor="price-input" className="text-sm font-medium"><Message id="operator.billingInputPrice" /></label>
          <Input id="price-input" inputMode="decimal" value={input} onChange={(e) => setInput(e.target.value)} disabled={pending} />
        </div>
        <div className="grid gap-2">
          <label htmlFor="price-output" className="text-sm font-medium"><Message id="operator.billingOutputPrice" /></label>
          <Input id="price-output" inputMode="decimal" value={output} onChange={(e) => setOutput(e.target.value)} disabled={pending} />
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

/** TenantsSection lists tenants with their billing state, switches modes and
 * adjusts balances.
 *
 * TenantsSection 列出租户及其计费状态，切换计费模式并调整余额。 */
function TenantsSection() {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [query, setQuery] = React.useState("");
  const tenants = usePagedResource<BillingTenant>({ path: "/operator/v1/billing/tenants", surface: "operator", filters: { q: query }, pageSize: TENANT_PAGE_SIZE, parse: parseBillingTenants });
  const [switching, setSwitching] = React.useState<{ tenant: BillingTenant; mode: BillingMode } | null>(null);
  const [adjusting, setAdjusting] = React.useState<BillingTenant | null>(null);

  if (tenants.errorKind === "not_found") return <BillingDisabled />;
  return <Card>
    <CardHeader><CardTitle><Message id="operator.billingTenants" /></CardTitle></CardHeader>
    <CardContent className="grid gap-3">
      <Input className="max-w-xs" aria-label={t("operator.searchTenants")} placeholder={t("operator.searchTenants")} value={query} onChange={(e) => setQuery(e.target.value)} />
      {tenants.error ? <ErrorState message={tenants.error} onRetry={tenants.reload} /> : tenants.loading || !tenants.items ? <LoadingState label={t("operator.loadingTenants")} rows={3} /> : tenants.items.length === 0 ? <EmptyState title={t("operator.noTenants")} /> : <>
        <Table>
          <TableHeader><TableRow>
            <TableHead><Message id="tenant.tenant" /></TableHead>
            <TableHead><Message id="tenant.billingMode" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.billingBalance" /></TableHead>
            <TableHead><span className="sr-only"><Message id="tenant.action" /></span></TableHead>
          </TableRow></TableHeader>
          <TableBody>{tenants.items.map((tenant) => {
            const next: BillingMode = tenant.mode === "exempt" ? "prepaid" : "exempt";
            return <TableRow key={tenant.id}>
              <TableCell className="max-w-64"><span className="grid gap-0.5"><span className="flex flex-wrap items-center gap-2 break-all">{tenant.name || tenant.id}{tenant.status !== "active" ? <Badge variant="destructive">{tenant.status}</Badge> : null}</span><code className="text-xs text-muted-foreground break-all">{tenant.id}</code></span></TableCell>
              <TableCell><span className="flex flex-wrap gap-1"><Badge variant={tenant.mode === "exempt" ? "secondary" : "outline"}>{modeLabel(tenant.mode, t)}</Badge>{tenant.blocked && tenant.mode === "prepaid" ? <Badge variant="destructive"><Message id="operator.billingBlocked" /></Badge> : null}</span></TableCell>
              <TableCell className="text-right font-mono whitespace-nowrap">{formatCredits(tenant.balanceMicro, locale)}</TableCell>
              <TableCell><span className="flex flex-wrap gap-2">
                <Button size="sm" variant="outline" onClick={() => setSwitching({ tenant, mode: next })}>{next === "exempt" ? t("operator.billingMakeExempt") : t("operator.billingMakePrepaid")}</Button>
                <Button size="sm" variant="outline" onClick={() => setAdjusting(tenant)}><Message id="operator.billingAdjust" /></Button>
              </span></TableCell>
            </TableRow>;
          })}</TableBody>
        </Table>
        <Pager resource={tenants} loadedCount={tenants.items.length} />
      </>}
    </CardContent>
    <ConfirmDialog
      open={switching !== null}
      onOpenChange={(open) => { if (!open) setSwitching(null); }}
      title={t("tenant.billingMode")}
      confirmLabel={switching ? (switching.mode === "exempt" ? t("operator.billingMakeExempt") : t("operator.billingMakePrepaid")) : undefined}
      description={switching ? t("operator.billingModeConfirm", { name: switching.tenant.name || switching.tenant.id, mode: modeLabel(switching.mode, t) }) : ""}
      onConfirm={async () => {
        if (!switching) return;
        await run({ method: "PUT", surface: "operator", path: `/operator/v1/billing/tenants/${encodeURIComponent(switching.tenant.id)}/mode`, body: { mode: switching.mode }, parse: parseNoContent });
        toast.success(<Message id="operator.billingModeSaved" />);
        setSwitching(null);
        tenants.reload();
      }}
    />
    {adjusting ? <AdjustDialog key={adjusting.id} tenant={adjusting} onClose={() => setAdjusting(null)} onSaved={tenants.reload} /> : null}
  </Card>;
}

/** AdjustDialog adds or deducts credits with a reason that lands on the
 * ledger. It is sent once: a repeat would apply the adjustment twice.
 *
 * AdjustDialog 增加或扣减点数，并附带记入账目的原因。它只发送一次：重发会让调整
 * 生效两次。 */
function AdjustDialog({ tenant, onClose, onSaved }: { tenant: BillingTenant; onClose: () => void; onSaved: () => void }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [amount, setAmount] = React.useState("");
  const [reason, setReason] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const name = tenant.name || tenant.id;

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const built = adjustRequest({ amount, reason });
    if (built.problem !== null) {
      setError(<Message id={built.problem === "amount" ? "operator.billingProblemAdjustAmount" : "operator.billingProblemReason"} />);
      return;
    }
    setPending(true);
    setError(null);
    try {
      const entry = await run({ method: "POST", surface: "operator", path: `/operator/v1/billing/tenants/${encodeURIComponent(tenant.id)}/adjust`, body: built.request, parse: parseLedgerEntry });
      toast.success(<Message id="operator.billingAdjusted" values={{ balance: formatCredits(entry.balanceAfterMicro, locale) }} />);
      onSaved();
      onClose();
    } catch (failure) {
      // The result of a failed write is unknown, so the list is re-read
      // rather than assumed unchanged.
      //
      // 失败写操作的结果未知，因此重新读取列表，而不是假定它没变。
      setError(<ErrorMessage error={failure} />);
      setPending(false);
      onSaved();
    }
  }

  return <Dialog open onOpenChange={(next) => { if (!next && !pending) onClose(); }}>
    <DialogContent className="sm:max-w-md">
      <form onSubmit={submit} className="grid gap-4" noValidate>
        <DialogHeader>
          <DialogTitle className="break-all">{t("operator.billingAdjustTitle", { name })}</DialogTitle>
          <DialogDescription>{t("tenant.billingBalance")}: {t("tenant.billingCreditsValue", { credits: formatCredits(tenant.balanceMicro, locale) })}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-2">
          <label htmlFor="adjust-amount" className="text-sm font-medium"><Message id="operator.billingAdjustAmount" /></label>
          <Input id="adjust-amount" inputMode="decimal" value={amount} onChange={(e) => setAmount(e.target.value)} disabled={pending} />
        </div>
        <div className="grid gap-2">
          <label htmlFor="adjust-reason" className="text-sm font-medium"><Message id="operator.billingAdjustReason" /></label>
          <Input id="adjust-reason" value={reason} maxLength={128} onChange={(e) => setReason(e.target.value)} disabled={pending} />
        </div>
        <FormError message={error} />
        <DialogFooter>
          <Button type="button" variant="outline" disabled={pending} onClick={onClose}><Message id="common.cancel" /></Button>
          <SubmitButton pending={pending}><Message id="operator.billingAdjust" /></SubmitButton>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>;
}

"use client";

import * as React from "react";
import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import { ErrorMessage } from "@/components/i18n/error-message";
import { useConsoleRequest } from "./use-console-request";
import { useResource } from "./use-resource";
import { ConfirmDialog } from "./confirm-dialog";
import { EmptyState, ErrorState, FormError, LoadingState } from "./states";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ACCOUNT_KINDS, parseAccountSales, parseAccountSale, accountCheckoutBody, accountKindLabel, accountPurchasable, accountRequest, type AccountKind, type AccountSale, type AccountDraftProblem } from "@/lib/console/account-sales";
import { formatMoney, formatScaled, currencyDigits, parseCheckout } from "@/lib/console/billing";
import type { MessageKey } from "@/lib/i18n/messages";
import { formatDateTime, matchesQuery } from "@/lib/console/format";
import { parseFleetSnapshot } from "@/lib/console/fleet";
import { tenantNodeOwner } from "@/lib/console/node-access";
import { ApiError } from "@/lib/console/errors";

/** SELECT_CLASS styles the native selects of the account forms.
 *
 * SELECT_CLASS 是账号表单中原生下拉框的样式。 */
export const SELECT_CLASS = "h-10 w-full min-w-0 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50";

const PROBLEM_KEYS: Record<AccountDraftProblem, MessageKey> = {
  name: "tenant.accountProblemName",
  target: "tenant.accountProblemTarget",
  kind: "tenant.accountProblemKind",
  currency: "tenant.accountProblemCurrency",
  price: "tenant.accountProblemPrice",
  product: "tenant.accountProblemProduct",
};

/** AccountSales shares the credential-free monthly inventory between
 * surfaces. A tenant buys a month through the provider's checkout, priced in
 * fiat; the page never decides that a payment succeeded — the month appears
 * once the server has the provider's confirmation.
 *
 * AccountSales 在两个界面间共享不含凭据的包月库存。商户经服务商结账以法币购买一个月；
 * 页面从不判定付款成功——服务端收到服务商确认后才会显示期限。 */
export function AccountSales({ operator = false, canPurchase = false }: { operator?: boolean; canPurchase?: boolean }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const surface = operator ? "operator" : "admin";
  const path = `/${surface}/v1/billing/accounts`;
  const resource = useResource({ method: "GET", surface, path, parse: parseAccountSales });
  const [editing, setEditing] = React.useState<AccountSale | "new" | null>(null);
  const [buying, setBuying] = React.useState<AccountSale | null>(null);
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(0);
  const rows = (resource.data ?? []).filter(row => matchesQuery([row.name, row.nodeId, row.runtimeId, row.tenantId], query));
  const current = Math.min(page, Math.max(0, Math.ceil(rows.length / 12) - 1));
  return <Card>
    <CardHeader><CardTitle><Message id="tenant.accountMonthly" /></CardTitle></CardHeader>
    <CardContent className="grid gap-4">
      <p className="text-sm text-muted-foreground"><Message id={operator ? "tenant.accountOperatorNote" : "tenant.accountMonthlyNote"} /></p>
      <div className="flex flex-wrap gap-3">
        <Input className="max-w-sm" aria-label={t("tenant.accountSearch")} placeholder={t("tenant.accountSearch")} value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} />
        <Button variant="outline" disabled={resource.loading} onClick={resource.reload}><Message id="tenant.accountRefresh" /></Button>
        {operator ? <Button disabled={resource.loading || resource.error !== null} onClick={() => setEditing("new")}><Message id="tenant.accountAdd" /></Button> : null}
      </div>
      {resource.errorKind === "not_found" ? <EmptyState title={t("tenant.accountDisabled")} description={operator ? t("tenant.accountEnableHint") : undefined} /> : resource.error ? <ErrorState message={resource.error} onRetry={resource.reload} /> : resource.loading ? <LoadingState label={t("tenant.accountMonthly")} rows={3} /> : rows.length === 0 ? <EmptyState title={t("tenant.accountEmpty")} /> : <>
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">{rows.slice(current * 12, current * 12 + 12).map(account => <div key={account.id} className="grid content-between gap-3 rounded-lg border p-4">
          <div className="grid gap-2">
            <p className="font-medium break-all">{account.name}</p>
            <code className="break-all text-xs text-muted-foreground">{account.nodeId} / {account.runtimeId}</code>
            <p className="text-lg font-semibold">{account.priceMinor > 0 ? t("tenant.accountMonthlyPrice", { price: formatMoney(account.priceMinor, account.currency, locale) }) : t("tenant.accountPriceMissing")}</p>
            <div className="flex flex-wrap gap-2">
              {account.kind ? <Badge variant="secondary">{accountKindLabel(account.kind)}</Badge> : null}
              {account.owned ? <Badge><Message id="tenant.accountOwned" /></Badge> : null}
              {!account.active ? <Badge variant="outline"><Message id="tenant.accountArchived" /></Badge> : null}
            </div>
            {account.expiresAt ? <p className="text-sm">{t("tenant.accountExpires", { time: formatDateTime(account.expiresAt, undefined, locale) })}</p> : null}
            {operator && account.tenantId ? <p className="text-xs break-all">{t("tenant.accountHolder", { tenant: account.tenantId })}</p> : null}
            {operator && account.dodoProductId ? <code className="text-xs break-all text-muted-foreground">{account.dodoProductId}</code> : null}
          </div>
          {operator ? <Button variant="outline" onClick={() => setEditing(account)}><Message id="tenant.accountEdit" /></Button> : canPurchase ?
            <Button disabled={!accountPurchasable(account)} onClick={() => setBuying(account)}><Message id={account.owned ? "tenant.accountRenew" : "tenant.accountBuy"} /></Button> : null}
        </div>)}</div>
        <div className="flex items-center justify-between gap-3">
          <Button variant="outline" disabled={current === 0} onClick={() => setPage(current - 1)}><Message id="tenant.accountPrevious" /></Button>
          <span className="text-sm">{current + 1} / {Math.ceil(rows.length / 12)}</span>
          <Button variant="outline" disabled={(current + 1) * 12 >= rows.length} onClick={() => setPage(current + 1)}><Message id="tenant.accountNext" /></Button>
        </div>
      </>}
    </CardContent>
    <ConfirmDialog open={buying !== null} onOpenChange={open => { if (!open) setBuying(null); }} title={t("tenant.accountPurchase")}
      description={buying ? t("tenant.accountBuyConfirm", { name: buying.name, price: formatMoney(buying.priceMinor, buying.currency, locale) }) : ""}
      onConfirm={async () => {
        if (!buying) return;
        let result;
        try {
          // A checkout is a write and is never retried: a repeat would open a
          // second pending order. 结账是写操作，绝不重试：重发会再开一笔待付款订单。
          result = await run({ method: "POST", path: `${path}/${encodeURIComponent(buying.id)}/checkout`, body: accountCheckoutBody(buying), parse: parseCheckout });
        } catch (error) {
          if (error instanceof ApiError && error.status === 409) throw new ApiError("account_unavailable", 409);
          if (error instanceof ApiError && error.status === 404) throw new ApiError("account_checkout_disabled", 404);
          if (error instanceof ApiError && error.status === 503) throw new ApiError("payment_unavailable", 503);
          throw error;
        }
        window.location.assign(result.checkoutUrl);
      }} />
    {editing ? <AccountEditor key={typeof editing === "string" ? editing : editing.id} account={editing === "new" ? null : editing} inventory={resource.data ?? []} onClose={() => setEditing(null)} onSaved={resource.reload} /> : null}
  </Card>;
}

function AccountEditor({ account, inventory, onClose, onSaved }: { account: AccountSale | null; inventory: AccountSale[]; onClose: () => void; onSaved: () => void }) {
  const run = useConsoleRequest();
  const [name, setName] = React.useState(account?.name ?? "");
  const [autoName, setAutoName] = React.useState("");
  const [node, setNode] = React.useState(account?.nodeId ?? "");
  const [runtime, setRuntime] = React.useState(account?.runtimeId ?? "");
  const [kind, setKind] = React.useState<AccountKind | "">(account?.kind ?? "");
  const [currency, setCurrency] = React.useState(account?.currency || "USD");
  const [rawPrice, setPrice] = React.useState(account && account.priceMinor > 0 ? formatScaled(account.priceMinor, currencyDigits(account.currency)) : "");
  const [productId, setProductId] = React.useState(account?.dodoProductId ?? "");
  const [active, setActive] = React.useState(account ? account.active && account.priceMinor > 0 : true);
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const built = accountRequest({ name, nodeId: node, runtimeId: runtime, kind, kindRequired: account === null, price: rawPrice, currency, productId, active });
  const touched = rawPrice.trim() !== "" || productId.trim() !== "";
  const prefix = React.useId();
  return <Card className="m-4">
    <CardHeader><CardTitle><Message id={account ? "tenant.accountEdit" : "tenant.accountAdd"} /></CardTitle></CardHeader>
    <CardContent><form className="grid gap-3" onSubmit={async event => {
      event.preventDefault();
      if (!built.request) return;
      setPending(true); setError(null);
      try {
        await run({ method: account ? "PUT" : "POST", surface: "operator", path: `/operator/v1/billing/accounts${account ? `/${encodeURIComponent(account.id)}` : ""}`,
          body: built.request, parse: parseAccountSale });
        onSaved(); onClose();
      } catch (failure) { setError(failure); } finally { setPending(false); }
    }}>
      <label htmlFor={`${prefix}-name`}><Message id="tenant.accountName" /></label><Input id={`${prefix}-name`} required maxLength={128} value={name} disabled={pending} onChange={e => setName(e.target.value)} />
      {account ? <>
        <label htmlFor={`${prefix}-node`}><Message id="tenant.accountNode" /></label><Input id={`${prefix}-node`} value={node} disabled />
        <label htmlFor={`${prefix}-runtime`}><Message id="tenant.accountRuntime" /></label><Input id={`${prefix}-runtime`} value={runtime} disabled />
        {account.kind ? <>
          <label htmlFor={`${prefix}-kind`}><Message id="tenant.accountKind" /></label><Input id={`${prefix}-kind`} value={accountKindLabel(account.kind)} disabled />
        </> : <>
          {/* A legacy row may have its kind filled in once; it never changes after.
              旧行可以补填一次类型，之后不再更改。 */}
          <label htmlFor={`${prefix}-kind`}><Message id="tenant.accountKind" /></label>
          <select id={`${prefix}-kind`} className={SELECT_CLASS} value={kind} disabled={pending} onChange={event => setKind(event.target.value as AccountKind | "")}>
            <option value="">—</option>
            {ACCOUNT_KINDS.map(value => <option key={value} value={value}>{accountKindLabel(value)}</option>)}
          </select>
          <p className="text-sm text-muted-foreground"><Message id="tenant.accountKindMissing" /></p>
        </>}
      </> : <AccountTargetPicker inventory={inventory} nodeId={node} runtimeId={runtime} disabled={pending} onSelect={(nextNode, nextRuntime, nextName, nextKind) => {
        setNode(nextNode); setRuntime(nextRuntime); setKind(nextKind);
        if (nextName) {
          setName(previous => previous === "" || previous === autoName ? nextName : previous);
          setAutoName(nextName);
        }
      }} />}
      <label htmlFor={`${prefix}-currency`}><Message id="tenant.accountCurrency" /></label><Input id={`${prefix}-currency`} required maxLength={3} autoCapitalize="characters" value={currency} disabled={pending} onChange={e => setCurrency(e.target.value.toUpperCase())} />
      <label htmlFor={`${prefix}-price`}><Message id="tenant.accountPrice" /></label><Input id={`${prefix}-price`} required={active} inputMode="decimal" value={rawPrice} disabled={pending} onChange={e => setPrice(e.target.value)} />
      <label htmlFor={`${prefix}-product`}><Message id="tenant.accountProductId" /></label><Input id={`${prefix}-product`} required={active} maxLength={64} value={productId} disabled={pending} onChange={e => setProductId(e.target.value)} />
      <p className="text-sm text-muted-foreground"><Message id="tenant.accountProductHint" /></p>
      <p className="text-sm text-muted-foreground"><Message id="tenant.accountUnpricedHint" /></p>
      <label className="flex items-center gap-2"><input type="checkbox" checked={active} disabled={pending} onChange={e => setActive(e.target.checked)} /><Message id="tenant.accountListed" /></label>
      {touched && built.problem ? <p role="status" className="text-sm text-muted-foreground"><Message id={PROBLEM_KEYS[built.problem]} /></p> : null}
      <FormError message={error === null ? null : <ErrorMessage error={error} />} />
      <div className="flex gap-3"><Button type="submit" disabled={pending || built.request === null}><Message id="tenant.accountSave" /></Button><Button type="button" variant="outline" disabled={pending} onClick={onClose}><Message id="common.cancel" /></Button></div>
    </form></CardContent>
  </Card>;
}

/** Only Agent-reported CLI accounts can be selected when creating an offer. */
function AccountTargetPicker({ inventory, nodeId, runtimeId, disabled, onSelect }: {
  inventory: AccountSale[]; nodeId: string; runtimeId: string; disabled: boolean;
  onSelect: (nodeId: string, runtimeId: string, name: string, kind: AccountKind | "") => void;
}) {
  const { t } = useI18n();
  const prefix = React.useId();
  const fleet = useResource({ method: "GET", surface: "operator", path: "/operator/v1/nodes", parse: parseFleetSnapshot });
  const nodes = (fleet.data?.nodes ?? []).filter(node => tenantNodeOwner(node.nodeId) === null).map(node => ({
    ...node, runtimes: node.runtimes.filter(runtime => (runtime.kind === "claude" || runtime.kind === "codex") &&
      !inventory.some(account => account.nodeId === node.nodeId && account.runtimeId === runtime.id)),
  })).filter(node => node.runtimes.length > 0);
  const selected = nodes.find(node => node.nodeId === nodeId);
  const label = (runtime: { kind: string; id: string }) => `${runtime.kind === "claude" ? "Claude Code" : "Codex"} · ${runtime.id}`;
  const selectClass = SELECT_CLASS;
  const refresh = () => { onSelect("", "", "", ""); fleet.reload(); };
  const unavailable = disabled || fleet.loading || fleet.error !== null;
  return <div className="grid min-w-0 gap-3">
    <Button type="button" variant="outline" className="justify-self-start" disabled={disabled || fleet.loading} onClick={refresh}><Message id="common.refresh" /></Button>
    {fleet.loading ? <LoadingState label={t("tenant.accountSelectAgent")} rows={1} /> : null}
    {fleet.error ? <ErrorState message={fleet.error} onRetry={refresh} /> : null}
    {fleet.data?.partial ? <p role="status" className="text-sm text-muted-foreground"><Message id="tenant.accountFleetPartial" /></p> : null}
    {fleet.data && nodes.length === 0 ? <EmptyState title={t("tenant.accountNoTargets")} description={t("tenant.accountConnectHint")} /> : null}
    <label htmlFor={`${prefix}-agent`}><Message id="tenant.accountSelectAgent" /></label>
    <select id={`${prefix}-agent`} className={selectClass} required disabled={unavailable || nodes.length === 0} value={nodeId} onChange={event => onSelect(event.target.value, "", "", "")}>
      <option value=""><Message id="tenant.accountSelectAgent" /></option>
      {nodes.map(node => <option key={node.nodeId} value={node.nodeId}>{node.nodeId}{node.live ? "" : ` (${t("tenant.accountOffline")})`}</option>)}
    </select>
    <label htmlFor={`${prefix}-account`}><Message id="tenant.accountSelectRuntime" /></label>
    <select id={`${prefix}-account`} className={selectClass} required disabled={unavailable || !selected} value={runtimeId} onChange={event => {
      const runtime = selected?.runtimes.find(runtime => runtime.id === event.target.value);
      onSelect(nodeId, runtime?.id ?? "", runtime ? label(runtime) : "", runtime ? (runtime.kind as AccountKind) : "");
    }}>
      <option value=""><Message id="tenant.accountSelectRuntime" /></option>
      {selected?.runtimes.map(runtime => <option key={runtime.id} value={runtime.id}>{label(runtime)}</option>)}
    </select>
    <p className="text-sm text-muted-foreground"><Message id="tenant.accountTargetHint" /></p>
  </div>;
}

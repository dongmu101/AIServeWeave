"use client";

import * as React from "react";
import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import { ErrorMessage } from "@/components/i18n/error-message";
import { useConsoleRequest } from "./use-console-request";
import { useResource, type Resource } from "./use-resource";
import { usePagedResource } from "./use-paged-resource";
import { ConfirmDialog } from "./confirm-dialog";
import { SELECT_CLASS } from "./account-sales";
import { CurrencySelect } from "./currency-select";
import { Pager } from "./pager";
import { EmptyState, ErrorState, FormError, LoadingState } from "./states";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ACCOUNT_KINDS, accountKindLabel, parseAccountSales, type AccountKind } from "@/lib/console/account-sales";
import {
  assignableAccounts,
  parseAccountPresale,
  parseAccountPresales,
  parseRefundCreated,
  presaleCheckoutBody,
  presaleRequest,
  refundReason,
  MAX_PRESALE_CAPACITY,
  type AccountPresale,
  type PresaleDraftProblem,
} from "@/lib/console/account-presales";
import { currencyDigits, formatMoney, formatScaled, parseCheckout, parseCheckoutOrder, parseOrderPage, type CheckoutOrder } from "@/lib/console/billing";
import { formatDateTime, observationAge } from "@/lib/console/format";
import { ApiError } from "@/lib/console/errors";
import type { MessageKey } from "@/lib/i18n/messages";

const PROBLEM_KEYS: Record<PresaleDraftProblem, MessageKey> = {
  kind: "tenant.accountProblemKind",
  name: "operator.presaleProblemName",
  currency: "tenant.accountProblemCurrency",
  price: "tenant.accountProblemPrice",
  product: "tenant.accountProblemProduct",
  capacity: "operator.presaleProblemCapacity",
};

/** PresaleSales lists the presales. A tenant pre-orders a month through the
 * provider's checkout and the order then waits for an operator to assign an
 * account; nothing here decides that a payment succeeded. A tenant sees the
 * section only when something is on presale.
 *
 * PresaleSales 列出预售。商户经服务商结账预购一个月，之后订单等待运维分配账号；这里
 * 不判定付款是否成功。只有存在预售商品时，商户才会看到这一区块。 */
export function PresaleSales({ canPurchase = false }: { canPurchase?: boolean }) {
  const resource = useResource({ method: "GET", path: "/admin/v1/billing/presales", parse: parseAccountPresales });
  return <PresaleCatalog resource={resource} canPurchase={canPurchase} />;
}

/** PresaleAdmin is the operator's presale tab: the catalog and the delivery
 * queue over one read of the presales, so a presale saved or an order
 * assigned shows in both.
 *
 * PresaleAdmin 是运维的预售标签页：商品与发货队列共用同一次预售读取，保存预售或分配
 * 订单后两处同时更新。 */
export function PresaleAdmin() {
  const resource = useResource({ method: "GET", surface: "operator", path: "/operator/v1/billing/presales", parse: parseAccountPresales });
  return <div className="grid gap-4">
    <PresaleCatalog resource={resource} operator />
    <PresaleAwaitingOrders presales={resource} />
  </div>;
}

function PresaleCatalog({ resource, operator = false, canPurchase = false }: { resource: Resource<AccountPresale[]>; operator?: boolean; canPurchase?: boolean }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const path = operator ? "/operator/v1/billing/presales" : "/admin/v1/billing/presales";
  const [editing, setEditing] = React.useState<AccountPresale | "new" | null>(null);
  const [buying, setBuying] = React.useState<AccountPresale | null>(null);
  const rows = resource.data ?? [];
  if (!operator && (resource.errorKind === "not_found" || resource.loading || (resource.data !== null && rows.length === 0))) {
    return null;
  }
  return <Card>
    <CardHeader><CardTitle><Message id="tenant.presale" /></CardTitle></CardHeader>
    <CardContent className="grid gap-4">
      <p className="text-sm text-muted-foreground"><Message id={operator ? "tenant.presaleOperatorNote" : "tenant.presaleNote"} /></p>
      {operator ? <div className="flex flex-wrap gap-3">
        <Button variant="outline" disabled={resource.loading} onClick={resource.reload}><Message id="common.refresh" /></Button>
        <Button disabled={resource.loading || resource.error !== null} onClick={() => setEditing("new")}><Message id="operator.presaleAdd" /></Button>
      </div> : null}
      {resource.errorKind === "not_found" ? <EmptyState title={t("tenant.accountDisabled")} description={t("tenant.accountEnableHint")} />
        : resource.error ? <ErrorState message={resource.error} onRetry={resource.reload} />
        : resource.loading ? <LoadingState label={t("tenant.presale")} rows={2} />
        : rows.length === 0 ? <EmptyState title={t("tenant.presaleEmpty")} />
        : <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">{rows.map(presale => <div key={presale.id} className="grid content-between gap-3 rounded-lg border p-4">
          <div className="grid gap-2">
            <div className="flex items-start justify-between gap-2">
              <p className="line-clamp-2 min-w-0 font-medium break-all" title={presale.name}>{presale.name}</p>
              <Badge variant="secondary" className="shrink-0">{accountKindLabel(presale.kind)}</Badge>
            </div>
            <div className="flex flex-wrap gap-2">
              <Badge variant="outline"><Message id="tenant.presaleBadge" /></Badge>
              {!presale.active ? <Badge variant="outline"><Message id="tenant.accountArchived" /></Badge> : null}
              {presale.active && !presale.available ? <Badge variant="destructive"><Message id="tenant.presaleSoldOut" /></Badge> : null}
            </div>
            <p className="text-lg font-semibold">{t("tenant.accountMonthlyPrice", { price: formatMoney(presale.priceMinor, presale.currency, locale) })}</p>
            <p className="text-sm text-muted-foreground"><Message id="tenant.presaleDelivery" /></p>
            {operator && presale.capacity !== null && presale.outstanding !== null ? <p className="text-sm">{t("operator.presaleStock", { outstanding: presale.outstanding, capacity: presale.capacity })}</p> : null}
            {operator && presale.dodoProductId ? <code className="text-xs break-all text-muted-foreground">{presale.dodoProductId}</code> : null}
          </div>
          {operator ? <Button variant="outline" onClick={() => setEditing(presale)}><Message id="operator.presaleEdit" /></Button>
            : canPurchase ? <div><Button disabled={!presale.available} onClick={() => setBuying(presale)}><Message id="tenant.presaleBuy" /></Button></div> : null}
        </div>)}</div>}
    </CardContent>
    <ConfirmDialog open={buying !== null} onOpenChange={open => { if (!open) setBuying(null); }} title={t("tenant.presalePurchase")}
      description={buying ? t("tenant.presaleBuyConfirm", { name: buying.name, price: formatMoney(buying.priceMinor, buying.currency, locale), kind: accountKindLabel(buying.kind) }) : ""}
      onConfirm={async () => {
        if (!buying) return;
        let result;
        try {
          // A checkout is a write and is never retried: a repeat would open a
          // second pending order and take a second place.
          // 结账是写操作，绝不重试：重发会再开一笔待付款订单并再占一个名额。
          result = await run({ method: "POST", path: `${path}/${encodeURIComponent(buying.id)}/checkout`, body: presaleCheckoutBody(buying), parse: parseCheckout });
        } catch (error) {
          if (error instanceof ApiError && error.status === 409) throw new ApiError("presale_unavailable", 409);
          if (error instanceof ApiError && error.status === 404) throw new ApiError("account_checkout_disabled", 404);
          if (error instanceof ApiError && error.status === 503) throw new ApiError("payment_unavailable", 503);
          throw error;
        }
        window.location.assign(result.checkoutUrl);
      }} />
    {editing ? <PresaleEditor key={typeof editing === "string" ? editing : editing.id} presale={editing === "new" ? null : editing} onClose={() => setEditing(null)} onSaved={resource.reload} /> : null}
  </Card>;
}

function PresaleEditor({ presale, onClose, onSaved }: { presale: AccountPresale | null; onClose: () => void; onSaved: () => void }) {
  const run = useConsoleRequest();
  const [kind, setKind] = React.useState<AccountKind | "">(presale?.kind ?? "");
  const [name, setName] = React.useState(presale?.name ?? "");
  const [currency, setCurrency] = React.useState(presale?.currency ?? "USD");
  const [rawPrice, setPrice] = React.useState(presale ? formatScaled(presale.priceMinor, currencyDigits(presale.currency)) : "");
  const [productId, setProductId] = React.useState(presale?.dodoProductId ?? "");
  const [capacity, setCapacity] = React.useState(presale?.capacity ? String(presale.capacity) : "");
  const [active, setActive] = React.useState(presale ? presale.active : true);
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const built = presaleRequest({ kind, name, price: rawPrice, currency, productId, capacity, active });
  const touched = kind !== "" && (name.trim() !== "" || rawPrice.trim() !== "" || productId.trim() !== "" || capacity.trim() !== "");
  const prefix = React.useId();
  return <Card className="m-4">
    <CardHeader><CardTitle><Message id={presale ? "operator.presaleEdit" : "operator.presaleAdd"} /></CardTitle></CardHeader>
    <CardContent><form className="grid gap-3" onSubmit={async event => {
      event.preventDefault();
      if (!built.request) return;
      setPending(true); setError(null);
      try {
        // Saving is a write and is sent once. 保存是写操作，只发送一次。
        await run({ method: presale ? "PUT" : "POST", surface: "operator", path: `/operator/v1/billing/presales${presale ? `/${encodeURIComponent(presale.id)}` : ""}`,
          body: built.request, parse: parseAccountPresale });
        onSaved(); onClose();
      } catch (failure) { setError(failure); } finally { setPending(false); }
    }}>
      <label htmlFor={`${prefix}-kind`}><Message id="tenant.accountKind" /></label>
      <select id={`${prefix}-kind`} className={SELECT_CLASS} required value={kind} disabled={pending || presale !== null} onChange={event => {
        const next = event.target.value as AccountKind | "";
        setKind(next);
        if (next && name.trim() === "") setName(accountKindLabel(next));
      }}>
        <option value="">—</option>
        {ACCOUNT_KINDS.map(value => <option key={value} value={value}>{accountKindLabel(value)}</option>)}
      </select>
      <p className="text-sm text-muted-foreground"><Message id="operator.presaleKindLocked" /></p>
      <label htmlFor={`${prefix}-name`}><Message id="operator.presaleName" /></label><Input id={`${prefix}-name`} required maxLength={128} value={name} disabled={pending} onChange={e => setName(e.target.value)} />
      <label htmlFor={`${prefix}-currency`}><Message id="tenant.accountCurrency" /></label><CurrencySelect id={`${prefix}-currency`} className={SELECT_CLASS} required value={currency} disabled={pending} onChange={setCurrency} />
      <label htmlFor={`${prefix}-price`}><Message id="tenant.accountPrice" /></label><Input id={`${prefix}-price`} required inputMode="decimal" value={rawPrice} disabled={pending} onChange={e => setPrice(e.target.value)} />
      <label htmlFor={`${prefix}-product`}><Message id="tenant.accountProductId" /></label><Input id={`${prefix}-product`} required maxLength={64} value={productId} disabled={pending} onChange={e => setProductId(e.target.value)} />
      <p className="text-sm text-muted-foreground"><Message id="tenant.accountProductHint" /></p>
      <label htmlFor={`${prefix}-capacity`}><Message id="operator.presaleCapacity" /></label><Input id={`${prefix}-capacity`} required inputMode="numeric" max={MAX_PRESALE_CAPACITY} value={capacity} disabled={pending} onChange={e => setCapacity(e.target.value)} />
      <p className="text-sm text-muted-foreground"><Message id="operator.presaleCapacityHint" /></p>
      <label className="flex items-center gap-2"><input type="checkbox" checked={active} disabled={pending} onChange={e => setActive(e.target.checked)} /><Message id="tenant.accountListed" /></label>
      {touched && built.problem ? <p role="status" className="text-sm text-muted-foreground"><Message id={PROBLEM_KEYS[built.problem]} /></p> : null}
      <FormError message={error === null ? null : <ErrorMessage error={error} />} />
      <div className="flex gap-3"><Button type="submit" disabled={pending || built.request === null}><Message id="operator.presaleSave" /></Button><Button type="button" variant="outline" disabled={pending} onClick={onClose}><Message id="common.cancel" /></Button></div>
    </form></CardContent>
  </Card>;
}

/** PresaleAwaitingOrders is the operator's delivery queue: paid presale
 * orders that have no account yet. Each is assigned an account of its
 * presale's kind, or refunded when it cannot be delivered. Both are writes
 * sent once; the list is read again afterwards rather than edited in place.
 *
 * PresaleAwaitingOrders 是运维的发货队列：尚未分配账号的已付款预售订单。每笔订单分配
 * 一个与其预售同类型的账号，无法交付时退款。两者都是只发送一次的写操作；之后重新读取
 * 列表，而不是就地修改。 */
function PresaleAwaitingOrders({ presales }: { presales: Resource<AccountPresale[]> }) {
  const { t, locale } = useI18n();
  const orders = usePagedResource<CheckoutOrder>({ path: "/operator/v1/billing/orders", surface: "operator", filters: { fulfillment: "awaiting" }, pageSize: 20, parse: parseOrderPage });
  const [assigning, setAssigning] = React.useState<CheckoutOrder | null>(null);
  const [refunding, setRefunding] = React.useState<CheckoutOrder | null>(null);
  const [notice, setNotice] = React.useState<MessageKey | null>(null);
  // Ages are measured from when the list was last asked for, not from each
  // render. 等待时长从最近一次读取列表时算起，而不是每次渲染。
  const [now, setNow] = React.useState(() => new Date());
  const presaleOf = (order: CheckoutOrder) => presales.data?.find(presale => presale.id === order.presaleId) ?? null;
  const reload = () => { setNow(new Date()); orders.reload(); presales.reload(); };
  const done = (key: MessageKey) => { setNotice(key); reload(); };
  return <Card>
    <CardHeader className="flex flex-wrap items-center justify-between gap-3">
      <CardTitle><Message id="operator.presaleAwaiting" /></CardTitle>
      <Button variant="outline" disabled={orders.loading} onClick={() => { setNotice(null); reload(); }}><Message id="common.refresh" /></Button>
    </CardHeader>
    <CardContent className="grid gap-3">
      <p className="text-sm text-muted-foreground"><Message id="operator.presaleAwaitingNote" /></p>
      {notice ? <p role="status" aria-live="polite" className="rounded-lg border bg-muted/40 px-4 py-3 text-sm"><Message id={notice} /></p> : null}
      {orders.errorKind === "not_found" ? <EmptyState title={t("tenant.accountDisabled")} description={t("tenant.accountEnableHint")} />
        : orders.error ? <ErrorState message={orders.error} onRetry={orders.reload} />
        : orders.loading || !orders.items ? <LoadingState label={t("operator.presaleAwaiting")} rows={3} />
        : orders.items.length === 0 ? <EmptyState title={t("operator.presaleAwaitingEmpty")} />
        : <>
          <Table>
            <TableHeader><TableRow>
              <TableHead><Message id="operator.presalePaidAt" /></TableHead>
              <TableHead><Message id="tenant.tenantId" /></TableHead>
              <TableHead><Message id="tenant.billingOrderId" /></TableHead>
              <TableHead><Message id="operator.presaleItem" /></TableHead>
              <TableHead className="text-right"><Message id="tenant.billingAmount" /></TableHead>
              <TableHead className="text-right"><Message id="tenant.billingRefunded" /></TableHead>
              <TableHead><Message id="tenant.action" /></TableHead>
            </TableRow></TableHeader>
            <TableBody>{orders.items.map(order => {
              const presale = presaleOf(order);
              return <TableRow key={order.id}>
                <TableCell className="whitespace-nowrap">
                  <div>{formatDateTime(order.updatedAt, undefined, locale)}</div>
                  <div className="text-xs text-muted-foreground">{observationAge(order.updatedAt, now, locale)?.label ?? ""}</div>
                </TableCell>
                <TableCell><code className="text-xs break-all">{order.tenantId}</code></TableCell>
                <TableCell><code className="text-xs break-all">{order.id}</code></TableCell>
                <TableCell>{presale ? <div className="grid gap-1">
                  <span className="break-all">{presale.name}</span>
                  <Badge variant="secondary">{accountKindLabel(presale.kind)}</Badge>
                </div> : <span className="text-muted-foreground"><Message id="operator.presaleUnknown" /></span>}</TableCell>
                <TableCell className="text-right whitespace-nowrap">{formatMoney(order.amountMinor, order.currency, locale)}</TableCell>
                <TableCell className="text-right whitespace-nowrap">{order.refundedMinor > 0 ? formatMoney(order.refundedMinor, order.currency, locale) : "—"}</TableCell>
                <TableCell><div className="flex flex-wrap gap-2">
                  <Button size="sm" disabled={!presale} onClick={() => { setNotice(null); setAssigning(order); }}><Message id="operator.presaleAssign" /></Button>
                  <Button size="sm" variant="outline" disabled={order.refundableMinor <= 0} onClick={() => { setNotice(null); setRefunding(order); }}><Message id="operator.presaleRefund" /></Button>
                </div></TableCell>
              </TableRow>;
            })}</TableBody>
          </Table>
          <Pager resource={orders} loadedCount={orders.items.length} />
        </>}
    </CardContent>
    {assigning && presaleOf(assigning) ? <AssignDialog key={assigning.id} order={assigning} presale={presaleOf(assigning)!} onClose={() => setAssigning(null)} onAssigned={() => done("operator.presaleAssigned")} /> : null}
    {refunding ? <RefundDialog key={refunding.id} order={refunding} onClose={() => setRefunding(null)} onRefunded={() => done("operator.presaleRefundSubmitted")} /> : null}
  </Card>;
}

function AssignDialog({ order, presale, onClose, onAssigned }: { order: CheckoutOrder; presale: AccountPresale; onClose: () => void; onAssigned: () => void }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const accounts = useResource({ method: "GET", surface: "operator", path: "/operator/v1/billing/accounts", parse: parseAccountSales });
  const [accountId, setAccountId] = React.useState("");
  const [openedAt] = React.useState(() => Date.now());
  const prefix = React.useId();
  const candidates = accounts.data ? assignableAccounts(accounts.data, presale.kind, order.tenantId, openedAt) : [];
  const option = (account: (typeof candidates)[number]) => {
    const parts = [account.name, `${account.nodeId} / ${account.runtimeId}`];
    if (!account.active) parts.push(t("tenant.accountArchived"));
    if (account.tenantId === order.tenantId && account.expiresAt && Date.parse(account.expiresAt) > openedAt) {
      parts.push(t("operator.presaleAssignOwned", { time: formatDateTime(account.expiresAt, undefined, locale) }));
    }
    return parts.join(" · ");
  };
  return <ConfirmDialog open onOpenChange={open => { if (!open) onClose(); }} title={t("operator.presaleAssignTitle")}
    description={t("operator.presaleAssignDescription", { tenant: order.tenantId, order: order.id, kind: accountKindLabel(presale.kind) })}
    confirmLabel={t("operator.presaleAssign")} confirmDisabled={accountId === ""}
    onConfirm={async () => {
      try {
        // Assignment is a write and is sent once. 分配是写操作，只发送一次。
        await run({ method: "POST", surface: "operator", path: `/operator/v1/billing/orders/${encodeURIComponent(order.id)}/assign`, body: { account_id: accountId }, parse: parseCheckoutOrder });
      } catch (error) {
        if (error instanceof ApiError && error.status === 409) throw new ApiError("presale_assign_unavailable", 409);
        throw error;
      }
      onAssigned();
    }}>
    <div className="grid min-w-0 gap-2">
      {accounts.error ? <ErrorState message={accounts.error} onRetry={accounts.reload} />
        : accounts.loading ? <LoadingState label={t("operator.presaleAssignSelect")} rows={1} />
        : candidates.length === 0 ? <p role="status" className="text-sm text-muted-foreground"><Message id="operator.presaleAssignNone" /></p>
        : <>
          <label htmlFor={`${prefix}-account`}><Message id="operator.presaleAssignSelect" /></label>
          <select id={`${prefix}-account`} className={SELECT_CLASS} required value={accountId} onChange={event => setAccountId(event.target.value)}>
            <option value="">—</option>
            {candidates.map(account => <option key={account.id} value={account.id}>{option(account)}</option>)}
          </select>
        </>}
    </div>
  </ConfirmDialog>;
}

function RefundDialog({ order, onClose, onRefunded }: { order: CheckoutOrder; onClose: () => void; onRefunded: () => void }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [reason, setReason] = React.useState("");
  const prefix = React.useId();
  const valid = refundReason(reason);
  return <ConfirmDialog open destructive onOpenChange={open => { if (!open) onClose(); }} title={t("operator.presaleRefundTitle")}
    description={t("operator.presaleRefundDescription", { tenant: order.tenantId, order: order.id, amount: formatMoney(order.refundableMinor, order.currency, locale) })}
    confirmLabel={t("operator.presaleRefund")} confirmDisabled={valid === null}
    onConfirm={async () => {
      if (valid === null) return;
      try {
        // A refund moves money and is never retried; amount_minor is omitted
        // so the control plane refunds whatever is left.
        // 退款会移动资金，绝不重试；省略 amount_minor，由控制面退回剩余全部金额。
        await run({ method: "POST", surface: "operator", path: `/operator/v1/billing/orders/${encodeURIComponent(order.id)}/refund`, body: { reason: valid }, parse: parseRefundCreated });
      } catch (error) {
        if (error instanceof ApiError && error.status === 409) throw new ApiError("refund_unavailable", 409);
        if (error instanceof ApiError && error.status === 503) throw new ApiError("payment_unavailable", 503);
        throw error;
      }
      onRefunded();
    }}>
    <div className="grid gap-2">
      <label htmlFor={`${prefix}-reason`}><Message id="operator.presaleRefundReason" /></label>
      <Input id={`${prefix}-reason`} required maxLength={500} value={reason} onChange={event => setReason(event.target.value)} />
    </div>
  </ConfirmDialog>;
}

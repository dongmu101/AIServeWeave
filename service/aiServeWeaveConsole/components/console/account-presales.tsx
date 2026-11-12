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
import { forgetPresaleIntent, readPresaleIntent, savePresaleIntent, unitLabel, useActionId } from "./alipay-billing";
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
  presaleAlipayBody,
  presaleCheckoutBody,
  presaleRequest,
  refundReason,
  MAX_ALIPAY_AMOUNT,
  MAX_PRESALE_CAPACITY,
  type AccountPresale,
  type PresaleDraftProblem,
} from "@/lib/console/account-presales";
import {
  actionOutcome,
  formatPlanPrice,
  newActionId,
  parseAlipayOverview,
  parseAlipayPurchase,
  parseAlipayRedemption,
  presaleQuotaArrived,
  presaleTopUp,
  type AlipayOverview,
  type AlipayPlan,
} from "@/lib/console/alipay";
import { abortableSleep, currencyDigits, formatMoney, formatScaled, parseCheckout, parseCheckoutOrder, parseOrderPage, pollUntilSettled, type CheckoutOrder } from "@/lib/console/billing";
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
  alipay: "operator.presaleProblemAlipay",
};

/** PresaleSales lists the presales. A tenant pre-orders a month through the
 * provider's checkout and the order then waits for an operator to assign an
 * account; nothing here decides that a payment succeeded. A tenant sees the
 * section only when something is on presale.
 *
 * PresaleSales 列出预售。商户经服务商结账预购一个月，之后订单等待运维分配账号；这里
 * 不判定付款是否成功。只有存在预售商品时，商户才会看到这一区块。 */
export function PresaleSales({ resource, canPurchase = false, dodo = true, alipay = false, resume = false }: { resource: Resource<AccountPresale[]>; canPurchase?: boolean; dodo?: boolean; alipay?: boolean; resume?: boolean }) {
  return alipay ? <PresaleSalesWithAlipay resource={resource} canPurchase={canPurchase} dodo={dodo} resume={resume} /> : <PresaleCatalog resource={resource} canPurchase={canPurchase} dodo={dodo} />;
}

const ALIPAY_OVERVIEW_PATH = "/admin/v1/billing/alipay";
// Alipay may take a while to report a payment: about two minutes of reads.
// 支付宝报告一笔付款可能需要一段时间：大约读取两分钟。
const TOP_UP_POLL_ATTEMPTS = 40;


/** PresaleSalesWithAlipay reads the tenant's Alipay overview, which says
 * whether quota can be redeemed and in which unit, so a presale can also be
 * paid with it.
 *
 * PresaleSalesWithAlipay 读取租户的支付宝概览，它说明能否兑换额度及其单位，使预售也能
 * 用支付宝额度支付。 */
function PresaleSalesWithAlipay({ resource, canPurchase, dodo, resume }: { resource: Resource<AccountPresale[]>; canPurchase: boolean; dodo: boolean; resume: boolean }) {
  const run = useConsoleRequest();
  const overview = useResource({ method: "GET", path: ALIPAY_OVERVIEW_PATH, parse: parseAlipayOverview });
  const [resumeNotice, setResumeNotice] = React.useState<MessageKey | null>(null);
  const reloadPresales = resource.reload;
  const reloadOverview = overview.reload;

  React.useEffect(() => {
    if (!resume || !canPurchase) {
      return;
    }
    const intent = readPresaleIntent();
    if (intent === null) {
      return;
    }
    // Back from buying quota for a presale: wait a bounded while for Alipay
    // to report enough quota, then pay with it once. The intent is dropped
    // before paying, so neither a reload nor a second mount pays again; a
    // payment whose answer is lost resumes from the button like any other.
    // 为预售购买额度后返回：在有限时间内等待支付宝报告额度足够，然后用它支付一次。支付之前
    // 先丢弃意图，因此刷新或再次挂载都不会重复支付；答复丢失的支付与其他支付一样从按钮恢复。
    const controller = new AbortController();
    void pollUntilSettled<AlipayOverview>({
      signal: controller.signal,
      sleep: abortableSleep,
      maxAttempts: TOP_UP_POLL_ATTEMPTS,
      read: (signal) => run({ method: "GET", path: ALIPAY_OVERVIEW_PATH, parse: parseAlipayOverview, signal }),
      onResult: (value) => { if (!presaleQuotaArrived(value, intent)) setResumeNotice("tenant.presaleTopUpWaiting"); },
      settled: (value) => presaleQuotaArrived(value, intent),
      giveUp: (error) => error instanceof ApiError && (error.kind === "not_found" || error.kind === "unauthorized" || error.kind === "forbidden"),
    }).then(async (outcome) => {
      if (outcome === "aborted") {
        return;
      }
      forgetPresaleIntent();
      if (outcome !== "settled") {
        setResumeNotice("tenant.presaleTopUpTimeout");
        reloadOverview();
        return;
      }
      let key: MessageKey;
      try {
        // Sent once and never retried, like the button's payment.
        // 只发送一次、绝不重试，与按钮发起的支付相同。
        const row = await run({ method: "POST", path: `/admin/v1/billing/presales/${encodeURIComponent(intent.presaleId)}/alipay`, body: presaleAlipayBody({ alipayAmount: intent.amount }, newActionId()), parse: parseAlipayRedemption });
        key = row.status === "succeeded" ? "tenant.presaleAlipayPaid" : row.status === "failed" ? "tenant.presaleAlipayFailed" : "tenant.presaleAlipayPending";
      } catch (error) {
        key = actionOutcome(error) === "unknown" ? "tenant.presaleAlipayPending" : "tenant.presaleTopUpPayFailed";
      }
      setResumeNotice(key);
      reloadPresales();
      reloadOverview();
    });
    return () => controller.abort();
  }, [resume, canPurchase, run, reloadPresales, reloadOverview]);

  return <PresaleCatalog resource={resource} canPurchase={canPurchase} dodo={dodo} alipayOn alipay={overview.data} onAlipayPaid={overview.reload} extraNotice={resumeNotice} />;
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

function PresaleCatalog({ resource, operator = false, canPurchase = false, dodo = true, alipayOn = false, alipay = null, onAlipayPaid, extraNotice = null }: {
  resource: Resource<AccountPresale[]>; operator?: boolean; canPurchase?: boolean; dodo?: boolean; alipayOn?: boolean; alipay?: AlipayOverview | null; onAlipayPaid?: () => void; extraNotice?: MessageKey | null;
}) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const path = operator ? "/operator/v1/billing/presales" : "/admin/v1/billing/presales";
  const [editing, setEditing] = React.useState<AccountPresale | "new" | null>(null);
  const [buying, setBuying] = React.useState<AccountPresale | null>(null);
  const [notice, setNotice] = React.useState<MessageKey | null>(null);
  const rows = resource.data ?? [];
  if (!operator && (resource.errorKind === "not_found" || resource.loading || (resource.data !== null && rows.length === 0))) {
    return null;
  }
  return <Card>
    <CardHeader><CardTitle><Message id="tenant.presale" /></CardTitle></CardHeader>
    <CardContent className="grid gap-4">
      <p className="text-sm text-muted-foreground"><Message id={operator ? "tenant.presaleOperatorNote" : "tenant.presaleNote"} /></p>
      {extraNotice && extraNotice !== notice ? <p role="status" aria-live="polite" className="rounded-lg border bg-muted/40 px-4 py-3 text-sm"><Message id={extraNotice} /></p> : null}
      {notice ? <p role="status" aria-live="polite" className="rounded-lg border bg-muted/40 px-4 py-3 text-sm"><Message id={notice} /></p> : null}
      {operator ? <div className="flex flex-wrap gap-3">
        <Button variant="outline" disabled={resource.loading} onClick={resource.reload}><Message id="common.refresh" /></Button>
        <Button disabled={resource.loading || resource.error !== null} onClick={() => setEditing("new")}><Message id="operator.presaleAdd" /></Button>
      </div> : null}
      {resource.errorKind === "not_found" ? <EmptyState title={t("tenant.accountDisabled")} description={t("tenant.accountEnableHint")} />
        : resource.error ? <ErrorState message={resource.error} onRetry={resource.reload} />
        : resource.loading ? <LoadingState label={t("tenant.presale")} rows={2} />
        : rows.length === 0 ? <EmptyState title={t("tenant.presaleEmpty")} />
        : <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">{rows.map(presale => {
          // Whether this presale can be bought now by a way this deployment offers.
          // 此刻能否用本部署提供的方式购买这个预售。
          const open = operator ? presale.available || presale.alipayAvailable : (dodo && presale.available) || (alipayOn && presale.alipayAvailable);
          const alipayOffer = !operator && alipayOn && alipay !== null && alipay.canRedeem && presale.alipayAmount > 0 && presale.alipayAmount <= alipay.maxAmount ? alipay : null;
          // Short of quota, the Alipay button first buys a plan that covers it.
          // 额度不足时，支付宝按钮先购买一档能补足差额的套餐。
          const topUp = alipayOffer ? presaleTopUp(alipayOffer, presale.alipayAmount) : null;
          return <div key={presale.id} className="grid content-between gap-3 rounded-lg border p-4">
          <div className="grid gap-2">
            <div className="flex items-start justify-between gap-2">
              <p className="line-clamp-2 min-w-0 font-medium break-all" title={presale.name}>{presale.name}</p>
              <Badge variant="secondary" className="shrink-0">{accountKindLabel(presale.kind)}</Badge>
            </div>
            <div className="flex flex-wrap gap-2">
              <Badge variant="outline"><Message id="tenant.presaleBadge" /></Badge>
              {!presale.active ? <Badge variant="outline"><Message id="tenant.accountArchived" /></Badge> : null}
              {presale.active && !open ? <Badge variant="destructive"><Message id="tenant.presaleSoldOut" /></Badge> : null}
            </div>
            <p className="text-lg font-semibold">{t("tenant.accountMonthlyPrice", { price: formatMoney(presale.priceMinor, presale.currency, locale) })}</p>
            <p className="text-sm text-muted-foreground"><Message id="tenant.presaleDelivery" /></p>
            {alipayOffer ? <p className="text-sm">{t("tenant.presaleAlipayCost", { amount: presale.alipayAmount, unit: unitLabel(alipayOffer.quotaUnit, t) })}</p> : null}
            {alipayOffer && topUp?.kind === "none" && canPurchase ? <p className="text-sm text-muted-foreground">{t("tenant.presaleTopUpNoPlan", { shortfall: topUp.shortfall, unit: unitLabel(alipayOffer.quotaUnit, t) })}</p> : null}
            {operator && presale.alipayAmount > 0 ? <p className="text-sm">{t("operator.presaleAlipayPaid", { amount: presale.alipayAmount })}</p> : null}
            {operator && presale.capacity !== null && presale.outstanding !== null ? <p className="text-sm">{t("operator.presaleStock", { outstanding: presale.outstanding, capacity: presale.capacity })}</p> : null}
            {operator && presale.dodoProductId ? <code className="text-xs break-all text-muted-foreground">{presale.dodoProductId}</code> : null}
          </div>
          {operator ? <Button variant="outline" onClick={() => setEditing(presale)}><Message id="operator.presaleEdit" /></Button>
            : canPurchase ? <div className="flex flex-wrap gap-2">
              {dodo ? <Button disabled={!presale.available} onClick={() => { setNotice(null); setBuying(presale); }}><Message id="tenant.presaleBuy" /></Button> : null}
              {alipayOffer && topUp?.kind === "plan" ? <PresaleAlipayTopUp key={presale.id} presale={presale} plan={topUp.plan} shortfall={topUp.shortfall}
                remaining={alipayOffer.access?.remaining ?? 0} unit={alipayOffer.quotaUnit} primary={!dodo} />
                : alipayOffer ? <PresaleAlipayPay key={presale.id} presale={presale} unit={alipayOffer.quotaUnit} primary={!dodo} disabled={topUp?.kind === "none"}
                  onPaid={key => { setNotice(key); resource.reload(); onAlipayPaid?.(); }} /> : null}
            </div> : null}
        </div>;
        })}</div>}
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

/** PresaleAlipayPay pays one month of a presale with the tenant's Alipay
 * quota. The click keeps its action id until a definite answer, so pressing
 * again after a lost answer resumes the same order instead of consuming
 * twice. Only the control plane says whether the consumption was confirmed:
 * a pending answer is reported as such, never as paid.
 *
 * PresaleAlipayPay 用租户的支付宝额度支付预售的一个月。点击在得到确定答复之前保留其
 * action id，因此答复丢失后再次点击会恢复同一订单而不是重复扣减。核销是否已确认只由控制面
 * 说了算：待确认的答复就如实报告为待确认，绝不当作已付款。 */
function PresaleAlipayPay({ presale, unit, primary, disabled = false, onPaid }: { presale: AccountPresale; unit: string; primary: boolean; disabled?: boolean; onPaid: (notice: MessageKey) => void }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const action = useActionId();
  const [open, setOpen] = React.useState(false);
  return <>
    <Button variant={primary ? "default" : "outline"} disabled={disabled || !presale.alipayAvailable} onClick={() => setOpen(true)}><Message id="tenant.presaleAlipayPay" /></Button>
    <ConfirmDialog open={open} onOpenChange={setOpen} title={t("tenant.presaleAlipayPay")}
      description={t("tenant.presaleAlipayConfirm", { name: presale.name, kind: accountKindLabel(presale.kind), cost: t("tenant.alipayRemainingValue", { amount: presale.alipayAmount, unit: unitLabel(unit, t) }) })}
      onConfirm={async () => {
        let row;
        try {
          // A payment is a write and is never retried automatically: the
          // action id makes a deliberate second press resume the same order.
          // 付款是写操作，绝不自动重试：action id 使有意的再次点击恢复同一订单。
          row = await run({ method: "POST", path: `/admin/v1/billing/presales/${encodeURIComponent(presale.id)}/alipay`, body: presaleAlipayBody(presale, action.current()), parse: parseAlipayRedemption });
        } catch (error) {
          action.settle(error);
          if (error instanceof ApiError && error.status === 409) throw new ApiError("presale_unavailable", 409);
          if (error instanceof ApiError && error.status === 404) throw new ApiError("account_checkout_disabled", 404);
          if (error instanceof ApiError && error.status === 503) throw new ApiError("payment_unavailable", 503);
          throw error;
        }
        action.settle();
        onPaid(row.status === "succeeded" ? "tenant.presaleAlipayPaid" : row.status === "failed" ? "tenant.presaleAlipayFailed" : "tenant.presaleAlipayPending");
      }} />
  </>;
}

/** PresaleAlipayTopUp pays a presale the tenant lacks quota for: it opens
 * the Alipay purchase entry for a plan that covers the shortfall and
 * remembers the presale in the browser, so that back on this page, once
 * Alipay reports the quota, the presale is paid with it. Nothing here treats
 * the trip to Alipay as a payment; only the quota read afterwards counts.
 *
 * PresaleAlipayTopUp 支付商户额度不足的预售：为一档能补足差额的套餐打开支付宝购买入口，
 * 并在浏览器中记住该预售，使回到本页、支付宝报告额度之后用它支付预售。这里不把前往
 * 支付宝当作付款；只有之后读到的额度才算数。 */
function PresaleAlipayTopUp({ presale, plan, shortfall, remaining, unit, primary }: {
  presale: AccountPresale; plan: AlipayPlan; shortfall: number; remaining: number; unit: string; primary: boolean;
}) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const action = useActionId();
  const [open, setOpen] = React.useState(false);
  const label = unitLabel(unit, t);
  return <>
    <Button variant={primary ? "default" : "outline"} disabled={!presale.alipayAvailable} onClick={() => setOpen(true)}><Message id="tenant.presaleAlipayPay" /></Button>
    <ConfirmDialog open={open} onOpenChange={setOpen} title={t("tenant.presaleAlipayPay")} confirmLabel={t("tenant.presaleTopUpGo")}
      description={t("tenant.presaleTopUpConfirm", {
        remaining, shortfall, unit: label, plan: plan.name || plan.skuId, quota: plan.quotaAmount, price: formatPlanPrice(plan, locale),
        amount: presale.alipayAmount, name: presale.name, kind: accountKindLabel(presale.kind),
      })}
      onConfirm={async () => {
        savePresaleIntent({ presaleId: presale.id, amount: presale.alipayAmount, savedAt: Date.now() });
        let result;
        try {
          // Opening an entry is a write and is never retried automatically;
          // a second press after a lost answer reuses the action id.
          // 创建入口是写操作，绝不自动重试；答复丢失后的再次按下沿用 action id。
          result = await run({ method: "POST", path: "/admin/v1/billing/alipay/purchases", body: { action_id: action.current(), sku_id: plan.skuId }, parse: parseAlipayPurchase });
        } catch (error) {
          action.settle(error);
          if (actionOutcome(error) === "definite") forgetPresaleIntent();
          if (error instanceof ApiError && (error.status === 409 || error.status === 404)) throw new ApiError("alipay_purchase_unavailable", error.status);
          if (error instanceof ApiError && error.status === 503) throw new ApiError("payment_unavailable", 503);
          throw error;
        }
        action.settle();
        window.location.assign(result.purchaseUrl);
      }}>
      {action.retrying ? <p className="text-xs text-muted-foreground"><Message id="tenant.alipayRetryHint" /></p> : null}
    </ConfirmDialog>
  </>;
}

function PresaleEditor({ presale, onClose, onSaved }: { presale: AccountPresale | null; onClose: () => void; onSaved: () => void }) {
  const run = useConsoleRequest();
  const [kind, setKind] = React.useState<AccountKind | "">(presale?.kind ?? "");
  const [name, setName] = React.useState(presale?.name ?? "");
  const [currency, setCurrency] = React.useState(presale?.currency ?? "USD");
  const [rawPrice, setPrice] = React.useState(presale ? formatScaled(presale.priceMinor, currencyDigits(presale.currency)) : "");
  const [productId, setProductId] = React.useState(presale?.dodoProductId ?? "");
  const [capacity, setCapacity] = React.useState(presale?.capacity ? String(presale.capacity) : "");
  const [alipayAmount, setAlipayAmount] = React.useState(presale && presale.alipayAmount > 0 ? String(presale.alipayAmount) : "");
  const [active, setActive] = React.useState(presale ? presale.active : true);
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const built = presaleRequest({ kind, name, price: rawPrice, currency, productId, capacity, active, alipayAmount });
  const alipayOnly = built.request !== null ? built.request.alipay_amount > 0 : /^[1-9]\d*$/.test(alipayAmount.trim());
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
      <label htmlFor={`${prefix}-product`}><Message id="tenant.accountProductId" /></label><Input id={`${prefix}-product`} required={!alipayOnly} maxLength={64} value={productId} disabled={pending} onChange={e => setProductId(e.target.value)} />
      <p className="text-sm text-muted-foreground"><Message id="tenant.accountProductHint" /></p>
      <label htmlFor={`${prefix}-capacity`}><Message id="operator.presaleCapacity" /></label><Input id={`${prefix}-capacity`} required inputMode="numeric" max={MAX_PRESALE_CAPACITY} value={capacity} disabled={pending} onChange={e => setCapacity(e.target.value)} />
      <p className="text-sm text-muted-foreground"><Message id="operator.presaleCapacityHint" /></p>
      <label htmlFor={`${prefix}-alipay`}><Message id="operator.presaleAlipayAmount" /></label><Input id={`${prefix}-alipay`} inputMode="numeric" max={MAX_ALIPAY_AMOUNT} value={alipayAmount} disabled={pending} onChange={e => setAlipayAmount(e.target.value)} />
      <p className="text-sm text-muted-foreground"><Message id="operator.presaleAlipayHint" /></p>
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
                <TableCell className="text-right">{order.alipayAmount > 0 ? <div className="grid gap-1">
                  <span className="whitespace-nowrap">{t("operator.presaleAlipayPaid", { amount: order.alipayAmount })}</span>
                  <span className="text-xs text-muted-foreground"><Message id="operator.presaleAlipayNoRefund" /></span>
                </div> : <span className="whitespace-nowrap">{formatMoney(order.amountMinor, order.currency, locale)}</span>}</TableCell>
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

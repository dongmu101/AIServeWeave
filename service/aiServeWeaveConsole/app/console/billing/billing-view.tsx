"use client";

import * as React from "react";

import { BillingDisabled, LedgerTable, OrdersTable } from "@/components/console/billing-tables";
import { ErrorMessage } from "@/components/i18n/error-message";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { EmptyState, ErrorState, FormError, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  abortableSleep,
  checkoutBody,
  currencyDigits,
  customAmountProblem,
  customCredits,
  formatCredits,
  formatMoney,
  parseBillingOverview,
  parseCheckout,
  parseOrderPage,
  parsePortalLink,
  pollUntilSettled,
  type BillingOverview,
  type CheckoutOrder,
  type CreditPackage,
} from "@/lib/console/billing";
import { ApiError } from "@/lib/console/errors";
import { canPurchaseCredits } from "@/lib/console/permissions";
import type { MessageKey } from "@/lib/i18n/messages";

/** ReturnState is where the wait after a payment return stands.
 *
 * ReturnState 是支付回跳后等待过程所处的阶段。 */
type ReturnState = "pending" | "settled" | "exhausted";

/** PollRead is one poll tick: the overview, and the latest order whose status
 * the server recorded from the provider's webhook.
 *
 * PollRead 是一次轮询：概览，以及最近一笔订单——其状态由服务端依据服务商 webhook 记录。 */
interface PollRead {
  overview: BillingOverview;
  latest: CheckoutOrder | null;
}

/**
 * paymentProblem turns a failed checkout or portal call into fixed copy: 503
 * means the control plane could not reach the payment provider, and a 404
 * from the portal means there is no customer record yet. Upstream text is
 * never shown.
 *
 * paymentProblem 把一次失败的结账或门户调用转换为固定文案：503 表示控制面没能联系上
 * 支付服务商，门户的 404 表示还没有客户记录。上游文本从不展示。
 */
function paymentProblem(failure: unknown, notFound?: MessageKey): React.ReactNode {
  if (failure instanceof ApiError && failure.status === 503) {
    return <Message id="tenant.billingPaymentUnavailable" />;
  }
  if (notFound && failure instanceof ApiError && failure.kind === "not_found") {
    return <Message id={notFound} />;
  }
  return <ErrorMessage error={failure} />;
}

/**
 * BillingView shows the balance, the packages on sale and the history.
 *
 * Nothing on this page decides that a payment succeeded. After the provider
 * sends the buyer back, the page reads the overview and the latest order a
 * bounded number of times and stops as soon as the server reports that order
 * as no longer pending; the balance shown is always the server's.
 *
 * BillingView 展示余额、在售充值包与历史记录。
 *
 * 本页面上没有任何东西会判定一次支付成功。服务商把购买者送回之后，页面在有限次数内
 * 读取概览与最近一笔订单，一旦服务端报告该订单不再处于待支付就停止；展示的余额始终
 * 是服务端的值。
 */
export function BillingView({ role, returning }: { role: string; returning: boolean }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const overview = useResource<BillingOverview>({ method: "GET", path: "/admin/v1/billing", parse: parseBillingOverview });
  const [polled, setPolled] = React.useState<BillingOverview | null>(null);
  const [returnState, setReturnState] = React.useState<ReturnState | null>(returning ? "pending" : null);
  const [historyKey, setHistoryKey] = React.useState(0);

  React.useEffect(() => {
    if (!returning) {
      return;
    }
    const controller = new AbortController();
    void pollUntilSettled<PollRead>({
      signal: controller.signal,
      sleep: abortableSleep,
      read: async (signal) => {
        const [current, orders] = await Promise.all([
          run({ method: "GET", path: "/admin/v1/billing", parse: parseBillingOverview, signal }),
          run({ method: "GET", path: "/admin/v1/billing/orders", query: { limit: "1" }, parse: parseOrderPage, signal }),
        ]);
        return { overview: current, latest: orders.items[0] ?? null };
      },
      onResult: (value) => setPolled(value.overview),
      settled: (value) => value.latest !== null && value.latest.status !== "pending",
      giveUp: (error) => error instanceof ApiError && (error.kind === "not_found" || error.kind === "unauthorized" || error.kind === "forbidden"),
    }).then((outcome) => {
      if (outcome === "aborted") {
        return;
      }
      setReturnState(outcome === "settled" ? "settled" : "exhausted");
      // The ledger and orders were read before the confirmation arrived;
      // remounting them reads the rows the confirmation produced.
      //
      // 账目与订单是在确认到达之前读取的；重挂载它们会读到确认所产生的行。
      setHistoryKey((value) => value + 1);
    });
    return () => controller.abort();
  }, [returning, run]);

  const data = polled ?? overview.data;
  const canPurchase = canPurchaseCredits(role);

  return <div className="grid gap-6">
    <div>
      <h1 className="font-heading text-lg font-semibold"><Message id="tenant.billing" /></h1>
      <p className="text-sm text-muted-foreground"><Message id="tenant.billingDescription" /></p>
    </div>
    {overview.errorKind === "not_found" && !polled ? <BillingDisabled /> : overview.error && !polled ? <ErrorState message={overview.error} onRetry={overview.reload} /> : !data ? <LoadingState label={t("tenant.loadingBilling")} rows={4} /> : <>
      {returnState ? <ReturnNotice state={returnState} /> : null}
      <BalanceCard overview={data} canPurchase={canPurchase} />
      <PackagesCard packages={data.packages} canPurchase={canPurchase} />
      <div key={historyKey} className="grid gap-6">
        <LedgerTable />
        <OrdersTable />
      </div>
    </>}
  </div>;
}

/** ReturnNotice says what the page knows after a payment return, which is
 * only ever what the server has confirmed.
 *
 * ReturnNotice 说明支付回跳后页面所知道的情况——而那只能是服务端已确认的内容。 */
function ReturnNotice({ state }: { state: ReturnState }) {
  const key: MessageKey = state === "pending" ? "tenant.billingReturnPending" : state === "settled" ? "tenant.billingReturnSettled" : "tenant.billingReturnTimeout";
  return <p role="status" aria-live="polite" className="rounded-lg border bg-muted/40 px-4 py-3 text-sm"><Message id={key} /></p>;
}

/** BalanceCard shows the balance, the mode, and the portal entry.
 *
 * BalanceCard 展示余额、计费模式与门户入口。 */
function BalanceCard({ overview, canPurchase }: { overview: BillingOverview; canPurchase: boolean }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  async function openPortal() {
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      // POST, never retried: each call asks the provider for a new one-time
      // link, and a link is only useful to the browser that asked for it.
      //
      // POST 且绝不重试：每次调用都会向服务商申请一条新的一次性链接，而链接只对发起
      // 请求的那个浏览器有用。
      const url = await run({ method: "POST", path: "/admin/v1/billing/portal", parse: parsePortalLink });
      window.location.assign(url);
    } catch (failure) {
      setError(paymentProblem(failure, "tenant.billingPortalNotReady"));
      setPending(false);
    }
  }

  const exempt = overview.mode === "exempt";
  return <Card>
    <CardHeader><CardTitle><Message id="tenant.billingBalance" /></CardTitle></CardHeader>
    <CardContent className="grid gap-4">
      <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
        <div className="grid gap-1">
          <dt className="text-xs text-muted-foreground"><Message id="tenant.billingBalance" /></dt>
          <dd className="font-heading text-2xl font-semibold break-all">{t("tenant.billingCreditsValue", { credits: formatCredits(overview.balanceMicro, locale) })}</dd>
        </div>
        <div className="grid gap-1">
          <dt className="text-xs text-muted-foreground"><Message id="tenant.billingMode" /></dt>
          <dd><Badge variant={exempt ? "secondary" : "outline"}>{exempt ? t("tenant.billingModeExempt") : t("tenant.billingModePrepaid")}</Badge></dd>
        </div>
      </dl>
      {exempt ? <p className="text-sm text-muted-foreground"><Message id="tenant.billingExemptNote" /></p> : null}
      {!exempt && overview.blocked ? <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm font-medium text-destructive"><Message id="tenant.billingBlocked" /></p> : null}
      {overview.hasCustomer && canPurchase ? <div className="grid gap-2">
        <div><Button variant="outline" onClick={openPortal} disabled={pending} aria-busy={pending}><Message id="tenant.billingManagePayments" /></Button></div>
        <p className="text-xs text-muted-foreground"><Message id="tenant.billingPortalHint" /></p>
        <FormError message={error} />
      </div> : null}
    </CardContent>
  </Card>;
}

/** PackagesCard lists the packages on sale.
 *
 * PackagesCard 列出在售的充值包。 */
function PackagesCard({ packages, canPurchase }: { packages: CreditPackage[]; canPurchase: boolean }) {
  const { t } = useI18n();
  return <Card>
    <CardHeader><CardTitle><Message id="tenant.billingPackages" /></CardTitle></CardHeader>
    <CardContent className="grid gap-4">
      {!canPurchase ? <p className="text-sm text-muted-foreground"><Message id="tenant.billingPurchaseOwnerOnly" /></p> : null}
      {packages.length === 0 ? <EmptyState title={t("tenant.billingNoPackages")} description={t("tenant.billingNoPackagesDescription")} /> : <ul className="grid gap-4 sm:grid-cols-2">
        {packages.map((pkg) => <li key={pkg.id} className="min-w-0"><PackageOffer pkg={pkg} canPurchase={canPurchase} /></li>)}
      </ul>}
    </CardContent>
  </Card>;
}

/**
 * PackageOffer is one package and its buy button. A custom package converts
 * the typed amount to credits as the buyer types, with the same range check
 * the server applies, so a refused amount is explained before it is sent.
 *
 * PackageOffer 是一个充值包及其购买按钮。自定义充值包在购买者输入时就把金额换算成
 * 点数，并做与服务端相同的范围检查，这样一个会被拒绝的金额在发送前就已说明原因。
 */
function PackageOffer({ pkg, canPurchase }: { pkg: CreditPackage; canPurchase: boolean }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [raw, setRaw] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const custom = pkg.kind === "custom";
  const check = custom ? customAmountProblem(pkg, raw) : null;
  const inputId = `billing-amount-${pkg.id}`;

  async function buy() {
    if (pending) return;
    const amount = check ? check.amountMinor : null;
    if (custom && amount === null) return;
    setPending(true);
    setError(null);
    try {
      // A checkout is a write and is never retried: a repeat would open a
      // second pending order for the same intent.
      //
      // 结账是写操作，绝不重试：重发会为同一个意图再开一笔待支付订单。
      const result = await run({ method: "POST", path: "/admin/v1/billing/checkout", body: checkoutBody(pkg, amount), parse: parseCheckout });
      window.location.assign(result.checkoutUrl);
    } catch (failure) {
      setError(paymentProblem(failure));
      setPending(false);
    }
  }

  let hint: React.ReactNode = null;
  if (check && raw.trim() !== "") {
    if (check.problem === null) {
      hint = <p className="text-sm">{t("tenant.billingYouWillReceive", { credits: formatCredits(customCredits(pkg, check.amountMinor) ?? 0, locale) })}</p>;
    } else {
      const message = check.problem === "below_min"
        ? t("tenant.billingAmountBelowMin", { min: formatMoney(pkg.minAmountMinor, pkg.currency, locale) })
        : check.problem === "above_max"
          ? t("tenant.billingAmountAboveMax", { max: formatMoney(pkg.maxAmountMinor, pkg.currency, locale) })
          : t("tenant.billingAmountInvalid", { digits: currencyDigits(pkg.currency) });
      hint = <p className="text-sm text-destructive" id={`${inputId}-problem`}>{message}</p>;
    }
  }

  return <div className="grid h-full content-between gap-3 rounded-lg border p-4">
    <div className="grid gap-1">
      <p className="font-medium break-all">{pkg.name}</p>
      {custom ? <p className="text-xs text-muted-foreground"><Message id="tenant.billingCustomAmount" /></p> : <>
        <p className="font-heading text-xl font-semibold">{t("tenant.billingCreditsValue", { credits: formatCredits(pkg.creditsMicro, locale) })}</p>
        <p className="text-sm text-muted-foreground">{formatMoney(pkg.priceMinor, pkg.currency, locale)}</p>
      </>}
    </div>
    {custom ? <div className="grid gap-2">
      <label htmlFor={inputId} className="text-sm font-medium">{t("tenant.billingAmountLabel", { currency: pkg.currency })}</label>
      <Input id={inputId} inputMode="decimal" autoComplete="off" value={raw} onChange={(event) => setRaw(event.target.value)} disabled={pending || !canPurchase} aria-invalid={check?.problem !== null && raw.trim() !== "" ? true : undefined} aria-describedby={`${inputId}-range`} />
      <p id={`${inputId}-range`} className="text-xs text-muted-foreground">{t("tenant.billingAmountRange", { min: formatMoney(pkg.minAmountMinor, pkg.currency, locale), max: formatMoney(pkg.maxAmountMinor, pkg.currency, locale) })}</p>
      {hint}
    </div> : null}
    <FormError message={error} />
    <div><Button onClick={buy} disabled={!canPurchase || pending || (custom && check?.problem !== null)} aria-busy={pending}><Message id="tenant.billingBuy" /></Button></div>
  </div>;
}

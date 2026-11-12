"use client";

import * as React from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { AccountSales } from "@/components/console/account-sales";
import { PresaleSales } from "@/components/console/account-presales";
import { AlipayBilling, hasPresaleIntent } from "@/components/console/alipay-billing";

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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
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
import { parseAccountPresales, presaleAwaiting } from "@/lib/console/account-presales";
import type { MessageKey } from "@/lib/i18n/messages";

/** ReturnState is where the wait after a payment return stands.
 *
 * ReturnState 是支付回跳后等待过程所处的阶段。 */
type ReturnState = "pending" | "settled" | "exhausted";

/** BILLING_TABS are the page's sections, in order: what can be bought, then
 * what has happened.
 *
 * BILLING_TABS 是页面的分区，依次为可购买的内容与已发生的记录。 */
const BILLING_TABS = ["presales", "accounts", "packages", "alipay", "orders", "ledger"] as const;
type BillingTab = (typeof BILLING_TABS)[number];

const TAB_KEYS: Record<BillingTab, MessageKey> = {
  packages: "tenant.billingPackages",
  alipay: "tenant.billingAlipay",
  accounts: "tenant.accountMonthly",
  presales: "tenant.presale",
  orders: "tenant.billingOrders",
  ledger: "tenant.billingLedger",
};

function isBillingTab(value: string | null): value is BillingTab {
  return value !== null && (BILLING_TABS as readonly string[]).includes(value);
}

/** useBillingTab keeps the open section in the query, so a reload or a
 * shared link opens the same one. Without a choice it opens the orders after
 * a payment return, the monthly accounts for a tenant on them, and the
 * packages otherwise; a section not offered falls back the same way. The URL is replaced in place: switching sections does
 * not fill the back button, nor ask the server to render the page again.
 *
 * useBillingTab 把当前分区放在查询参数里，刷新或分享链接时打开同一个分区。未选择时，
 * 支付回跳后打开订单，账号包月的商户打开账号现货，其余打开充值包；未提供的分区同样回退。URL 原地替换：切换
 * 分区不会填满后退按钮，也不会让服务端重新渲染页面。 */
function useBillingTab(fallback: BillingTab, offered: readonly BillingTab[]): [BillingTab, (tab: BillingTab) => void] {
  const searchParams = useSearchParams();
  const chosen = searchParams.get("tab");
  const select = React.useCallback((tab: BillingTab) => {
    const next = new URLSearchParams(window.location.search);
    next.set("tab", tab);
    window.history.replaceState(null, "", `?${next.toString()}`);
  }, []);
  return [isBillingTab(chosen) && offered.includes(chosen) ? chosen : fallback, select];
}

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
export function BillingView({ role, returning, alipayReturning = false }: { role: string; returning: boolean; alipayReturning?: boolean }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const overview = useResource<BillingOverview>({ method: "GET", path: "/admin/v1/billing", parse: parseBillingOverview });
  const [polled, setPolled] = React.useState<BillingOverview | null>(null);
  const [returnState, setReturnState] = React.useState<ReturnState | null>(returning ? "pending" : null);
  const [returnedPresale, setReturnedPresale] = React.useState(false);
  const [historyKey, setHistoryKey] = React.useState(0);
  // Read here rather than in its tab: the tab is offered only while
  // something is on presale. 在此读取而不是在其标签页内：只有存在预售时才提供该标签页。
  const presales = useResource({ method: "GET", path: "/admin/v1/billing/presales", parse: parseAccountPresales });
  const reloadPresales = presales.reload;

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
      onResult: (value) => {
        setPolled(value.overview);
        setReturnedPresale(value.latest !== null && presaleAwaiting(value.latest));
      },
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
      reloadPresales();
    });
    return () => controller.abort();
  }, [returning, run, reloadPresales]);

  const data = polled ?? overview.data;
  const canPurchase = canPurchaseCredits(role);
  const reloadOverview = overview.reload;
  // A redemption moves the balance and writes the ledger; read both again.
  // The overview is read into `polled` so the page keeps showing the old one
  // meanwhile instead of unmounting the section that reported the outcome.
  // 兑换会变动余额并写入账目；两者都重新读取。概览读入 `polled`，期间页面继续显示旧值，
  // 而不是卸载报告结果的那个分区。
  const onRedeemed = React.useCallback(() => {
    setHistoryKey((value) => value + 1);
    run({ method: "GET", path: "/admin/v1/billing", parse: parseBillingOverview })
      .then(setPolled)
      .catch(() => { setPolled(null); reloadOverview(); });
  }, [run, reloadOverview]);
  // Packages, orders and presales are bought through Dodo; the Alipay section
  // exists only when the control plane offers it.
  // 充值包、订单与预售经 Dodo 购买；支付宝分区只在控制面提供时出现。
  const methods = data?.paymentMethods ?? { dodo: true, alipay: false };
  const offered = BILLING_TABS.filter((value) => {
    switch (value) {
      case "packages":
      case "orders":
        return methods.dodo;
      case "presales":
        return (methods.dodo || methods.alipay) && (presales.data?.length ?? 0) > 0;
      case "alipay":
        return methods.alipay;
      default:
        return true;
    }
  });
  const fallback: BillingTab = returning && methods.dodo ? "orders"
    : alipayReturning && methods.alipay ? "alipay"
    : data?.mode === "account_monthly" ? "accounts"
    : methods.dodo ? "packages"
    : methods.alipay ? "alipay"
    : "ledger";
  const [tab, setTab] = useBillingTab(fallback, offered);
  // Back from Alipay after buying quota for a presale: open the presales,
  // where the purchase is finished once the quota arrives. Storage is read
  // after hydration only, so the server and first client render agree.
  // 为预售购买额度后从支付宝返回：打开预售分区，额度到账后在那里完成购买。只在水合之后
  // 读取存储，使服务端渲染与客户端首次渲染一致。
  React.useEffect(() => {
    if (alipayReturning && hasPresaleIntent()) setTab("presales");
  }, [alipayReturning, setTab]);

  return <div className="grid gap-6">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <h1 className="font-heading text-lg font-semibold"><Message id="tenant.billing" /></h1>
        <p className="text-sm text-muted-foreground"><Message id="tenant.billingDescription" /></p>
        <p className="text-sm text-muted-foreground"><Message id="site.purchaseNote" values={{
          terms: <Link href="/terms" target="_blank" className="underline underline-offset-4"><Message id="site.terms" /></Link>,
          refund: <Link href="/refund" target="_blank" className="underline underline-offset-4"><Message id="site.refund" /></Link>,
        }} /></p>
      </div>
      {data?.hasCustomer && canPurchase ? <PortalButton /> : null}
    </div>
    {overview.errorKind === "not_found" && !polled ? <BillingDisabled /> : overview.error && !polled ? <ErrorState message={overview.error} onRetry={overview.reload} /> : !data ? <LoadingState label={t("tenant.loadingBilling")} rows={4} /> : <>
      {returnState ? <ReturnNotice state={returnState} presale={returnedPresale} /> : null}
      <BalanceSummary overview={data} canPurchase={canPurchase} />
      <Tabs value={tab} onValueChange={(next) => { if (isBillingTab(next)) setTab(next); }}>
        <TabsList aria-label={t("tenant.billingSections")} className="h-auto! w-full flex-wrap justify-start sm:w-fit">
          {offered.map((value) => <TabsTrigger key={value} value={value} className="flex-none px-3"><Message id={TAB_KEYS[value]} /></TabsTrigger>)}
        </TabsList>
        <TabsContent value="packages" className="mt-2">
          <PackagesCard packages={data.packages} canPurchase={canPurchase} />
        </TabsContent>
        {methods.alipay ? <TabsContent value="alipay" className="mt-2">
          <AlipayBilling canPurchase={canPurchase} returning={alipayReturning} onRedeemed={onRedeemed} />
        </TabsContent> : null}
        {/* Remounted with the history so a month confirmed after a payment
            return shows up. 与历史记录一同重挂载，使支付回跳后确认的期限能显示出来。 */}
        <TabsContent value="accounts" className="mt-2">
          <AccountSales key={historyKey} canPurchase={canPurchase} />
        </TabsContent>
        <TabsContent value="presales" className="mt-2">
          <PresaleSales resource={presales} canPurchase={canPurchase} dodo={methods.dodo} alipay={methods.alipay} resume={alipayReturning} />
        </TabsContent>
        <TabsContent value="orders" className="mt-2">
          <OrdersTable key={historyKey} />
        </TabsContent>
        <TabsContent value="ledger" className="mt-2">
          <LedgerTable key={historyKey} />
        </TabsContent>
      </Tabs>
    </>}
  </div>;
}

/** ReturnNotice says what the page knows after a payment return, which is
 * only ever what the server has confirmed. A confirmed presale payment says
 * that the account is still to be assigned.
 *
 * ReturnNotice 说明支付回跳后页面所知道的情况——而那只能是服务端已确认的内容。已确认的
 * 预售付款会说明账号仍待分配。 */
function ReturnNotice({ state, presale }: { state: ReturnState; presale: boolean }) {
  const key: MessageKey = state === "pending" ? "tenant.billingReturnPending"
    : state === "settled" ? (presale ? "tenant.billingReturnPresale" : "tenant.billingReturnSettled") : "tenant.billingReturnTimeout";
  return <p role="status" aria-live="polite" className="rounded-lg border bg-muted/40 px-4 py-3 text-sm"><Message id={key} /></p>;
}

/** PortalButton opens the provider's customer portal, where payment
 * methods and receipts live.
 *
 * PortalButton 打开服务商的客户门户，付款方式与收据都在那里。 */
function PortalButton() {
  const { t } = useI18n();
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

  return <div className="grid justify-items-end gap-1">
    <Button variant="outline" onClick={openPortal} disabled={pending} aria-busy={pending} title={t("tenant.billingPortalHint")}><Message id="tenant.billingManagePayments" /></Button>
    <FormError message={error} />
  </div>;
}

/** BalanceSummary shows the balance and the mode, with what either means
 * for requests right now.
 *
 * BalanceSummary 展示余额与计费模式，以及两者此刻对请求意味着什么。 */
function BalanceSummary({ overview, canPurchase }: { overview: BillingOverview; canPurchase: boolean }) {
  const { t, locale } = useI18n();
  const exempt = overview.mode === "exempt";
  return <Card>
    <CardContent className="grid gap-3">
      <dl className="flex flex-wrap items-start gap-x-10 gap-y-3">
        <div className="grid min-w-0 gap-1">
          <dt className="text-xs text-muted-foreground"><Message id="tenant.billingBalance" /></dt>
          <dd className="font-heading text-2xl font-semibold break-all">{t("tenant.billingCreditsValue", { credits: formatCredits(overview.balanceMicro, locale) })}</dd>
        </div>
        <div className="grid gap-1.5">
          <dt className="text-xs text-muted-foreground"><Message id="tenant.billingMode" /></dt>
          <dd><Badge variant={exempt ? "secondary" : "outline"}>{overview.mode === "account_monthly" ? t("tenant.billingModeAccountMonthly") : exempt ? t("tenant.billingModeExempt") : t("tenant.billingModePrepaid")}</Badge></dd>
        </div>
      </dl>
      {exempt ? <p className="text-sm text-muted-foreground"><Message id="tenant.billingExemptNote" /></p> : null}
      {!exempt && overview.blocked ? <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm font-medium text-destructive"><Message id="tenant.billingBlocked" /></p> : null}
      {!canPurchase ? <p className="text-sm text-muted-foreground"><Message id="tenant.billingPurchaseOwnerOnly" /></p> : null}
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
      {packages.length === 0 ? <EmptyState title={t("tenant.billingNoPackages")} description={t("tenant.billingNoPackagesDescription")} /> : <ul className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
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
      <p className="line-clamp-2 font-medium break-all" title={pkg.name}>{pkg.name}</p>
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

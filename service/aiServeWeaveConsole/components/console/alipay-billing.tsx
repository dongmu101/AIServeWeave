"use client";

import * as React from "react";

import { ErrorMessage } from "@/components/i18n/error-message";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Pager } from "./pager";
import { EmptyState, ErrorState, FormError, LoadingState } from "./states";
import { useConsoleRequest } from "./use-console-request";
import { usePagedResource } from "./use-paged-resource";
import { useResource } from "./use-resource";
import {
  actionOutcome,
  checkRedeemAmount,
  formatPlanPrice,
  newActionId,
  parseAlipayOverview,
  parseAlipayPurchase,
  parseAlipayRedemption,
  parseAlipayRedemptionPage,
  quotaShort,
  type AlipayOverview,
  type AlipayPlan,
  type AlipayRedemption,
  type RedemptionStatus,
} from "@/lib/console/alipay";
import { abortableSleep, formatCredits, pollUntilSettled } from "@/lib/console/billing";
import { ApiError } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";
import type { MessageKey } from "@/lib/i18n/messages";
import type { Translator } from "@/lib/i18n/translate";

const OVERVIEW_PATH = "/admin/v1/billing/alipay";
const REDEMPTIONS_PATH = "/admin/v1/billing/alipay/redemptions";

const STATUS_KEYS: Record<RedemptionStatus, MessageKey> = {
  pending: "tenant.alipayStatusPending",
  succeeded: "tenant.alipayStatusSucceeded",
  failed: "tenant.alipayStatusFailed",
};

function unitLabel(unit: string, t: Translator): string {
  return unit === "COUNT" ? t("tenant.alipayUnitCount") : unit === "POINT" ? t("tenant.alipayUnitPoint") : unit;
}

/** problemFor turns a failed Alipay call into fixed copy. Upstream text is
 * never shown.
 *
 * problemFor 把一次失败的支付宝调用转换为固定文案，从不展示上游文本。 */
function problemFor(failure: unknown, conflict: MessageKey): React.ReactNode {
  if (failure instanceof ApiError && failure.status === 503) {
    return <Message id="tenant.billingPaymentUnavailable" />;
  }
  if (failure instanceof ApiError && failure.status === 409) {
    return <Message id={conflict} />;
  }
  return <ErrorMessage error={failure} />;
}

/** useActionId keeps the id of the click in progress. A click whose answer
 * was lost keeps its id, so pressing again resumes the same request on the
 * control plane; any definite answer retires it.
 *
 * useActionId 保存进行中的那次点击的编号。答复丢失的点击保留其编号，再次按下时控制面
 * 恢复同一个请求；任何确定的答复都会让它作废。 */
function useActionId(): { current: () => string; settle: (failure?: unknown) => void; retrying: boolean } {
  const ref = React.useRef<string | null>(null);
  const [retrying, setRetrying] = React.useState(false);
  const current = React.useCallback(() => {
    ref.current ??= newActionId();
    return ref.current;
  }, []);
  const settle = React.useCallback((failure?: unknown) => {
    const keep = failure !== undefined && actionOutcome(failure) === "unknown";
    if (!keep) {
      ref.current = null;
    }
    setRetrying(keep);
  }, []);
  return { current, settle, retrying };
}

/**
 * AlipayBilling is the Alipay (一人付) section: the quota the control plane
 * read from Alipay, the merchant's plans with a buy button that opens the
 * Alipay purchase entry, and redeeming quota into credits.
 *
 * Nothing here decides that a payment happened. After Alipay sends the buyer
 * back, the section reads the quota a bounded number of times; credits are
 * added only by a redemption the control plane reports as succeeded.
 *
 * AlipayBilling 是支付宝（一人付）分区：控制面从支付宝读到的额度、商户套餐（购买按钮打开
 * 支付宝购买入口），以及把额度兑换为点数。
 *
 * 这里没有任何东西判定一笔付款发生。支付宝把购买者送回之后，分区在有限次数内读取额度；
 * 只有控制面报告成功的兑换才会增加点数。
 */
export function AlipayBilling({ canPurchase, returning, onRedeemed }: { canPurchase: boolean; returning: boolean; onRedeemed: () => void }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const overview = useResource({ method: "GET", path: OVERVIEW_PATH, parse: parseAlipayOverview });
  const [polled, setPolled] = React.useState<AlipayOverview | null>(null);
  const [returnState, setReturnState] = React.useState<"pending" | "done" | null>(returning ? "pending" : null);
  const [historyKey, setHistoryKey] = React.useState(0);
  // A redemption's outcome is kept here, above the section the reload
  // remounts, so it stays on screen while the quota is read again.
  // 兑换结果保存在这里，位于重新加载会重挂载的区块之上，使其在重新读取额度时仍留在屏幕上。
  const [notice, setNotice] = React.useState<React.ReactNode>(null);
  const reloadOverview = overview.reload;

  React.useEffect(() => {
    if (!returning) {
      return;
    }
    // The quota is read a bounded number of times and the first answer is
    // the baseline: the page cannot know what a payment bought, only that
    // the quota Alipay reports has moved.
    // 额度只读取有限次数，第一次的答复作为基准：页面无法知道一笔付款买了什么，只能看到
    // 支付宝报告的额度是否变化。
    const controller = new AbortController();
    let baseline: number | null = null;
    void pollUntilSettled<AlipayOverview>({
      signal: controller.signal,
      sleep: abortableSleep,
      read: (signal) => run({ method: "GET", path: OVERVIEW_PATH, parse: parseAlipayOverview, signal }),
      onResult: (value) => {
        setPolled(value);
        baseline ??= value.access?.remaining ?? null;
      },
      settled: (value) => baseline !== null && value.access !== null && value.access.remaining !== baseline,
      giveUp: (error) => error instanceof ApiError && (error.kind === "not_found" || error.kind === "unauthorized" || error.kind === "forbidden"),
    }).then((outcome) => {
      if (outcome !== "aborted") setReturnState("done");
    });
    return () => controller.abort();
  }, [returning, run]);

  const data = polled ?? overview.data;
  // Read into `polled` so the current figures stay on screen until the new
  // ones arrive; a failed read falls back to the resource's own reload.
  // 读入 `polled`，使当前数据在新数据到达前留在屏幕上；读取失败时退回资源自身的重新加载。
  const refresh = React.useCallback(() => {
    setHistoryKey((value) => value + 1);
    run({ method: "GET", path: OVERVIEW_PATH, parse: parseAlipayOverview })
      .then(setPolled)
      .catch(() => { setPolled(null); reloadOverview(); });
  }, [run, reloadOverview]);
  const settled = React.useCallback((outcome: React.ReactNode, credited: boolean) => {
    setNotice(outcome);
    refresh();
    if (credited) onRedeemed();
  }, [refresh, onRedeemed]);

  return <div className="grid gap-4">
    <Card>
      <CardHeader className="flex flex-wrap items-center justify-between gap-3">
        <CardTitle><Message id="tenant.billingAlipay" /></CardTitle>
        <Button variant="outline" disabled={overview.loading} onClick={() => { setNotice(null); refresh(); }}><Message id="common.refresh" /></Button>
      </CardHeader>
      <CardContent className="grid gap-4">
        <p className="text-sm text-muted-foreground"><Message id="tenant.alipayNote" /></p>
        {returnState ? <p role="status" aria-live="polite" className="rounded-lg border bg-muted/40 px-4 py-3 text-sm">
          <Message id={returnState === "pending" ? "tenant.alipayReturn" : "tenant.alipayReturnTimeout"} />
        </p> : null}
        {notice}
        {overview.error && !data ? <ErrorState message={overview.error} onRetry={overview.reload} />
          : !data ? <LoadingState label={t("tenant.alipayLoading")} rows={3} />
          : <AlipayBody overview={data} canPurchase={canPurchase} onChanged={refresh} onSettled={settled} />}
      </CardContent>
    </Card>
    <RedemptionHistory key={historyKey} />
  </div>;
}

type Settled = (outcome: React.ReactNode, credited: boolean) => void;

function AlipayBody({ overview, canPurchase, onChanged, onSettled }: { overview: AlipayOverview; canPurchase: boolean; onChanged: () => void; onSettled: Settled }) {
  const { t, locale } = useI18n();
  if (!overview.supported) {
    return <EmptyState title={t("tenant.alipayUnsupported")} description={t("tenant.alipayUnsupportedDescription")} />;
  }
  const unit = unitLabel(overview.quotaUnit, t);
  const enabled = overview.canPurchase || overview.canRedeem;
  return <div className="grid gap-4">
    {!enabled ? <p role="status" className="rounded-md border px-3 py-2 text-sm"><Message id="tenant.alipayNotReady" values={{ status: overview.capabilityStatus || "—" }} /></p> : null}
    <dl className="flex flex-wrap items-start gap-x-10 gap-y-3">
      <div className="grid min-w-0 gap-1">
        <dt className="text-xs text-muted-foreground"><Message id="tenant.alipayRemaining" /></dt>
        <dd className="font-heading text-2xl font-semibold break-all">
          {overview.access ? t("tenant.alipayRemainingValue", { amount: new Intl.NumberFormat(locale).format(overview.access.remaining), unit }) : "—"}
        </dd>
      </div>
      <div className="grid gap-1">
        <dt className="text-xs text-muted-foreground"><Message id="tenant.billingCredits" /></dt>
        <dd className="text-sm">{t("tenant.alipayRate", { unit, credits: formatCredits(overview.creditsPerUnitMicro, locale) })}</dd>
      </div>
    </dl>
    {overview.access === null ? <p className="text-sm text-muted-foreground"><Message id="tenant.alipayQuotaUnknown" /></p> : null}
    {overview.pending ? <PendingRedemption redemption={overview.pending} onSettled={onSettled} onChecked={onChanged} /> : null}
    <RedeemForm key={overview.pending?.id ?? "none"} overview={overview} canPurchase={canPurchase} onSettled={onSettled} />
    <PlansSection overview={overview} canPurchase={canPurchase} />
  </div>;
}

/** outcomeNotice is the fixed copy for a settled or pending redemption.
 *
 * outcomeNotice 是已结算或待确认兑换的固定文案。 */
function outcomeNotice(row: AlipayRedemption, t: Translator, locale: string): React.ReactNode {
  if (row.status === "succeeded") {
    return <p role="status" aria-live="polite" className="rounded-lg border bg-muted/40 px-4 py-3 text-sm font-medium">{t("tenant.alipayRedeemed", { credits: formatCredits(row.creditsMicro, locale) })}</p>;
  }
  if (row.status === "failed") {
    return <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
      <Message id={row.reasonCode === "NOW_PAY_QUOTA_INSUFFICIENT" ? "tenant.alipayRedeemFailedQuota" : "tenant.alipayRedeemFailed"} />
    </p>;
  }
  return null;
}

/** PendingRedemption shows a redemption whose outcome is not known yet and
 * lets the tenant ask the control plane to check again. It never retries on
 * its own beyond what the control plane does.
 *
 * PendingRedemption 展示一笔结果尚未知晓的兑换，并允许租户请控制面再次确认。除了控制面
 * 自身的恢复之外，它不会自行重试。 */
function PendingRedemption({ redemption, onSettled, onChecked }: { redemption: AlipayRedemption; onSettled: Settled; onChecked: () => void }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  async function check() {
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      // Resuming only looks up or repeats the same consumption under its own
      // id, so it is safe to send; still, it is sent once per click.
      // 恢复只会以原编号查询或重发同一次核销，因此可以安全发送；仍然每次点击只发一次。
      const row = await run({ method: "POST", path: `${REDEMPTIONS_PATH}/${encodeURIComponent(redemption.id)}/refresh`, parse: parseAlipayRedemption });
      if (row.status === "pending") onChecked(); else onSettled(outcomeNotice(row, t, locale), row.status === "succeeded");
    } catch (failure) {
      setError(problemFor(failure, "tenant.alipayPendingBusy"));
    } finally {
      setPending(false);
    }
  }
  return <div role="status" aria-live="polite" className="grid gap-2 rounded-lg border bg-muted/40 px-4 py-3 text-sm">
    <p><Message id="tenant.alipayRedeemPending" /></p>
    <p className="text-muted-foreground">{t("tenant.alipayRemainingValue", { amount: redemption.amount, unit: unitLabel(redemption.quotaUnit, t) })} · {t("tenant.billingCreditsValue", { credits: formatCredits(redemption.creditsMicro, locale) })} · {formatDateTime(redemption.createdAt, undefined, locale)}</p>
    <div><Button size="sm" variant="outline" onClick={check} disabled={pending} aria-busy={pending}><Message id="tenant.alipayCheckAgain" /></Button></div>
    <FormError message={error} />
  </div>;
}

/** RedeemForm redeems quota into credits. The amount is checked as the
 * control plane will; a known quota that cannot cover it steers the tenant to
 * buy first. A click keeps its action id until a definite answer.
 *
 * RedeemForm 把额度兑换为点数。数量按控制面的规则校验；已知额度不够时引导租户先购买。
 * 一次点击在得到确定答复前一直保留其 action id。 */
function RedeemForm({ overview, canPurchase, onSettled }: { overview: AlipayOverview; canPurchase: boolean; onSettled: Settled }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const count = overview.maxAmount === 1;
  const [raw, setRaw] = React.useState(count ? "1" : "");
  const [pending, setPending] = React.useState(false);
  const [result, setResult] = React.useState<React.ReactNode>(null);
  const action = useActionId();
  const check = checkRedeemAmount(raw, overview);
  const short = check.amount !== null && quotaShort(overview, check.amount);
  const unit = unitLabel(overview.quotaUnit, t);
  const inputId = React.useId();
  const disabled = !canPurchase || !overview.canRedeem || overview.pending !== null;

  async function redeem() {
    if (pending || check.amount === null) return;
    setPending(true);
    setResult(null);
    try {
      // A redemption is a write and is never retried automatically. Pressing
      // again after a lost answer sends the same action id, which resumes it.
      // 兑换是写操作，绝不自动重试。答复丢失后再按一次会发送同一 action id，从而恢复它。
      const row = await run({ method: "POST", path: REDEMPTIONS_PATH, body: { action_id: action.current(), amount: check.amount }, parse: parseAlipayRedemption });
      action.settle();
      // A pending answer shows as the pending notice once the overview is read again.
      // 待确认的答复会在重新读取概览后以待确认提示展示。
      onSettled(outcomeNotice(row, t, locale), row.status === "succeeded");
    } catch (failure) {
      action.settle(failure);
      setResult(<FormError message={problemFor(failure, "tenant.alipayPendingBusy")} />);
    } finally {
      setPending(false);
    }
  }

  let hint: React.ReactNode = null;
  if (!count && raw.trim() !== "") {
    hint = check.problem === null
      ? <p className="text-sm">{t("tenant.billingYouWillReceive", { credits: formatCredits(check.creditsMicro, locale) })}</p>
      : <p className="text-sm text-destructive">{check.problem === "invalid" ? t("tenant.alipayAmountInvalid") : t("tenant.alipayAmountAboveMax", { max: overview.maxAmount })}</p>;
  } else if (count && check.creditsMicro !== null) {
    hint = <p className="text-sm">{t("tenant.billingYouWillReceive", { credits: formatCredits(check.creditsMicro, locale) })}</p>;
  }

  return <div className="grid gap-3 rounded-lg border p-4">
    <p className="font-medium"><Message id="tenant.alipayRedeem" /></p>
    {count ? <p className="text-sm text-muted-foreground"><Message id="tenant.alipayRedeemCount" /></p> : <div className="grid gap-2 sm:max-w-xs">
      <label htmlFor={inputId} className="text-sm font-medium">{t("tenant.alipayRedeemAmount", { unit })}</label>
      <Input id={inputId} inputMode="numeric" autoComplete="off" value={raw} onChange={(event) => setRaw(event.target.value)} disabled={pending || disabled || action.retrying}
        aria-invalid={check.problem !== null && raw.trim() !== "" ? true : undefined} aria-describedby={`${inputId}-range`} />
      <p id={`${inputId}-range`} className="text-xs text-muted-foreground">{t("tenant.alipayRedeemRange", { max: overview.maxAmount })}</p>
    </div>}
    {hint}
    {short ? <p role="status" className="text-sm text-destructive"><Message id="tenant.alipayQuotaShort" /></p> : null}
    {action.retrying ? <p className="text-sm text-muted-foreground"><Message id="tenant.alipayRetryHint" /></p> : null}
    {result}
    <div><Button onClick={redeem} disabled={disabled || pending || check.amount === null || short} aria-busy={pending}><Message id="tenant.alipayRedeem" /></Button></div>
  </div>;
}

/** PlansSection lists the merchant's Alipay plans as Alipay prices them. A
 * buy button opens the Alipay purchase entry; whatever happens there, the
 * page learns only from the quota read afterwards.
 *
 * PlansSection 按支付宝的定价列出商户套餐。购买按钮打开支付宝购买入口；无论那里发生什么，
 * 页面只从之后读到的额度得知结果。 */
function PlansSection({ overview, canPurchase }: { overview: AlipayOverview; canPurchase: boolean }) {
  const { t } = useI18n();
  return <div className="grid gap-3">
    <p className="font-medium"><Message id="tenant.alipayPlans" /></p>
    {overview.plans.length === 0 ? <EmptyState title={t("tenant.alipayNoPlans")} /> : <ul className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
      {overview.plans.map((plan) => <li key={plan.skuId} className="min-w-0"><PlanOffer plan={plan} enabled={canPurchase && overview.canPurchase} /></li>)}
    </ul>}
    {!canPurchase ? <p className="text-sm text-muted-foreground"><Message id="tenant.billingPurchaseOwnerOnly" /></p> : null}
  </div>;
}

function PlanOffer({ plan, enabled }: { plan: AlipayPlan; enabled: boolean }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const action = useActionId();

  async function buy() {
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      // Opening an entry is a write and is never retried automatically; a
      // second press after a lost answer reuses the action id, so the
      // control plane reopens the same entry instead of creating another.
      // 创建入口是写操作，绝不自动重试；答复丢失后的再次按下沿用 action id，控制面重开同一
      // 入口而不是新建一个。
      const result = await run({ method: "POST", path: "/admin/v1/billing/alipay/purchases", body: { action_id: action.current(), sku_id: plan.skuId }, parse: parseAlipayPurchase });
      action.settle();
      window.location.assign(result.purchaseUrl);
    } catch (failure) {
      action.settle(failure);
      setError(problemFor(failure, "tenant.alipayPurchaseUnavailable"));
      setPending(false);
    }
  }

  return <div className="grid h-full content-between gap-3 rounded-lg border p-4">
    <div className="grid gap-1">
      <p className="line-clamp-2 font-medium break-all" title={plan.name}>{plan.name || plan.skuId}</p>
      {plan.quotaAmount > 0 ? <p className="font-heading text-xl font-semibold">{t("tenant.alipayPlanQuota", { amount: new Intl.NumberFormat(locale).format(plan.quotaAmount), unit: unitLabel(plan.quotaUnit, t) })}</p> : null}
      <p className="text-sm text-muted-foreground">{formatPlanPrice(plan, locale)}</p>
    </div>
    {action.retrying ? <p className="text-xs text-muted-foreground"><Message id="tenant.alipayRetryHint" /></p> : null}
    <FormError message={error} />
    <div><Button onClick={buy} disabled={!enabled || pending} aria-busy={pending}><Message id="tenant.alipayBuy" /></Button></div>
  </div>;
}

/** RedemptionHistory lists the tenant's redemptions, newest first.
 *
 * RedemptionHistory 按时间倒序列出租户的兑换。 */
function RedemptionHistory() {
  const { t, locale } = useI18n();
  const history = usePagedResource<AlipayRedemption>({ path: REDEMPTIONS_PATH, filters: {}, pageSize: 20, parse: parseAlipayRedemptionPage });
  return <Card>
    <CardHeader><CardTitle><Message id="tenant.alipayHistory" /></CardTitle></CardHeader>
    <CardContent className="grid gap-3">
      {history.error ? <ErrorState message={history.error} onRetry={history.reload} />
        : history.loading || !history.items ? <LoadingState label={t("tenant.alipayHistory")} rows={2} />
        : history.items.length === 0 ? <EmptyState title={t("tenant.alipayNoHistory")} />
        : <>
          <Table>
            <TableHeader><TableRow>
              <TableHead><Message id="tenant.time" /></TableHead>
              <TableHead className="text-right"><Message id="tenant.alipayQuantity" /></TableHead>
              <TableHead className="text-right"><Message id="tenant.billingCredits" /></TableHead>
              <TableHead><Message id="tenant.status" /></TableHead>
            </TableRow></TableHeader>
            <TableBody>{history.items.map((row) => <TableRow key={row.id}>
              <TableCell className="whitespace-nowrap">{formatDateTime(row.createdAt, undefined, locale)}</TableCell>
              <TableCell className="text-right whitespace-nowrap">{t("tenant.alipayRemainingValue", { amount: row.amount, unit: unitLabel(row.quotaUnit, t) })}</TableCell>
              <TableCell className="text-right whitespace-nowrap">{formatCredits(row.creditsMicro, locale)}</TableCell>
              <TableCell><Badge variant={row.status === "succeeded" ? "secondary" : row.status === "failed" ? "destructive" : "outline"}><Message id={STATUS_KEYS[row.status]} /></Badge></TableCell>
            </TableRow>)}</TableBody>
          </Table>
          <Pager resource={history} loadedCount={history.items.length} />
        </>}
    </CardContent>
  </Card>;
}

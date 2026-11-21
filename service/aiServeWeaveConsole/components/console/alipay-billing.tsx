"use client";

import * as React from "react";

import { ErrorMessage } from "@/components/i18n/error-message";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Pager } from "./pager";
import { EmptyState, ErrorState, FormError, LoadingState } from "./states";
import { useConsoleRequest } from "./use-console-request";
import { usePagedResource } from "./use-paged-resource";
import { useResource } from "./use-resource";
import {
  actionOutcome,
  claimAmount,
  decodePresaleIntent,
  decodeTopUpIntent,
  encodePresaleIntent,
  encodeTopUpIntent,
  formatPlanPrice,
  newActionId,
  parseAlipayOverview,
  parseAlipayPurchase,
  parseAlipayRedemption,
  parseAlipayRedemptionPage,
  planCreditsMicro,
  topUpArrived,
  PRESALE_INTENT_KEY,
  TOP_UP_INTENT_KEY,
  type AlipayOverview,
  type AlipayPlan,
  type AlipayRedemption,
  type PresaleIntent,
  type RedemptionStatus,
  type TopUpIntent,
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

export function unitLabel(unit: string, t: Translator): string {
  return unit === "COUNT" ? t("tenant.alipayUnitCount") : unit === "POINT" ? t("tenant.alipayUnitPoint") : unit;
}

/** The browser's record of a presale awaiting quota from Alipay. Storage may
 * be unavailable; then the purchase is simply not finished on its own and
 * the buyer presses the button again once the quota is there.
 *
 * 浏览器中等待支付宝额度的预售记录。存储可能不可用；那样购买不会自动完成，额度到账后
 * 购买者再按一次按钮即可。 */
export function readPresaleIntent(): PresaleIntent | null {
  try {
    return decodePresaleIntent(window.localStorage.getItem(PRESALE_INTENT_KEY), Date.now());
  } catch {
    return null;
  }
}

export function savePresaleIntent(intent: PresaleIntent) {
  try {
    window.localStorage.setItem(PRESALE_INTENT_KEY, encodePresaleIntent(intent));
  } catch {
    // Without storage the return only shows the quota; see readPresaleIntent.
  }
}

/** forgetPresaleIntent drops the pending presale, so a later Alipay return
 * (for example from buying a plan for credits) does not pay for it.
 *
 * forgetPresaleIntent 丢弃待完成的预售，使之后的支付宝回跳（例如为兑换点数购买套餐）
 * 不会去支付它。 */
export function forgetPresaleIntent() {
  try {
    window.localStorage.removeItem(PRESALE_INTENT_KEY);
  } catch {
    // Nothing stored that could be read either.
  }
}

/** hasPresaleIntent reports whether a presale is waiting for Alipay quota.
 *
 * hasPresaleIntent 报告是否有预售在等待支付宝额度。 */
export function hasPresaleIntent(): boolean {
  return readPresaleIntent() !== null;
}

/** The browser's record of a plan bought for credits, read back on the
 * return from Alipay. Storage may be unavailable; then the quota is booked
 * from the button that offers whatever quota is left.
 *
 * 浏览器中为充值点数购买套餐的记录，从支付宝返回时读回。存储可能不可用；那样额度由
 * 「入账」按钮入账，它会提供剩余的全部额度。 */
function readTopUpIntent(): TopUpIntent | null {
  try {
    return decodeTopUpIntent(window.localStorage.getItem(TOP_UP_INTENT_KEY), Date.now());
  } catch {
    return null;
  }
}

function saveTopUpIntent(intent: TopUpIntent) {
  try {
    window.localStorage.setItem(TOP_UP_INTENT_KEY, encodeTopUpIntent(intent));
  } catch {
    // Without storage the quota is booked from the button; see readTopUpIntent.
  }
}

/** forgetTopUpIntent drops the pending top-up, so a later return does not
 * book it a second time.
 *
 * forgetTopUpIntent 丢弃待完成的充值，使之后的回跳不会再次入账。 */
export function forgetTopUpIntent() {
  try {
    window.localStorage.removeItem(TOP_UP_INTENT_KEY);
  } catch {
    // Nothing stored that could be read either.
  }
}

/** TOP_UP_POLL_ATTEMPTS bounds the wait for a bought plan to reach the quota
 * to about two minutes, as for a presale.
 *
 * TOP_UP_POLL_ATTEMPTS 把等待所购套餐到达额度的时间限定在两分钟左右，与预售相同。 */
const TOP_UP_POLL_ATTEMPTS = 40;

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
export function useActionId(): { current: () => string; settle: (failure?: unknown) => void; retrying: boolean } {
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
 * AlipayBilling is the Alipay (一人付) section, where tenants buy credits:
 * each plan is shown as the credits it books, and Alipay quota — the units
 * 一人付 sells — never appears as such. Buying opens the Alipay purchase
 * entry; back on this page, once the control plane reads the plan in the
 * quota, all of the quota is redeemed into credits in one redemption. Quota
 * the page did not book (the wait ran out, storage was unavailable, a plan
 * bought in Alipay directly) is offered as credits awaiting a click.
 *
 * Nothing here decides that a payment happened. Credits are added only by a
 * redemption the control plane reports as succeeded.
 *
 * AlipayBilling 是支付宝（一人付）分区，租户在这里购买点数：每档套餐按它入账的点数展示，
 * 支付宝额度——一人付出售的单位——从不直接出现。购买打开支付宝购买入口；回到本页、控制面
 * 读到额度中已有该套餐之后，全部额度以一笔兑换入账为点数。页面没有入账的额度（等待超时、
 * 存储不可用、直接在支付宝购买的套餐）显示为待入账点数，点击即可入账。
 *
 * 这里没有任何东西判定一笔付款发生。只有控制面报告成功的兑换才会增加点数。
 */
export function AlipayBilling({ canPurchase, returning, onRedeemed }: { canPurchase: boolean; returning: boolean; onRedeemed: () => void }) {
  const { t, locale } = useI18n();
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

  // The return runs once per page load; what it calls afterwards is read
  // through a ref, so a new callback identity cannot start it again and
  // redeem twice.
  // 回跳处理每次页面加载只运行一次；之后调用的函数经 ref 读取，回调换了身份也不会让它
  // 重新开始、重复兑换。
  const after = React.useRef({ settled, refresh, canPurchase, t, locale });
  React.useEffect(() => {
    after.current = { settled, refresh, canPurchase, t, locale };
  });

  React.useEffect(() => {
    if (!returning) {
      return;
    }
    // Quota bought for a presale is spent on the presales tab, never here.
    // 为预售购买的额度在预售分区使用，绝不在这里入账。
    const presale = hasPresaleIntent();
    const intent = presale ? null : readTopUpIntent();
    // Without an intent the first answer is the baseline: the page cannot
    // know what a payment bought, only that the quota has moved.
    // 没有意图时第一次的答复作为基准：页面无法知道一笔付款买了什么，只能看到额度是否变化。
    const controller = new AbortController();
    let baseline: number | null = null;
    let last: AlipayOverview | null = null;
    void pollUntilSettled<AlipayOverview>({
      signal: controller.signal,
      sleep: abortableSleep,
      maxAttempts: intent ? TOP_UP_POLL_ATTEMPTS : undefined,
      read: (signal) => run({ method: "GET", path: OVERVIEW_PATH, parse: parseAlipayOverview, signal }),
      onResult: (value) => {
        setPolled(value);
        last = value;
        baseline ??= value.access?.remaining ?? null;
      },
      settled: (value) => intent ? topUpArrived(value, intent)
        : baseline !== null && value.access !== null && value.access.remaining !== baseline,
      giveUp: (error) => error instanceof ApiError && (error.kind === "not_found" || error.kind === "unauthorized" || error.kind === "forbidden"),
    }).then(async (outcome) => {
      if (outcome === "aborted") {
        return;
      }
      const { settled: done, refresh: reread, canPurchase: allowed, t: tr, locale: lc } = after.current;
      if (presale) {
        setReturnState("done");
        return;
      }
      forgetTopUpIntent();
      const amount = outcome === "settled" && last !== null && allowed ? claimAmount(last) : 0;
      if (amount === 0) {
        // Arrived quota that could not be booked here stays on offer as
        // credits awaiting a click.
        // 在这里无法入账的已到账额度，继续显示为待入账点数，点击即可入账。
        setReturnState(outcome === "settled" ? null : "done");
        return;
      }
      try {
        // Sent once and never retried, like the button's redemption. The
        // intent is dropped first, so a reload does not redeem again.
        // 只发送一次、绝不重试，与按钮发起的兑换相同。意图先被丢弃，因此刷新不会再次兑换。
        const row = await run({ method: "POST", path: REDEMPTIONS_PATH, body: { action_id: newActionId(), amount }, parse: parseAlipayRedemption });
        setReturnState(null);
        done(outcomeNotice(row, tr, lc), row.status === "succeeded");
      } catch (failure) {
        setReturnState(null);
        setNotice(<FormError message={problemFor(failure, "tenant.alipayPendingBusy")} />);
        reread();
      }
    });
    return () => controller.abort();
  }, [returning, run]);

  const data = polled ?? overview.data;

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
          : <AlipayBody overview={data} canPurchase={canPurchase} busy={returnState === "pending"} onChanged={refresh} onSettled={settled} />}
      </CardContent>
    </Card>
    <RedemptionHistory key={historyKey} />
  </div>;
}

type Settled = (outcome: React.ReactNode, credited: boolean) => void;

function AlipayBody({ overview, canPurchase, busy, onChanged, onSettled }: { overview: AlipayOverview; canPurchase: boolean; busy: boolean; onChanged: () => void; onSettled: Settled }) {
  const { t } = useI18n();
  if (!overview.supported) {
    return <EmptyState title={t("tenant.alipayUnsupported")} description={t("tenant.alipayUnsupportedDescription")} />;
  }
  const enabled = overview.canPurchase || overview.canRedeem;
  return <div className="grid gap-4">
    {!enabled ? <p role="status" className="rounded-md border px-3 py-2 text-sm"><Message id="tenant.alipayNotReady" values={{ status: overview.capabilityStatus || "—" }} /></p> : null}
    {overview.access === null ? <p className="text-sm text-muted-foreground"><Message id="tenant.alipayQuotaUnknown" /></p> : null}
    {overview.pending ? <PendingRedemption redemption={overview.pending} onSettled={onSettled} onChecked={onChanged} /> : null}
    {!busy && claimAmount(overview) > 0 ? <UnclaimedQuota key={overview.access?.remaining ?? 0} overview={overview} canPurchase={canPurchase} onSettled={onSettled} /> : null}
    <PlansSection overview={overview} canPurchase={canPurchase} />
  </div>;
}

/** outcomeNotice is the fixed copy for a settled or pending redemption.
 *
 * outcomeNotice 是已结算或待确认兑换的固定文案。 */
function outcomeNotice(row: AlipayRedemption, t: Translator, locale: string): React.ReactNode {
  // A redemption that paid for a presale order booked no credits.
  // 支付预售订单的兑换没有入账点数。
  if (row.presaleOrderId !== "") {
    return row.status === "succeeded" ? <p role="status" aria-live="polite" className="rounded-lg border bg-muted/40 px-4 py-3 text-sm font-medium"><Message id="tenant.presaleAlipayPaid" /></p>
      : row.status === "failed" ? <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive"><Message id="tenant.presaleAlipayFailed" /></p> : null;
  }
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
    <p className="text-muted-foreground">{redemption.presaleOrderId !== "" ? t("tenant.alipayPresaleRow", { id: redemption.presaleOrderId }) : t("tenant.billingCreditsValue", { credits: formatCredits(redemption.creditsMicro, locale) })} · {formatDateTime(redemption.createdAt, undefined, locale)}</p>
    <div><Button size="sm" variant="outline" onClick={check} disabled={pending} aria-busy={pending}><Message id="tenant.alipayCheckAgain" /></Button></div>
    <FormError message={error} />
  </div>;
}

/** UnclaimedQuota offers quota already in Alipay that is not yet booked as
 * credits — a return whose wait ran out, a browser without storage, or a plan
 * bought in Alipay directly — and books all of it in one redemption. A click
 * keeps its action id until a definite answer.
 *
 * UnclaimedQuota 提供已在支付宝、尚未入账为点数的额度——等待超时的回跳、没有存储的浏览器，
 * 或直接在支付宝购买的套餐——并以一笔兑换全部入账。一次点击在得到确定答复前一直保留其
 * action id。 */
function UnclaimedQuota({ overview, canPurchase, onSettled }: { overview: AlipayOverview; canPurchase: boolean; onSettled: Settled }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [pending, setPending] = React.useState(false);
  const [result, setResult] = React.useState<React.ReactNode>(null);
  const action = useActionId();
  // Fixed when shown (the parent remounts this on a new quota), so a retry
  // of a click whose answer was lost sends the same amount.
  // 显示时即固定（额度变化时父组件会重挂载本组件），使答复丢失后的重试发送相同数量。
  const [amount] = React.useState(() => claimAmount(overview));
  const credits = amount * overview.creditsPerUnitMicro;

  async function claim() {
    if (pending) return;
    setPending(true);
    setResult(null);
    try {
      // A redemption is a write and is never retried automatically. Pressing
      // again after a lost answer sends the same action id, which resumes it.
      // 兑换是写操作，绝不自动重试。答复丢失后再按一次会发送同一 action id，从而恢复它。
      const row = await run({ method: "POST", path: REDEMPTIONS_PATH, body: { action_id: action.current(), amount }, parse: parseAlipayRedemption });
      action.settle();
      onSettled(outcomeNotice(row, t, locale), row.status === "succeeded");
    } catch (failure) {
      action.settle(failure);
      setResult(<FormError message={problemFor(failure, "tenant.alipayPendingBusy")} />);
    } finally {
      setPending(false);
    }
  }

  return <div role="status" className="grid gap-3 rounded-lg border bg-muted/40 p-4">
    <p className="text-sm">{t("tenant.alipayUnclaimed", { credits: formatCredits(credits, locale) })}</p>
    {action.retrying ? <p className="text-sm text-muted-foreground"><Message id="tenant.alipayRetryHint" /></p> : null}
    {result}
    {!canPurchase ? <p className="text-sm text-muted-foreground"><Message id="tenant.billingPurchaseOwnerOnly" /></p> : null}
    <div><Button onClick={claim} disabled={!canPurchase || pending} aria-busy={pending}><Message id="tenant.alipayClaim" /></Button></div>
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
      {overview.plans.map((plan) => <li key={plan.skuId} className="min-w-0"><PlanOffer plan={plan} overview={overview} enabled={canPurchase && overview.canPurchase} /></li>)}
    </ul>}
    {!canPurchase ? <p className="text-sm text-muted-foreground"><Message id="tenant.billingPurchaseOwnerOnly" /></p> : null}
  </div>;
}

function PlanOffer({ plan, overview, enabled }: { plan: AlipayPlan; overview: AlipayOverview; enabled: boolean }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const action = useActionId();
  const credits = planCreditsMicro(plan, overview);

  async function buy() {
    if (pending) return;
    setPending(true);
    setError(null);
    // Buying a plan for credits is not paying for a presale: a return from
    // this purchase must not finish one left pending earlier.
    // 为兑换点数购买套餐不是支付预售：这次购买的回跳不能完成之前遗留的预售。
    forgetPresaleIntent();
    // Remembered before leaving, so the return books the plan as credits.
    // 离开前记下，使回跳把该套餐入账为点数。
    saveTopUpIntent({ quota: plan.quotaAmount, baseline: overview.access?.remaining ?? 0, savedAt: Date.now() });
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
      if (actionOutcome(failure) === "definite") forgetTopUpIntent();
      setError(problemFor(failure, "tenant.alipayPurchaseUnavailable"));
      setPending(false);
    }
  }

  return <div className="grid h-full content-between gap-3 rounded-lg border p-4">
    <div className="grid gap-1">
      {credits !== null ? <p className="font-heading text-xl font-semibold">{t("tenant.billingCreditsValue", { credits: formatCredits(credits, locale) })}</p>
        : <p className="line-clamp-2 font-medium break-all" title={plan.name}>{plan.name || plan.skuId}</p>}
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
              <TableHead className="text-right"><Message id="tenant.billingCredits" /></TableHead>
              <TableHead><Message id="tenant.status" /></TableHead>
            </TableRow></TableHeader>
            <TableBody>{history.items.map((row) => <TableRow key={row.id}>
              <TableCell className="whitespace-nowrap">{formatDateTime(row.createdAt, undefined, locale)}</TableCell>
              <TableCell className="text-right whitespace-nowrap">{row.presaleOrderId !== "" ? t("tenant.alipayPresaleRow", { id: row.presaleOrderId }) : formatCredits(row.creditsMicro, locale)}</TableCell>
              <TableCell><Badge variant={row.status === "succeeded" ? "secondary" : row.status === "failed" ? "destructive" : "outline"}><Message id={STATUS_KEYS[row.status]} /></Badge></TableCell>
            </TableRow>)}</TableBody>
          </Table>
          <Pager resource={history} loadedCount={history.items.length} />
        </>}
    </CardContent>
  </Card>;
}

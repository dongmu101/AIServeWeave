import type { Page } from "./contract.ts";
import { ApiError } from "./errors.ts";
import { boolean, count, httpsLink, integer, list, page, record, required, text, timestamp } from "./billing.ts";

/**
 * Alipay (一人付) credits: a tenant buys a count or point package in Alipay,
 * and redeems quota into credits through the control plane. Nothing here
 * decides that a payment happened — 一人付 reports none. The page shows the
 * quota the control plane read, and a redemption counts only once the control
 * plane reports it succeeded.
 *
 * 支付宝（一人付）点数：租户在支付宝购买次数或积分套餐，经控制面把额度兑换为点数。这里
 * 没有任何东西判定一笔付款发生——一人付不报告逐笔付款。页面展示控制面读到的额度，兑换只有
 * 在控制面报告成功之后才算数。
 */

/** QuotaUnit is the unit of an Alipay quantity package.
 *
 * QuotaUnit 是支付宝数量套餐的单位。 */
export type QuotaUnit = "COUNT" | "POINT";

/** RedemptionStatus mirrors model.AlipayRedemption*.
 *
 * RedemptionStatus 镜像 model.AlipayRedemption*。 */
export type RedemptionStatus = "pending" | "succeeded" | "failed";

/** AlipayPlan mirrors types.AlipayPlan. Price is Alipay's decimal string,
 * shown as it is.
 *
 * AlipayPlan 镜像 types.AlipayPlan。Price 是支付宝的十进制字符串，原样展示。 */
export interface AlipayPlan {
  skuId: string;
  name: string;
  price: string;
  currency: string;
  quotaUnit: string;
  quotaAmount: number;
}

/** AlipayRedemption mirrors types.AlipayRedemption.
 *
 * AlipayRedemption 镜像 types.AlipayRedemption。 */
export interface AlipayRedemption {
  id: string;
  actionId: string;
  amount: number;
  quotaUnit: string;
  creditsMicro: number;
  presaleOrderId: string;
  status: RedemptionStatus;
  reasonCode: string;
  createdAt: string;
  updatedAt: string;
}

/** AlipayOverview mirrors types.AlipayOverviewResponse. `access` is null when
 * the control plane could not read the quota.
 *
 * AlipayOverview 镜像 types.AlipayOverviewResponse。控制面无法读取额度时 `access` 为 null。 */
export interface AlipayOverview {
  capabilityStatus: string;
  productName: string;
  billingMode: string;
  quotaUnit: string;
  supported: boolean;
  canPurchase: boolean;
  canRedeem: boolean;
  plans: AlipayPlan[];
  access: { total: number; used: number; remaining: number } | null;
  creditsPerUnitMicro: number;
  maxAmount: number;
  pending: AlipayRedemption | null;
}

/** AlipayPurchase mirrors types.AlipayPurchaseResponse.
 *
 * AlipayPurchase 镜像 types.AlipayPurchaseResponse。 */
export interface AlipayPurchase {
  id: string;
  purchaseUrl: string;
}

const STATUSES: readonly RedemptionStatus[] = ["pending", "succeeded", "failed"];

function fail(): never {
  throw new ApiError("contract");
}

function parsePlan(value: unknown): AlipayPlan {
  const source = record(value);
  return {
    skuId: required(source, "sku_id"),
    name: text(source, "name"),
    price: text(source, "price"),
    currency: text(source, "currency"),
    quotaUnit: text(source, "quota_unit"),
    quotaAmount: count(source, "quota_amount"),
  };
}

/** parseAlipayRedemption validates one types.AlipayRedemption.
 *
 * parseAlipayRedemption 校验一个 types.AlipayRedemption。 */
export function parseAlipayRedemption(value: unknown): AlipayRedemption {
  const source = record(value);
  const status = source.status;
  if (!(STATUSES as readonly unknown[]).includes(status)) {
    fail();
  }
  const amount = count(source, "amount");
  if (amount < 1) {
    fail();
  }
  return {
    id: required(source, "id"),
    actionId: required(source, "action_id"),
    amount,
    quotaUnit: text(source, "quota_unit"),
    creditsMicro: count(source, "credits_micro"),
    presaleOrderId: source.presale_order_id === undefined ? "" : required(source, "presale_order_id"),
    status: status as RedemptionStatus,
    reasonCode: text(source, "reason_code"),
    createdAt: timestamp(source, "created_at"),
    updatedAt: timestamp(source, "updated_at"),
  };
}

/** parseAlipayRedemptionPage validates GET /admin/v1/billing/alipay/redemptions.
 *
 * parseAlipayRedemptionPage 校验 GET /admin/v1/billing/alipay/redemptions。 */
export function parseAlipayRedemptionPage(value: unknown): Page<AlipayRedemption> {
  return page(value, parseAlipayRedemption);
}

/** parseAlipayOverview validates GET /admin/v1/billing/alipay.
 *
 * parseAlipayOverview 校验 GET /admin/v1/billing/alipay。 */
export function parseAlipayOverview(value: unknown): AlipayOverview {
  const source = record(value);
  let access: AlipayOverview["access"] = null;
  if (source.access !== null && source.access !== undefined) {
    const a = record(source.access);
    access = { total: integer(a, "total"), used: integer(a, "used"), remaining: integer(a, "remaining") };
  }
  const pending = source.pending === null || source.pending === undefined ? null : parseAlipayRedemption(source.pending);
  if (pending !== null && pending.status !== "pending") {
    fail();
  }
  return {
    capabilityStatus: text(source, "capability_status"),
    productName: text(source, "product_name"),
    billingMode: text(source, "billing_mode"),
    quotaUnit: text(source, "quota_unit"),
    supported: boolean(source, "supported"),
    canPurchase: boolean(source, "can_purchase"),
    canRedeem: boolean(source, "can_redeem"),
    plans: list(source.plans, parsePlan),
    access,
    creditsPerUnitMicro: count(source, "credits_per_unit_micro"),
    maxAmount: count(source, "max_amount"),
    pending,
  };
}

/** parseAlipayPurchase validates POST /admin/v1/billing/alipay/purchases.
 * The entry is navigated to, so it must be an absolute https URL.
 *
 * parseAlipayPurchase 校验 POST /admin/v1/billing/alipay/purchases。页面会跳转到该入口，
 * 因此它必须是绝对 https URL。 */
export function parseAlipayPurchase(value: unknown): AlipayPurchase {
  const source = record(value);
  return { id: required(source, "id"), purchaseUrl: httpsLink(source, "purchase_url") };
}

/** newActionId mints the id of one click. A retry of the same click after a
 * lost answer reuses it, so the control plane resumes rather than repeats.
 *
 * newActionId 生成一次点击的编号。丢失答复后重试同一次点击时沿用它，使控制面恢复而不是重复。 */
export function newActionId(random: () => string = () => crypto.randomUUID()): string {
  return random();
}

/** ActionOutcome is how a write ended, for deciding whether its action id may
 * be reused: "unknown" (no answer, a timeout or a 5xx) keeps it, anything
 * definite discards it.
 *
 * ActionOutcome 是一次写操作的结束方式，用于决定其 action id 能否沿用："unknown"（无答复、
 * 超时或 5xx）保留它，任何确定的结果都丢弃它。 */
export type ActionOutcome = "unknown" | "definite";

/** actionOutcome classifies a failed write.
 *
 * actionOutcome 对一次失败的写操作分类。 */
export function actionOutcome(error: unknown): ActionOutcome {
  if (error instanceof ApiError && (error.kind === "network" || error.kind === "timeout" || error.kind === "server" || error.status >= 500)) {
    return "unknown";
  }
  return "definite";
}

/** AmountCheck is a typed redemption amount and what it redeems into.
 *
 * AmountCheck 是输入的兑换数量及其可兑换的点数。 */
export type AmountCheck = { amount: number; creditsMicro: number; problem: null } | { amount: null; creditsMicro: null; problem: "invalid" | "above_max" };

/** checkRedeemAmount validates a typed amount as the control plane will: a
 * whole number from 1 to maxAmount (1 for a count package).
 *
 * checkRedeemAmount 按控制面的规则校验输入的数量：1 到 maxAmount 之间的整数（次数包为 1）。 */
export function checkRedeemAmount(raw: string, overview: Pick<AlipayOverview, "maxAmount" | "creditsPerUnitMicro">): AmountCheck {
  const trimmed = raw.trim();
  if (!/^[1-9][0-9]{0,15}$/.test(trimmed)) {
    return { amount: null, creditsMicro: null, problem: "invalid" };
  }
  const amount = Number(trimmed);
  if (!Number.isSafeInteger(amount) || amount > overview.maxAmount) {
    return { amount: null, creditsMicro: null, problem: "above_max" };
  }
  const creditsMicro = amount * overview.creditsPerUnitMicro;
  if (!Number.isSafeInteger(creditsMicro)) {
    return { amount: null, creditsMicro: null, problem: "above_max" };
  }
  return { amount, creditsMicro, problem: null };
}

/** quotaShort reports whether the known quota cannot cover amount, which is
 * when the page steers the buyer to Alipay first.
 *
 * quotaShort 报告已知额度是否不足以覆盖 amount，此时页面先引导购买者去支付宝购买。 */
export function quotaShort(overview: Pick<AlipayOverview, "access">, amount: number): boolean {
  return overview.access !== null && overview.access.remaining < amount;
}

/** isAlipayReturn reports whether Alipay sent the buyer back: the configured
 * return URL carries `alipay_return`. It only starts a bounded wait for the
 * quota to move; its value is never read as an outcome.
 *
 * isAlipayReturn 报告支付宝是否把购买者送回：配置的回跳地址带有 `alipay_return`。它只会
 * 开始一段有限的额度等待；其取值从不被当作结果。 */
export function isAlipayReturn(params: Record<string, string | string[] | undefined>): boolean {
  return params.alipay_return !== undefined;
}

/** formatPlanPrice shows Alipay's price string with its currency, formatted
 * when it is a plain decimal and as given otherwise.
 *
 * formatPlanPrice 带币种展示支付宝的价格字符串；是普通十进制数时格式化，否则原样展示。 */
export function formatPlanPrice(plan: Pick<AlipayPlan, "price" | "currency">, locale: string): string {
  if (/^\d+(\.\d+)?$/.test(plan.price) && /^[A-Z]{3}$/.test(plan.currency)) {
    try {
      return new Intl.NumberFormat(locale, { style: "currency", currency: plan.currency }).format(Number(plan.price));
    } catch {
      // An unknown currency falls through to the raw text.
    }
  }
  return plan.currency ? `${plan.price} ${plan.currency}` : plan.price;
}

/** PresaleTopUp says how a presale costing `amount` Alipay units can be paid
 * now: from the quota already there ("enough", also when the quota could not
 * be read and the control plane will judge), by first buying `plan` in Alipay
 * ("plan"), or not at all because no plan covers the shortfall ("none").
 * 一人付 buys only whole plans, never an arbitrary amount, so a plan must
 * cover the shortfall by itself.
 *
 * PresaleTopUp 说明一个需要 `amount` 个支付宝单位的预售此刻如何支付：用已有额度（"enough"，
 * 额度读不到时也是，由控制面判断）、先在支付宝购买 `plan`（"plan"），或者因为没有套餐能补足
 * 差额而无法支付（"none"）。一人付只能整档购买套餐、不能购买任意数量，因此一档套餐必须
 * 单独补足差额。 */
export type PresaleTopUp =
  | { kind: "enough" }
  | { kind: "plan"; plan: AlipayPlan; shortfall: number }
  | { kind: "none"; shortfall: number };

function planPrice(plan: AlipayPlan): number {
  return /^\d+(\.\d+)?$/.test(plan.price) ? Number(plan.price) : Number.POSITIVE_INFINITY;
}

/** presaleTopUp picks the cheapest plan in the overview's unit whose quota
 * alone covers what the tenant lacks, the smaller quota winning a tie.
 *
 * presaleTopUp 选出与概览同单位、单档额度即可补足差额的最便宜套餐，价格相同时额度较小者优先。 */
export function presaleTopUp(overview: Pick<AlipayOverview, "access" | "plans" | "quotaUnit" | "canPurchase">, amount: number): PresaleTopUp {
  if (!quotaShort(overview, amount) || overview.access === null) {
    return { kind: "enough" };
  }
  const shortfall = amount - overview.access.remaining;
  if (!overview.canPurchase) {
    return { kind: "none", shortfall };
  }
  let best: AlipayPlan | null = null;
  for (const plan of overview.plans) {
    if ((plan.quotaUnit !== "" && plan.quotaUnit !== overview.quotaUnit) || plan.quotaAmount < shortfall) {
      continue;
    }
    if (best === null || planPrice(plan) < planPrice(best) || (planPrice(plan) === planPrice(best) && plan.quotaAmount < best.quotaAmount)) {
      best = plan;
    }
  }
  return best === null ? { kind: "none", shortfall } : { kind: "plan", plan: best, shortfall };
}

/** PresaleIntent remembers, across the trip to Alipay, which presale the
 * tenant is buying quota for. It lives in the browser only: it lets the page
 * finish the purchase once the quota arrives, and proves nothing — the
 * control plane still checks the quota, the price and the place.
 *
 * PresaleIntent 在前往支付宝的往返期间记住商户为哪个预售购买额度。它只存在于浏览器：
 * 使页面在额度到账后完成购买，本身不证明任何事——控制面仍会核对额度、价格与名额。 */
export interface PresaleIntent {
  presaleId: string;
  amount: number;
  savedAt: number;
}

/** PRESALE_INTENT_KEY is the localStorage key of the pending PresaleIntent.
 *
 * PRESALE_INTENT_KEY 是待完成 PresaleIntent 的 localStorage 键。 */
export const PRESALE_INTENT_KEY = "aisw.alipay.presale-intent";

/** PRESALE_INTENT_TTL_MS bounds how long after leaving for Alipay a return
 * still completes the purchase on its own.
 *
 * PRESALE_INTENT_TTL_MS 限定离开前往支付宝之后多久内的回跳仍会自动完成购买。 */
export const PRESALE_INTENT_TTL_MS = 60 * 60 * 1000;

/** encodePresaleIntent serializes an intent for storage.
 *
 * encodePresaleIntent 把意图序列化以便存储。 */
export function encodePresaleIntent(intent: PresaleIntent): string {
  return JSON.stringify({ presale_id: intent.presaleId, amount: intent.amount, saved_at: intent.savedAt });
}

/** decodePresaleIntent reads a stored intent, refusing anything malformed,
 * from the future or older than PRESALE_INTENT_TTL_MS.
 *
 * decodePresaleIntent 读取已存储的意图，拒绝格式不符、来自未来或早于
 * PRESALE_INTENT_TTL_MS 的内容。 */
export function decodePresaleIntent(raw: string | null, now: number): PresaleIntent | null {
  if (raw === null) {
    return null;
  }
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof value !== "object" || value === null) {
    return null;
  }
  const { presale_id: presaleId, amount, saved_at: savedAt } = value as Record<string, unknown>;
  if (typeof presaleId !== "string" || !/^[A-Za-z0-9_-]{1,64}$/.test(presaleId)) {
    return null;
  }
  if (typeof amount !== "number" || !Number.isSafeInteger(amount) || amount < 1 || amount > 99_999_999) {
    return null;
  }
  if (typeof savedAt !== "number" || !Number.isFinite(savedAt) || savedAt > now || now - savedAt > PRESALE_INTENT_TTL_MS) {
    return null;
  }
  return { presaleId, amount, savedAt };
}

/** presaleQuotaArrived reports whether the quota now covers the intent.
 *
 * presaleQuotaArrived 报告额度此刻是否已足够支付该意图。 */
export function presaleQuotaArrived(overview: Pick<AlipayOverview, "access">, intent: Pick<PresaleIntent, "amount">): boolean {
  return overview.access !== null && overview.access.remaining >= intent.amount;
}

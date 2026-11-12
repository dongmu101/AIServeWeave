import { ApiError } from "./errors.ts";
import { EZFP_CURRENCY, MAX_PRODUCT_ID_BYTES, MAX_TEXT_RUNES, optionalBoolean, parseMoneyInput, type CheckoutOrder } from "./billing.ts";
import { MAX_ACCOUNT_PRICE_MINOR, parseAccountKind, type AccountKind, type AccountSale } from "./account-sales.ts";
import { isSupportedCurrency } from "./currencies.ts";
import { tenantNodeOwner } from "./node-access.ts";
import { withPayChoice, type EzfpPayType, type PayChoice } from "./payment-choice.ts";

/** MAX_PRESALES and MAX_PRESALE_CAPACITY mirror model.MaxPresales and
 * model.MaxPresaleCapacity.
 *
 * MAX_PRESALES 与 MAX_PRESALE_CAPACITY 镜像 model.MaxPresales 与
 * model.MaxPresaleCapacity。 */
export const MAX_PRESALES = 100;
export const MAX_PRESALE_CAPACITY = 10_000;

/** MAX_ALIPAY_AMOUNT mirrors billing.MaxAlipayPointAmount.
 *
 * MAX_ALIPAY_AMOUNT 镜像 billing.MaxAlipayPointAmount。 */
export const MAX_ALIPAY_AMOUNT = 99_999_999;

/** AccountPresale mirrors types.AccountPresale: one month of an account of
 * `kind`, sold before an account is chosen. `available` says whether it can
 * be checked out now. `dodoProductId`, `capacity` and `outstanding` exist
 * only on the operator view ("" and null on the tenant's).
 *
 * AccountPresale 镜像 types.AccountPresale：在选定账号之前预售的某一类型账号的一个月。
 * `available` 表示此刻能否结账。`dodoProductId`、`capacity` 与 `outstanding` 只在运维
 * 视图中存在（租户视图中为 "" 与 null）。
 *
 * `available` means listed, with capacity, and payable through Dodo. An item
 * may be `available: false` and `ezfpAvailable: true` (a CNY presale sold
 * through EZFP only): that is not sold out. `ezfpAvailable` is false when a
 * control plane from before EZFP does not send it.
 *
 * `available` 表示已上架、有名额且可经 Dodo 支付。商品可以 `available: false` 而
 * `ezfpAvailable: true`（只经易支付出售的 CNY 预售）：这不是售罄。早于易支付的控制面
 * 不发送 `ezfpAvailable`，此时为 false。
 *
 * `alipayAmount` is how many Alipay units (counts or points) one month costs
 * from a tenant's Alipay quota, 0 when it cannot be paid that way;
 * `alipayAvailable` says whether it can be now.
 *
 * `alipayAmount` 是用租户支付宝额度支付一个月所需的单位数（次数或积分），不能这样支付时
 * 为 0；`alipayAvailable` 表示此刻能否这样支付。 */
export interface AccountPresale {
  id: string; kind: AccountKind; name: string; priceMinor: number; currency: string;
  active: boolean; available: boolean; ezfpAvailable: boolean; alipayAmount: number; alipayAvailable: boolean; dodoProductId: string;
  capacity: number | null; outstanding: number | null;
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}
function text(value: unknown, max: number): string {
  if (typeof value !== "string" || !value || new TextEncoder().encode(value).length > max) throw new ApiError("contract");
  return value;
}
function counted(value: unknown, max: number): number | null {
  if (value === undefined) return null;
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0 || value > max) throw new ApiError("contract");
  return value;
}

export function parseAccountPresale(value: unknown): AccountPresale {
  const row = object(value);
  if (typeof row.active !== "boolean" || typeof row.available !== "boolean") throw new ApiError("contract");
  const priceMinor = row.price_minor;
  if (typeof priceMinor !== "number" || !Number.isSafeInteger(priceMinor) || priceMinor <= 0 || priceMinor > MAX_ACCOUNT_PRICE_MINOR) throw new ApiError("contract");
  const currency = row.currency;
  if (typeof currency !== "string" || !/^[A-Z]{3}$/.test(currency)) throw new ApiError("contract");
  const capacity = counted(row.capacity, MAX_PRESALE_CAPACITY);
  // A payment that settles after its hold may push the waiting count past
  // the capacity, so it is bounded only loosely.
  // 超过占用时长才完成的付款可能让等待数超过名额，因此只做宽松的上界检查。
  const outstanding = counted(row.outstanding, Number.MAX_SAFE_INTEGER);
  if ((capacity === null) !== (outstanding === null)) throw new ApiError("contract");
  // A control plane from before Alipay presales sends neither field.
  // 早于支付宝预售的控制面不发送这两个字段。
  const alipayAmount = counted(row.alipay_amount, MAX_ALIPAY_AMOUNT) ?? 0;
  if (row.alipay_available !== undefined && typeof row.alipay_available !== "boolean") throw new ApiError("contract");
  return {
    id: text(row.id, 32), kind: parseAccountKind(row.kind, false) as AccountKind, name: text(row.name, 512),
    priceMinor, currency, active: row.active, available: row.available,
    ezfpAvailable: optionalBoolean(row, "ezfp_available", false),
    alipayAmount, alipayAvailable: row.alipay_available === true,
    dodoProductId: row.dodo_product_id === undefined || row.dodo_product_id === "" ? "" : text(row.dodo_product_id, MAX_PRODUCT_ID_BYTES),
    capacity, outstanding,
  };
}

export function parseAccountPresales(value: unknown): AccountPresale[] {
  const source = object(value);
  if (!Array.isArray(source.items) || source.items.length > MAX_PRESALES) throw new ApiError("contract");
  const rows = source.items.map(parseAccountPresale);
  if (new Set(rows.map(row => row.id)).size !== rows.length) throw new ApiError("contract");
  return rows;
}

/** presaleCheckoutBody pins the price the tenant confirmed, so a price
 * change in between is refused with 409 rather than charged. `provider` and
 * `pay_type` are added only when the tenant chose EZFP.
 *
 * presaleCheckoutBody 固定商户确认的价格，期间若改价会以 409 拒绝而不是照新价收费。
 * 只有商户选择易支付时才附加 `provider` 与 `pay_type`。 */
export function presaleCheckoutBody(presale: Pick<AccountPresale, "priceMinor" | "currency">, choice?: PayChoice): { expected_price_minor: number; currency: string; provider?: "ezfp"; pay_type?: EzfpPayType } {
  return withPayChoice({ expected_price_minor: presale.priceMinor, currency: presale.currency }, choice);
}

/** presaleAlipayBody pins the Alipay amount the tenant confirmed and the
 * click's action id, so a change in between is refused with 409 and a retry
 * of a lost answer resumes the same order.
 *
 * presaleAlipayBody 固定商户确认的支付宝额度数量与点击的 action id，使期间改动以 409
 * 拒绝，并使丢失答复后的重试恢复同一订单。 */
export function presaleAlipayBody(presale: Pick<AccountPresale, "alipayAmount">, actionId: string): { action_id: string; expected_amount: number } {
  return { action_id: actionId, expected_amount: presale.alipayAmount };
}

/** PresaleDraftProblem names the first invalid field of a presale draft.
 *
 * PresaleDraftProblem 指出预售草稿中第一个无效字段。 */
export type PresaleDraftProblem = "name" | "kind" | "currency" | "price" | "product" | "capacity" | "alipay";

export interface PresaleRequest {
  kind: AccountKind; name: string; price_minor: number; currency: string;
  dodo_product_id: string; capacity: number; active: boolean; alipay_amount: number;
}

/** presaleRequest validates an operator's draft the way SaveAccountPresale
 * does: the price is typed in the currency's major unit and the capacity
 * as a whole number.
 *
 * presaleRequest 按 SaveAccountPresale 的规则校验运维草稿：价格以货币主单位输入，
 * 名额为整数。支付宝额度数量留空即 0（不支持支付宝）；只要填了，Dodo 产品 ID 可以留空；
 * 币种为 CNY 时 Dodo 产品 ID 同样可以留空，此时只能经易支付购买。 */
export function presaleRequest(draft: { kind: AccountKind | ""; name: string; price: string; currency: string; productId: string; capacity: string; active: boolean; alipayAmount?: string }):
  { request: PresaleRequest; problem: null } | { request: null; problem: PresaleDraftProblem } {
  if (draft.kind === "") return { request: null, problem: "kind" };
  const name = draft.name.trim();
  if (name === "" || [...name].length > MAX_TEXT_RUNES) return { request: null, problem: "name" };
  const code = draft.currency.trim().toUpperCase();
  if (!isSupportedCurrency(code)) return { request: null, problem: "currency" };
  const minor = parseMoneyInput(draft.price, code);
  if (minor === null || minor <= 0 || minor > MAX_ACCOUNT_PRICE_MINOR) return { request: null, problem: "price" };
  const rawAlipay = (draft.alipayAmount ?? "").trim();
  if (rawAlipay !== "" && !/^\d{1,8}$/.test(rawAlipay)) return { request: null, problem: "alipay" };
  const alipayAmount = rawAlipay === "" ? 0 : Number(rawAlipay);
  if (alipayAmount > MAX_ALIPAY_AMOUNT) return { request: null, problem: "alipay" };
  const product = draft.productId.trim();
  if ((product === "" && alipayAmount === 0 && code !== EZFP_CURRENCY) || new TextEncoder().encode(product).length > MAX_PRODUCT_ID_BYTES) return { request: null, problem: "product" };
  const raw = draft.capacity.trim();
  const capacity = /^\d{1,5}$/.test(raw) ? Number(raw) : 0;
  if (capacity < 1 || capacity > MAX_PRESALE_CAPACITY) return { request: null, problem: "capacity" };
  return { request: { kind: draft.kind, name, price_minor: minor, currency: code, dodo_product_id: product, capacity, active: draft.active, alipay_amount: alipayAmount }, problem: null };
}

/** presaleAwaiting reports whether a presale order is paid and still waits
 * for an account, as the control plane's PresaleAwaiting decides.
 *
 * presaleAwaiting 按控制面 PresaleAwaiting 的判断，报告预售订单是否已付款且仍在等待
 * 分配账号。 */
export function presaleAwaiting(order: Pick<CheckoutOrder, "presaleId" | "status" | "accountExpiresAt">): boolean {
  return order.presaleId !== "" && order.status === "paid" && order.accountExpiresAt === null;
}

/** assignableAccounts lists the inventory an operator may offer a presale
 * order: the presale's kind, not a tenant-owned node, and free — unheld,
 * expired, or already the order tenant's own (which extends its month). The
 * control plane decides again under lock, including checkout holds and node
 * dedication, which this list cannot see.
 *
 * assignableAccounts 列出运维可以分配给预售订单的库存：与预售同类型、不是商户自有节点，
 * 且空闲——无人持有、已到期，或已属于该订单的商户（此时顺延其期限）。控制面会在锁下再次
 * 判断，包括本列表看不到的结账占用与节点专属。 */
export function assignableAccounts(accounts: readonly AccountSale[], kind: AccountKind, tenantId: string, now: number): AccountSale[] {
  return accounts.filter(account => account.kind === kind && tenantNodeOwner(account.nodeId) === null &&
    (account.tenantId === "" || account.tenantId === tenantId || account.expiresAt === null || Date.parse(account.expiresAt) <= now));
}

/** parseRefundCreated validates the part of types.RefundRequest the presale
 * refund action needs: that the control plane opened one, and its state.
 *
 * parseRefundCreated 校验预售退款操作所需的 types.RefundRequest 部分：控制面已经创建
 * 退款，以及其状态。 */
export function parseRefundCreated(value: unknown): { id: string; status: string } {
  const row = object(value);
  return { id: text(row.id, 32), status: text(row.status, 16) };
}

/** MAX_REFUND_REASON_RUNES mirrors the control plane's refundText bound.
 *
 * MAX_REFUND_REASON_RUNES 镜像控制面 refundText 的上限。 */
export const MAX_REFUND_REASON_RUNES = 500;

/** refundReason trims an operator's reason, or returns null when it is
 * empty or too long for the control plane.
 *
 * refundReason 去除运维填写的原因首尾空白；为空或超过控制面上限时返回 null。 */
export function refundReason(raw: string): string | null {
  const reason = raw.trim();
  if (reason === "" || [...reason].length > MAX_REFUND_REASON_RUNES || new TextEncoder().encode(reason).length > 1024) return null;
  return reason;
}

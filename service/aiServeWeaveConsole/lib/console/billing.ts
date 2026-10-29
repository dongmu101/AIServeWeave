import { ApiError } from "./errors.ts";
import type { Page } from "./contract.ts";

/**
 * This module mirrors the control plane's prepaid credit billing API
 * (internal/types/billing.go): the tenant overview, checkout and portal links,
 * the ledger and order lists, and the operator's packages, model prices and
 * per-tenant billing state. It also holds the unit arithmetic the pages need,
 * because both units are easy to get wrong in a browser: credits travel as
 * micro-credit integers (one million to a credit) and money in the currency's
 * minor unit, whose size depends on the currency (JPY has none, USD has two).
 *
 * The Console never talks to the payment provider. Checkout and the customer
 * portal are links the control plane creates and hands back; this module only
 * checks that such a link is an https URL before a page navigates to it.
 *
 * 本模块镜像控制面的预充值点数计费 API（internal/types/billing.go）：租户概览、结账
 * 与客户门户链接、账目与订单列表，以及运维侧的充值包、模型单价与按租户的计费状态。
 * 页面需要的单位换算也放在这里，因为两种单位在浏览器里都容易算错：点数以 micro 点
 * 整数传输（一百万为一点），金额以货币最小单位传输，而最小单位的大小取决于货币
 * （JPY 没有小数位，USD 有两位）。
 *
 * Console 从不与支付服务商通信。结账与客户门户都是控制面创建并交回的链接；本模块
 * 只在页面跳转前确认这样的链接是 https URL。
 */

/** MICRO_PER_CREDIT mirrors model.MicroPerCredit.
 *
 * MICRO_PER_CREDIT 镜像 model.MicroPerCredit。 */
export const MICRO_PER_CREDIT = 1_000_000;

/** MAX_PRICE_MICRO_PER_MTOK mirrors model.MaxPriceMicroPerMTok: one million
 * credits per million tokens.
 *
 * MAX_PRICE_MICRO_PER_MTOK 镜像 model.MaxPriceMicroPerMTok：每百万 token 一百万点。 */
export const MAX_PRICE_MICRO_PER_MTOK = 1_000_000 * MICRO_PER_CREDIT;

/** MAX_TEXT_RUNES mirrors the control plane's 128-character bound on a package
 * name, a model alias and an adjustment reason.
 *
 * MAX_TEXT_RUNES 镜像控制面对充值包名称、模型别名与调整原因的 128 字符上限。 */
export const MAX_TEXT_RUNES = 128;

/** MAX_PRODUCT_ID_BYTES mirrors the control plane's bound on a provider
 * product id.
 *
 * MAX_PRODUCT_ID_BYTES 镜像控制面对服务商产品 id 的长度上限。 */
export const MAX_PRODUCT_ID_BYTES = 64;

/** DEFAULT_PRICE_MODEL mirrors model.DefaultPriceModel: the alias whose price
 * applies to every alias without one of its own.
 *
 * DEFAULT_PRICE_MODEL 镜像 model.DefaultPriceModel：其单价适用于所有没有单独定价的
 * 别名。 */
export const DEFAULT_PRICE_MODEL = "*";

/** BillingMode is a tenant's billing mode.
 *
 * BillingMode 是租户的计费模式。 */
export type BillingMode = "prepaid" | "exempt";

/** BILLING_MODES is the closed set of modes, in display order.
 *
 * BILLING_MODES 是计费模式的封闭集合，按展示顺序排列。 */
export const BILLING_MODES: readonly BillingMode[] = ["prepaid", "exempt"];

/** PackageKind says how a package is priced: a fixed bundle, or an amount the
 * buyer chooses within a range.
 *
 * PackageKind 表示充值包的定价方式：固定组合，或由购买者在一个范围内自选金额。 */
export type PackageKind = "fixed" | "custom";

/** PACKAGE_KINDS is the closed set of package kinds.
 *
 * PACKAGE_KINDS 是充值包类型的封闭集合。 */
export const PACKAGE_KINDS: readonly PackageKind[] = ["fixed", "custom"];

/** LEDGER_KINDS is the vocabulary the control plane writes (model.LedgerKind*),
 * offered as the ledger filter.
 *
 * LEDGER_KINDS 是控制面写入的词汇表（model.LedgerKind*），用作账目筛选项。 */
export const LEDGER_KINDS = ["grant", "topup", "usage", "refund", "dispute", "dispute_reversal", "adjust"] as const;

/** ORDER_STATUSES is the vocabulary the control plane writes
 * (model.OrderStatus*), offered as the order filter.
 *
 * ORDER_STATUSES 是控制面写入的词汇表（model.OrderStatus*），用作订单筛选项。 */
export const ORDER_STATUSES = ["pending", "paid", "failed", "refunded", "disputed"] as const;

/** CreditPackage mirrors types.CreditPackage. Fields that do not apply to a
 * package's kind are zero, as the Go side omits them. `dodoProductId` is ""
 * on the tenant view, which never carries it.
 *
 * CreditPackage 镜像 types.CreditPackage。不适用于该类型的字段为零，与 Go 侧省略它们
 * 一致。租户视图从不携带 `dodoProductId`，此时为 ""。 */
export interface CreditPackage {
  id: string;
  name: string;
  kind: PackageKind;
  creditsMicro: number;
  priceMinor: number;
  currency: string;
  creditsPerMinorUnitMicro: number;
  minAmountMinor: number;
  maxAmountMinor: number;
  active: boolean;
  sortOrder: number;
  dodoProductId: string;
  createdAt: string;
  updatedAt: string;
}

/** BillingOverview mirrors types.BillingOverviewResponse.
 *
 * BillingOverview 镜像 types.BillingOverviewResponse。 */
export interface BillingOverview {
  mode: BillingMode;
  balanceMicro: number;
  blocked: boolean;
  hasCustomer: boolean;
  packages: CreditPackage[];
}

/** CheckoutResult mirrors types.CheckoutResponse.
 *
 * CheckoutResult 镜像 types.CheckoutResponse。 */
export interface CheckoutResult {
  orderId: string;
  checkoutUrl: string;
}

/** LedgerEntry mirrors types.LedgerEntry. `tenantId` is "" on the tenant view.
 *
 * LedgerEntry 镜像 types.LedgerEntry。租户视图上 `tenantId` 为 ""。 */
export interface LedgerEntry {
  id: string;
  tenantId: string;
  kind: string;
  amountMicro: number;
  balanceAfterMicro: number;
  ref: string;
  createdAt: string;
}

/** CheckoutOrder mirrors types.CheckoutOrder. `tenantId` is "" on the tenant
 * view and `paymentId` is "" until the provider reports a payment.
 *
 * CheckoutOrder 镜像 types.CheckoutOrder。租户视图上 `tenantId` 为 ""，服务商报告
 * 付款之前 `paymentId` 为 ""。 */
export interface CheckoutOrder {
  id: string;
  tenantId: string;
  packageId: string;
  amountMinor: number;
  currency: string;
  creditsMicro: number;
  refundedMinor: number;
  status: string;
  paymentId: string;
  createdAt: string;
  updatedAt: string;
}

/** ModelPrice mirrors types.ModelPrice.
 *
 * ModelPrice 镜像 types.ModelPrice。 */
export interface ModelPrice {
  model: string;
  inputMicroPerMTok: number;
  outputMicroPerMTok: number;
  updatedBy: string;
  updatedAt: string;
}

/** BillingTenant mirrors types.BillingTenant.
 *
 * BillingTenant 镜像 types.BillingTenant。 */
export interface BillingTenant {
  id: string;
  name: string;
  status: string;
  mode: BillingMode;
  balanceMicro: number;
  blocked: boolean;
  createdAt: string;
}

function fail(): never {
  throw new ApiError("contract");
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    fail();
  }
  return value as Record<string, unknown>;
}

function required(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (typeof value !== "string" || value === "") {
    fail();
  }
  return value;
}

/** text reads a string that is always present but may be empty.
 *
 * text 读取一个总会出现、但可能为空的字符串。 */
function text(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (typeof value !== "string") {
    fail();
  }
  return value;
}

/** optionalText reads an `omitempty` string field; absent becomes "".
 *
 * optionalText 读取一个 `omitempty` 的字符串字段；缺席时为 ""。 */
function optionalText(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (value === undefined) {
    return "";
  }
  if (typeof value !== "string") {
    fail();
  }
  return value;
}

function timestamp(source: Record<string, unknown>, key: string): string {
  const value = required(source, key);
  if (Number.isNaN(Date.parse(value))) {
    fail();
  }
  return value;
}

/** integer reads a required safe integer, which may be negative: a ledger
 * amount is negative for a deduction.
 *
 * integer 读取一个必填的安全整数，可以为负：扣减的账目金额就是负数。 */
function integer(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  if (typeof value !== "number" || !Number.isSafeInteger(value)) {
    fail();
  }
  return value;
}

/** optionalCount reads an `omitempty` non-negative integer; absent is zero,
 * matching Go's encoding.
 *
 * optionalCount 读取一个 `omitempty` 的非负整数；缺席即为零，与 Go 的编码一致。 */
function optionalCount(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  if (value === undefined || value === null) {
    return 0;
  }
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
    fail();
  }
  return value;
}

function count(source: Record<string, unknown>, key: string): number {
  const value = integer(source, key);
  if (value < 0) {
    fail();
  }
  return value;
}

function boolean(source: Record<string, unknown>, key: string): boolean {
  const value = source[key];
  if (typeof value !== "boolean") {
    fail();
  }
  return value;
}

function mode(source: Record<string, unknown>, key: string): BillingMode {
  const value = source[key];
  if (!(BILLING_MODES as readonly unknown[]).includes(value)) {
    fail();
  }
  return value as BillingMode;
}

/** list reads an array field; absent or null is empty, because a Go nil slice
 * encodes as null.
 *
 * list 读取一个数组字段；缺席或 null 视为空，因为 Go 的 nil 切片会编码成 null。 */
function list<T>(value: unknown, parse: (item: unknown) => T): T[] {
  if (value === undefined || value === null) {
    return [];
  }
  if (!Array.isArray(value)) {
    fail();
  }
  return value.map(parse);
}

/** page reads a `{items, next_cursor}` envelope. An absent or empty cursor
 * means the last page, mirroring contract.ts's parsePage.
 *
 * page 读取一个 `{items, next_cursor}` 信封。游标缺席或为空表示末页，与 contract.ts
 * 的 parsePage 一致。 */
function page<T>(value: unknown, parse: (item: unknown) => T): Page<T> {
  const source = record(value);
  const cursor = source.next_cursor;
  if (cursor !== undefined && cursor !== null && typeof cursor !== "string") {
    fail();
  }
  return {
    items: list(source.items, parse),
    nextCursor: typeof cursor === "string" && cursor !== "" ? cursor : null,
  };
}

/** httpsLink reads a link the page will navigate to. Anything but an absolute
 * https URL is a contract violation: navigating to a `javascript:` or
 * relative value handed back by a compromised upstream would run in this
 * origin.
 *
 * httpsLink 读取一个页面将跳转过去的链接。除绝对 https URL 外一律视为契约违例：
 * 跳转到一个被攻破的上游交回的 `javascript:` 或相对地址，会在本源中执行。 */
function httpsLink(source: Record<string, unknown>, key: string): string {
  const value = required(source, key);
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    fail();
  }
  if (parsed.protocol !== "https:") {
    fail();
  }
  return value;
}

/** parseCreditPackage validates one types.CreditPackage.
 *
 * parseCreditPackage 校验一个 types.CreditPackage。 */
export function parseCreditPackage(value: unknown): CreditPackage {
  const source = record(value);
  const kind = source.kind;
  if (!(PACKAGE_KINDS as readonly unknown[]).includes(kind)) {
    fail();
  }
  return {
    id: required(source, "id"),
    name: required(source, "name"),
    kind: kind as PackageKind,
    creditsMicro: optionalCount(source, "credits_micro"),
    priceMinor: optionalCount(source, "price_minor"),
    currency: required(source, "currency"),
    creditsPerMinorUnitMicro: optionalCount(source, "credits_per_minor_unit_micro"),
    minAmountMinor: optionalCount(source, "min_amount_minor"),
    maxAmountMinor: optionalCount(source, "max_amount_minor"),
    active: boolean(source, "active"),
    sortOrder: integer(source, "sort_order"),
    dodoProductId: optionalText(source, "dodo_product_id"),
    createdAt: timestamp(source, "created_at"),
    updatedAt: timestamp(source, "updated_at"),
  };
}

/** parseCreditPackages validates GET /operator/v1/billing/packages.
 *
 * parseCreditPackages 校验 GET /operator/v1/billing/packages。 */
export function parseCreditPackages(value: unknown): CreditPackage[] {
  return list(record(value).items, parseCreditPackage);
}

/** parseBillingOverview validates GET /admin/v1/billing.
 *
 * parseBillingOverview 校验 GET /admin/v1/billing。 */
export function parseBillingOverview(value: unknown): BillingOverview {
  const source = record(value);
  return {
    mode: mode(source, "mode"),
    balanceMicro: integer(source, "balance_micro"),
    blocked: boolean(source, "blocked"),
    hasCustomer: boolean(source, "has_customer"),
    packages: list(source.packages, parseCreditPackage),
  };
}

/** parseCheckout validates POST /admin/v1/billing/checkout.
 *
 * parseCheckout 校验 POST /admin/v1/billing/checkout。 */
export function parseCheckout(value: unknown): CheckoutResult {
  const source = record(value);
  return { orderId: required(source, "order_id"), checkoutUrl: httpsLink(source, "checkout_url") };
}

/** parsePortalLink validates POST /admin/v1/billing/portal and returns the
 * one-time link.
 *
 * parsePortalLink 校验 POST /admin/v1/billing/portal 并返回一次性链接。 */
export function parsePortalLink(value: unknown): string {
  return httpsLink(record(value), "url");
}

/** parseLedgerEntry validates one types.LedgerEntry.
 *
 * parseLedgerEntry 校验一个 types.LedgerEntry。 */
export function parseLedgerEntry(value: unknown): LedgerEntry {
  const source = record(value);
  return {
    id: required(source, "id"),
    tenantId: optionalText(source, "tenant_id"),
    kind: required(source, "kind"),
    amountMicro: integer(source, "amount_micro"),
    balanceAfterMicro: integer(source, "balance_after_micro"),
    ref: text(source, "ref"),
    createdAt: timestamp(source, "created_at"),
  };
}

/** parseLedgerPage validates one page of the ledger.
 *
 * parseLedgerPage 校验一页账目。 */
export function parseLedgerPage(value: unknown): Page<LedgerEntry> {
  return page(value, parseLedgerEntry);
}

function parseOrder(value: unknown): CheckoutOrder {
  const source = record(value);
  return {
    id: required(source, "id"),
    tenantId: optionalText(source, "tenant_id"),
    packageId: required(source, "package_id"),
    amountMinor: count(source, "amount_minor"),
    currency: required(source, "currency"),
    creditsMicro: count(source, "credits_micro"),
    refundedMinor: count(source, "refunded_minor"),
    status: required(source, "status"),
    paymentId: optionalText(source, "payment_id"),
    createdAt: timestamp(source, "created_at"),
    updatedAt: timestamp(source, "updated_at"),
  };
}

/** parseOrderPage validates one page of checkout orders.
 *
 * parseOrderPage 校验一页结账订单。 */
export function parseOrderPage(value: unknown): Page<CheckoutOrder> {
  return page(value, parseOrder);
}

/** parseModelPrice validates one types.ModelPrice.
 *
 * parseModelPrice 校验一个 types.ModelPrice。 */
export function parseModelPrice(value: unknown): ModelPrice {
  const source = record(value);
  return {
    model: required(source, "model"),
    inputMicroPerMTok: count(source, "input_micro_per_mtok"),
    outputMicroPerMTok: count(source, "output_micro_per_mtok"),
    updatedBy: text(source, "updated_by"),
    updatedAt: timestamp(source, "updated_at"),
  };
}

/** parseModelPrices validates GET /operator/v1/billing/model-prices.
 *
 * parseModelPrices 校验 GET /operator/v1/billing/model-prices。 */
export function parseModelPrices(value: unknown): ModelPrice[] {
  return list(record(value).items, parseModelPrice);
}

function parseBillingTenant(value: unknown): BillingTenant {
  const source = record(value);
  return {
    id: required(source, "id"),
    name: text(source, "name"),
    status: required(source, "status"),
    mode: mode(source, "mode"),
    balanceMicro: integer(source, "balance_micro"),
    blocked: boolean(source, "blocked"),
    createdAt: timestamp(source, "created_at"),
  };
}

/** parseBillingTenants validates one page of GET /operator/v1/billing/tenants.
 *
 * parseBillingTenants 校验 GET /operator/v1/billing/tenants 的一页。 */
export function parseBillingTenants(value: unknown): Page<BillingTenant> {
  return page(value, parseBillingTenant);
}

/** currencyDigits returns how many minor-unit digits a currency has, as
 * Intl knows it: 2 for USD, 0 for JPY, 3 for BHD. An unknown code falls back
 * to 2 rather than throwing, so one odd row cannot break a page.
 *
 * currencyDigits 返回某货币最小单位的小数位数，以 Intl 的认识为准：USD 为 2，JPY 为
 * 0，BHD 为 3。未知代码回退为 2 而不是抛错，免得一行异常数据拖垮整页。 */
export function currencyDigits(currency: string): number {
  try {
    return new Intl.NumberFormat("en", { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
  } catch {
    return 2;
  }
}

/** formatMoney renders an amount in minor units as currency.
 *
 * formatMoney 把以最小单位计的金额渲染为货币。 */
export function formatMoney(minor: number, currency: string, locale: string): string {
  const digits = currencyDigits(currency);
  const major = minor / 10 ** digits;
  try {
    return new Intl.NumberFormat(locale, {
      style: "currency",
      currency,
      minimumFractionDigits: digits,
      maximumFractionDigits: digits,
    }).format(major);
  } catch {
    return `${major.toFixed(digits)} ${currency}`;
  }
}

/** formatCredits renders micro-credits as credits with up to six fraction
 * digits, so a sub-credit usage charge is never displayed as zero.
 *
 * formatCredits 把 micro 点渲染为点数，最多六位小数，这样不足一点的用量扣费不会被
 * 显示成零。 */
export function formatCredits(micro: number, locale: string): string {
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 6 }).format(micro / MICRO_PER_CREDIT);
}

/** formatSignedCredits is formatCredits with an explicit sign, for ledger
 * amounts.
 *
 * formatSignedCredits 是带显式正负号的 formatCredits，用于账目金额。 */
export function formatSignedCredits(micro: number, locale: string): string {
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 6, signDisplay: "exceptZero" }).format(micro / MICRO_PER_CREDIT);
}

/** parseScaled turns a decimal string into an integer scaled by 10^digits,
 * using string arithmetic so "0.1" is exactly 100000 micro rather than a
 * float that rounds. It returns null for anything that is not a plain
 * decimal, has more fraction digits than the scale, or does not fit a safe
 * integer. Both "." and "," are accepted as the decimal separator.
 *
 * parseScaled 把十进制字符串转换为放大 10^digits 倍的整数，采用字符串运算，因此
 * "0.1" 恰好是 100000 micro，而不是一个会舍入的浮点数。不是普通十进制数、小数位
 * 多于精度或超出安全整数范围的输入，一律返回 null。"." 与 "," 都可作小数点。 */
export function parseScaled(raw: string, digits: number, options: { allowNegative?: boolean } = {}): number | null {
  const match = /^([+-]?)(\d+)(?:[.,](\d*))?$/.exec(raw.trim());
  if (!match) {
    return null;
  }
  const [, sign, whole, fraction = ""] = match;
  if (sign === "-" && !options.allowNegative) {
    return null;
  }
  if (fraction.length > digits) {
    return null;
  }
  const scaled = Number(`${whole}${fraction.padEnd(digits, "0")}`);
  if (!Number.isSafeInteger(scaled)) {
    return null;
  }
  return sign === "-" ? -scaled : scaled;
}

/** formatScaled is parseScaled's inverse, for filling an edit form with a
 * stored value: no grouping, no trailing zeros, "." as the separator.
 *
 * formatScaled 是 parseScaled 的逆运算，用于把已存储的值填回编辑表单：不分组、
 * 不带末尾零、以 "." 为小数点。 */
export function formatScaled(value: number, digits: number): string {
  const sign = value < 0 ? "-" : "";
  const abs = Math.abs(value);
  const scale = 10 ** digits;
  const whole = Math.floor(abs / scale);
  const fraction = String(abs % scale).padStart(digits, "0").replace(/0+$/, "");
  return `${sign}${whole}${fraction === "" ? "" : `.${fraction}`}`;
}

/** parseCreditsInput reads a credit amount typed in credits and returns
 * micro-credits, or null.
 *
 * parseCreditsInput 读取以点为单位输入的点数并返回 micro 点，无效时返回 null。 */
export function parseCreditsInput(raw: string, options: { allowNegative?: boolean } = {}): number | null {
  return parseScaled(raw, 6, options);
}

/** parseMoneyInput reads an amount typed in the currency's major unit and
 * returns minor units, or null — "12.5" USD is 1250, "1000" JPY is 1000, and
 * "12.5" JPY is invalid because JPY has no minor unit.
 *
 * parseMoneyInput 读取以货币主单位输入的金额并返回最小单位，无效时返回 null——
 * "12.5" USD 为 1250，"1000" JPY 为 1000，而 "12.5" JPY 无效，因为 JPY 没有更小的单位。 */
export function parseMoneyInput(raw: string, currency: string): number | null {
  return parseScaled(raw, currencyDigits(currency));
}

/** customCredits returns the micro-credits an amount buys from a custom
 * package, or null when the product overflows a safe integer.
 *
 * customCredits 返回某金额从自定义充值包可购得的 micro 点；乘积超出安全整数时返回 null。 */
export function customCredits(pkg: Pick<CreditPackage, "creditsPerMinorUnitMicro">, amountMinor: number): number | null {
  const credits = amountMinor * pkg.creditsPerMinorUnitMicro;
  return Number.isSafeInteger(credits) ? credits : null;
}

/** AmountProblem says why a custom amount cannot be bought.
 *
 * AmountProblem 说明一个自定义金额为何无法购买。 */
export type AmountProblem = "invalid" | "below_min" | "above_max";

/** customAmountProblem checks a typed amount against a custom package's range,
 * the same check model.CreditPackage.Credits makes on the server.
 *
 * customAmountProblem 依充值包的范围检查输入的金额，与服务端
 * model.CreditPackage.Credits 做的检查相同。 */
export function customAmountProblem(
  pkg: Pick<CreditPackage, "currency" | "minAmountMinor" | "maxAmountMinor" | "creditsPerMinorUnitMicro">,
  raw: string
): { amountMinor: number; problem: null } | { amountMinor: null; problem: AmountProblem } {
  const amountMinor = parseMoneyInput(raw, pkg.currency);
  if (amountMinor === null || amountMinor <= 0 || customCredits(pkg, amountMinor) === null) {
    return { amountMinor: null, problem: "invalid" };
  }
  if (amountMinor < pkg.minAmountMinor) {
    return { amountMinor: null, problem: "below_min" };
  }
  if (amountMinor > pkg.maxAmountMinor) {
    return { amountMinor: null, problem: "above_max" };
  }
  return { amountMinor, problem: null };
}

/** checkoutBody builds types.CheckoutRequest. A fixed package never carries
 * an amount: the control plane rejects one, because its price is the
 * package's, not the buyer's.
 *
 * checkoutBody 构造 types.CheckoutRequest。固定充值包从不携带金额：控制面会拒绝它，
 * 因为价格属于充值包而不是购买者。 */
export function checkoutBody(pkg: Pick<CreditPackage, "id" | "kind">, amountMinor: number | null): { package_id: string; amount_minor?: number } {
  if (pkg.kind === "fixed" || amountMinor === null) {
    return { package_id: pkg.id };
  }
  return { package_id: pkg.id, amount_minor: amountMinor };
}

/** PackageDraft is the operator's package form, as typed: credits in credits
 * and money in major units.
 *
 * PackageDraft 是运维的充值包表单原样输入：点数以点计，金额以主单位计。 */
export interface PackageDraft {
  name: string;
  kind: PackageKind;
  dodoProductId: string;
  currency: string;
  credits: string;
  price: string;
  creditsPerMinorUnit: string;
  minAmount: string;
  maxAmount: string;
  active: boolean;
  sortOrder: string;
}

/** PackageRequest mirrors types.CreditPackageRequest, which rejects unknown
 * fields, so this shape carries exactly its keys.
 *
 * PackageRequest 镜像 types.CreditPackageRequest；该请求拒绝未知字段，因此这里恰好
 * 只携带它的那些键。 */
export interface PackageRequest {
  name: string;
  kind: PackageKind;
  dodo_product_id: string;
  credits_micro: number;
  price_minor: number;
  currency: string;
  credits_per_minor_unit_micro: number;
  min_amount_minor: number;
  max_amount_minor: number;
  active: boolean;
  sort_order: number;
}

/** PackageProblem names the first field a package draft gets wrong.
 *
 * PackageProblem 指出充值包草稿里第一个出错的字段。 */
export type PackageProblem = "name" | "product" | "currency" | "credits" | "price" | "rate" | "range" | "sort_order";

/** emptyPackageDraft is a new package form.
 *
 * emptyPackageDraft 是一份新建充值包的表单。 */
export function emptyPackageDraft(): PackageDraft {
  return {
    name: "", kind: "fixed", dodoProductId: "", currency: "USD", credits: "", price: "",
    creditsPerMinorUnit: "", minAmount: "", maxAmount: "", active: true, sortOrder: "0",
  };
}

/** packageDraft fills the form from a stored package, so editing starts from
 * what the server holds.
 *
 * packageDraft 用已存储的充值包填充表单，让编辑从服务端持有的值开始。 */
export function packageDraft(pkg: CreditPackage): PackageDraft {
  const digits = currencyDigits(pkg.currency);
  const fixed = pkg.kind === "fixed";
  return {
    name: pkg.name,
    kind: pkg.kind,
    dodoProductId: pkg.dodoProductId,
    currency: pkg.currency,
    credits: fixed ? formatScaled(pkg.creditsMicro, 6) : "",
    price: fixed ? formatScaled(pkg.priceMinor, digits) : "",
    creditsPerMinorUnit: fixed ? "" : formatScaled(pkg.creditsPerMinorUnitMicro, 6),
    minAmount: fixed ? "" : formatScaled(pkg.minAmountMinor, digits),
    maxAmount: fixed ? "" : formatScaled(pkg.maxAmountMinor, digits),
    active: pkg.active,
    sortOrder: String(pkg.sortOrder),
  };
}

function runes(value: string): number {
  return Array.from(value).length;
}

/** packageRequest validates a draft with the rules packageFromInput applies
 * and builds the request. The fields of the other kind are sent as zero,
 * which the control plane requires rather than tolerates.
 *
 * packageRequest 按 packageFromInput 的规则校验草稿并构造请求。另一种类型的字段以零
 * 发送——这是控制面的要求，而不只是它能容忍的写法。 */
export function packageRequest(draft: PackageDraft): { request: PackageRequest; problem: null } | { request: null; problem: PackageProblem } {
  const name = draft.name.trim();
  const product = draft.dodoProductId.trim();
  const currency = draft.currency.trim();
  if (name === "" || runes(name) > MAX_TEXT_RUNES || new TextEncoder().encode(name).length > 4 * MAX_TEXT_RUNES) return { request: null, problem: "name" };
  if (product === "" || new TextEncoder().encode(product).length > MAX_PRODUCT_ID_BYTES) return { request: null, problem: "product" };
  if (!/^[A-Z]{3}$/.test(currency)) return { request: null, problem: "currency" };
  const sortOrder = parseScaled(draft.sortOrder, 0, { allowNegative: true });
  const base = {
    name, kind: draft.kind, dodo_product_id: product, currency, active: draft.active,
    credits_micro: 0, price_minor: 0, credits_per_minor_unit_micro: 0, min_amount_minor: 0, max_amount_minor: 0,
  };
  if (draft.kind === "fixed") {
    const credits = parseCreditsInput(draft.credits);
    if (credits === null || credits <= 0) return { request: null, problem: "credits" };
    const price = parseMoneyInput(draft.price, currency);
    if (price === null || price <= 0) return { request: null, problem: "price" };
    if (sortOrder === null) return { request: null, problem: "sort_order" };
    return { request: { ...base, credits_micro: credits, price_minor: price, sort_order: sortOrder }, problem: null };
  }
  const rate = parseCreditsInput(draft.creditsPerMinorUnit);
  if (rate === null || rate <= 0) return { request: null, problem: "rate" };
  const min = parseMoneyInput(draft.minAmount, currency);
  const max = parseMoneyInput(draft.maxAmount, currency);
  if (min === null || max === null || min <= 0 || max < min || !Number.isSafeInteger(max * rate)) {
    return { request: null, problem: "range" };
  }
  if (sortOrder === null) return { request: null, problem: "sort_order" };
  return {
    request: { ...base, credits_per_minor_unit_micro: rate, min_amount_minor: min, max_amount_minor: max, sort_order: sortOrder },
    problem: null,
  };
}

/** PriceProblem names what a model price draft gets wrong.
 *
 * PriceProblem 指出模型单价草稿哪里出错。 */
export type PriceProblem = "model" | "price";

/** modelPriceRequest validates a price typed in credits per million tokens
 * and builds types.ModelPriceRequest in micro-credits.
 *
 * modelPriceRequest 校验以「点/百万 token」输入的单价，并以 micro 点构造
 * types.ModelPriceRequest。 */
export function modelPriceRequest(draft: { model: string; input: string; output: string }):
  | { request: { model: string; input_micro_per_mtok: number; output_micro_per_mtok: number }; problem: null }
  | { request: null; problem: PriceProblem } {
  const model = draft.model.trim();
  if (model === "" || new TextEncoder().encode(model).length > MAX_TEXT_RUNES) return { request: null, problem: "model" };
  const input = parseCreditsInput(draft.input);
  const output = parseCreditsInput(draft.output);
  if (input === null || output === null || input > MAX_PRICE_MICRO_PER_MTOK || output > MAX_PRICE_MICRO_PER_MTOK) {
    return { request: null, problem: "price" };
  }
  return { request: { model, input_micro_per_mtok: input, output_micro_per_mtok: output }, problem: null };
}

/** AdjustProblem names what a balance adjustment draft gets wrong.
 *
 * AdjustProblem 指出余额调整草稿哪里出错。 */
export type AdjustProblem = "amount" | "reason";

/** adjustRequest validates an adjustment typed in credits (negative to
 * deduct) and builds types.CreditAdjustRequest. Zero is refused, as on the
 * server: an adjustment that changes nothing would only add a ledger row.
 *
 * adjustRequest 校验以点为单位输入的调整（为负则扣减），并构造
 * types.CreditAdjustRequest。与服务端一致拒绝零：什么都不改变的调整只会多出一行账目。 */
export function adjustRequest(draft: { amount: string; reason: string }):
  | { request: { amount_micro: number; reason: string }; problem: null }
  | { request: null; problem: AdjustProblem } {
  const amount = parseCreditsInput(draft.amount, { allowNegative: true });
  if (amount === null || amount === 0) return { request: null, problem: "amount" };
  const reason = draft.reason.trim();
  if (reason === "" || runes(reason) > MAX_TEXT_RUNES || new TextEncoder().encode(reason).length > MAX_TEXT_RUNES) {
    return { request: null, problem: "reason" };
  }
  return { request: { amount_micro: amount, reason }, problem: null };
}

/** isPaymentReturn reports whether the page was reached from the provider's
 * return redirect, which appends `status` (and `payment_id`). It is a hint to
 * start watching for the server's confirmation and nothing more: the value
 * of `status` is never read as an outcome, because anyone can type it.
 *
 * isPaymentReturn 报告页面是否由服务商的回跳到达——回跳会附加 `status`（以及
 * `payment_id`）。它只是开始等待服务端确认的提示，别无其他：`status` 的取值从不被
 * 当作结果读取，因为任何人都可以手动输入它。 */
export function isPaymentReturn(params: Readonly<Record<string, string | string[] | undefined>>): boolean {
  return params.status !== undefined || params.payment_id !== undefined;
}

/** PollOutcome is how a bounded poll ended.
 *
 * PollOutcome 是一次有界轮询的结束方式。 */
export type PollOutcome = "settled" | "exhausted" | "aborted";

/** PollOptions configures pollUntilSettled. `read` and `sleep` are injected
 * so a test drives the loop without a network or a clock.
 *
 * PollOptions 配置 pollUntilSettled。`read` 与 `sleep` 由外部注入，测试因此无需网络
 * 或真实时钟即可驱动循环。 */
export interface PollOptions<T> {
  read: (signal: AbortSignal) => Promise<T>;
  sleep: (ms: number, signal: AbortSignal) => Promise<void>;
  settled: (value: T) => boolean;
  onResult: (value: T) => void;
  signal: AbortSignal;
  intervalMs?: number;
  maxAttempts?: number;
  /** giveUp reports a failure that more attempts cannot fix — billing
   * switched off (404), the session gone (401) — so the poll ends at once.
   *
   * giveUp 报告再试也无济于事的失败——计费已关闭（404）、会话已失效（401）——
   * 此时轮询立即结束。 */
  giveUp?: (error: unknown) => boolean;
}

/** POLL_INTERVAL_MS and POLL_MAX_ATTEMPTS bound the wait after a payment
 * return to about half a minute: long enough for a webhook that is on time,
 * short enough that an abandoned tab does not keep asking.
 *
 * POLL_INTERVAL_MS 与 POLL_MAX_ATTEMPTS 把支付回跳后的等待限定在半分钟左右：足够一个
 * 准时的 webhook 到达，又短到不会让一个被遗忘的标签页一直问下去。 */
export const POLL_INTERVAL_MS = 3_000;
export const POLL_MAX_ATTEMPTS = 10;

/** pollUntilSettled reads up to `maxAttempts` times, `intervalMs` apart,
 * handing each result to `onResult` and stopping early once `settled` says
 * the server has confirmed an outcome. A failed read counts as an attempt
 * and does not end the loop, since one dropped request says nothing about the
 * payment — unless `giveUp` says retrying cannot help. It never polls without
 * bound.
 *
 * pollUntilSettled 最多读取 `maxAttempts` 次，每次间隔 `intervalMs`，把每次结果交给
 * `onResult`，一旦 `settled` 表明服务端已确认结果就提前停止。读取失败计为一次尝试，
 * 但不结束循环，因为一次丢失的请求说明不了付款的任何情况——除非 `giveUp` 表明再试
 * 也无济于事。它绝不无界轮询。 */
export async function pollUntilSettled<T>(options: PollOptions<T>): Promise<PollOutcome> {
  const interval = options.intervalMs ?? POLL_INTERVAL_MS;
  const attempts = options.maxAttempts ?? POLL_MAX_ATTEMPTS;
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (attempt > 0) {
      await options.sleep(interval, options.signal);
    }
    if (options.signal.aborted) {
      return "aborted";
    }
    try {
      const value = await options.read(options.signal);
      if (options.signal.aborted) {
        return "aborted";
      }
      options.onResult(value);
      if (options.settled(value)) {
        return "settled";
      }
    } catch (error) {
      if (options.signal.aborted) {
        return "aborted";
      }
      if (options.giveUp?.(error)) {
        return "exhausted";
      }
    }
  }
  return "exhausted";
}

/** abortableSleep waits `ms`, resolving early when `signal` aborts. It is the
 * default `sleep` for pollUntilSettled in the browser.
 *
 * abortableSleep 等待 `ms` 毫秒，`signal` 中止时提前 resolve。它是浏览器里
 * pollUntilSettled 默认使用的 `sleep`。 */
export function abortableSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", stop);
      resolve();
    }, ms);
    function stop() {
      clearTimeout(timer);
      resolve();
    }
    signal.addEventListener("abort", stop, { once: true });
  });
}

import { ApiError } from "./errors.ts";
import { MAX_PRODUCT_ID_BYTES, MAX_TEXT_RUNES, parseMoneyInput } from "./billing.ts";
import { isSupportedCurrency } from "./currencies.ts";

/** MAX_ACCOUNT_PRICE_MINOR mirrors model.MaxAccountPriceMinor.
 *
 * MAX_ACCOUNT_PRICE_MINOR 镜像 model.MaxAccountPriceMinor。 */
export const MAX_ACCOUNT_PRICE_MINOR = 100_000_000;

/** ACCOUNT_KINDS mirrors model.AccountKindClaude / AccountKindCodex: the
 * runtime kinds an Agent reports for its Claude Code and Codex adapters.
 *
 * ACCOUNT_KINDS 镜像 model.AccountKindClaude / AccountKindCodex：Agent 为其 Claude
 * Code 与 Codex 适配器上报的运行时类型。 */
export const ACCOUNT_KINDS = ["claude", "codex"] as const;
export type AccountKind = (typeof ACCOUNT_KINDS)[number];

/** accountKindLabel names a kind by its product name, which is not
 * translated.
 *
 * accountKindLabel 以产品名称称呼一个类型，产品名称不翻译。 */
export function accountKindLabel(kind: AccountKind): string {
  return kind === "claude" ? "Claude Code" : "Codex";
}

/** parseAccountKind reads a kind; `allowEmpty` admits the empty kind of a
 * legacy account, which an absent field also means.
 *
 * parseAccountKind 读取一个类型；`allowEmpty` 允许旧版账号的空类型，字段缺席同样表示
 * 空类型。 */
export function parseAccountKind(value: unknown, allowEmpty: boolean): AccountKind | "" {
  if (allowEmpty && (value === undefined || value === "")) return "";
  if (!(ACCOUNT_KINDS as readonly unknown[]).includes(value)) throw new ApiError("contract");
  return value as AccountKind;
}

/** AccountSale mirrors types.AccountSale: a monthly account priced in
 * `priceMinor` units of `currency`. A zero price is a legacy credit-priced
 * offer, or archived inventory kept only for presale assignment; it must be
 * priced before it can be listed or bought. `kind` is "" on a legacy row.
 * `dodoProductId` and `tenantId` are "" on the tenant view.
 *
 * AccountSale 镜像 types.AccountSale：月价为 `currency` 的 `priceMinor` 个最小单位。
 * 价格为零表示旧版按点数定价的商品，或仅供预售分配的下架库存；须标价才能上架或购买。
 * 旧行的 `kind` 为 ""。租户视图上 `dodoProductId` 与 `tenantId` 为 ""。 */
export interface AccountSale {
  id: string; nodeId: string; runtimeId: string; kind: AccountKind | ""; name: string;
  priceMinor: number; currency: string; dodoProductId: string;
  active: boolean; owned: boolean; expiresAt: string | null; tenantId: string;
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}
function text(value: unknown, max: number): string {
  if (typeof value !== "string" || !value || new TextEncoder().encode(value).length > max) throw new ApiError("contract");
  return value;
}
function optional(value: unknown, max: number): string {
  return value === undefined || value === "" ? "" : text(value, max);
}
function price(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0 || value > MAX_ACCOUNT_PRICE_MINOR) throw new ApiError("contract");
  return value;
}
function currency(value: unknown, priced: boolean): string {
  if (value === "" && !priced) return "";
  if (typeof value !== "string" || !/^[A-Z]{3}$/.test(value)) throw new ApiError("contract");
  return value;
}
function timestamp(value: unknown): string {
  const raw = text(value, 40);
  if (!Number.isFinite(Date.parse(raw))) throw new ApiError("contract");
  return raw;
}
export function parseAccountSale(value: unknown): AccountSale {
  const row = object(value);
  if (typeof row.active !== "boolean" || typeof row.owned !== "boolean") throw new ApiError("contract");
  const priceMinor = price(row.price_minor);
  const result = {
    id: text(row.id, 32), nodeId: text(row.node_id, 128), runtimeId: text(row.runtime_id, 128),
    kind: parseAccountKind(row.kind, true), name: text(row.name, 512), priceMinor, currency: currency(row.currency, priceMinor > 0),
    dodoProductId: optional(row.dodo_product_id, MAX_PRODUCT_ID_BYTES),
    active: row.active, owned: row.owned,
    expiresAt: row.expires_at === undefined ? null : timestamp(row.expires_at),
    tenantId: optional(row.tenant_id, 32),
  };
  if (result.owned && result.expiresAt === null) throw new ApiError("contract");
  return result;
}
export function parseAccountSales(value: unknown): AccountSale[] {
  const source = object(value);
  if (!Array.isArray(source.items) || source.items.length > 1000) throw new ApiError("contract");
  const rows = source.items.map(parseAccountSale);
  if (new Set(rows.map(row => row.id)).size !== rows.length || new Set(rows.map(row => JSON.stringify([row.nodeId, row.runtimeId]))).size !== rows.length) throw new ApiError("contract");
  return rows;
}

/** accountPurchasable reports whether a tenant may open a checkout for the
 * account: listed and priced in fiat.
 *
 * accountPurchasable 报告商户能否为该账号发起结账：已上架且有法币价格。 */
export function accountPurchasable(account: AccountSale): boolean {
  return account.active && account.priceMinor > 0 && account.currency !== "";
}

/** accountCheckoutBody pins the price the tenant confirmed, so a price
 * change in between is refused with 409 rather than charged.
 *
 * accountCheckoutBody 固定商户确认的价格，期间若改价会以 409 拒绝而不是照新价收费。 */
export function accountCheckoutBody(account: Pick<AccountSale, "priceMinor" | "currency">): { expected_price_minor: number; currency: string } {
  return { expected_price_minor: account.priceMinor, currency: account.currency };
}

/** AccountDraftProblem names the first invalid field of an account draft.
 *
 * AccountDraftProblem 指出账号草稿中第一个无效字段。 */
export type AccountDraftProblem = "name" | "target" | "kind" | "currency" | "price" | "product";

export interface AccountRequest {
  name: string; node_id: string; runtime_id: string; kind: AccountKind | "";
  price_minor: number; currency: string; dodo_product_id: string; active: boolean;
}

/** accountRequest validates an operator's draft the way SaveAccountSale does:
 * the price is typed in the currency's major unit. A new account must name
 * its kind; an edit may leave a legacy row's kind empty, which keeps it.
 * Archived inventory may leave price and product blank, kept only for
 * presale assignment; then neither price nor currency is sent.
 *
 * accountRequest 按 SaveAccountSale 的规则校验运维草稿：价格以货币主单位输入。新账号
 * 必须指明类型；编辑旧行时可以留空类型，表示保持不变。下架库存可以不填价格与产品，
 * 仅供预售分配，此时不发送价格与币种。 */
export function accountRequest(draft: { name: string; nodeId: string; runtimeId: string; kind: AccountKind | ""; kindRequired: boolean; price: string; currency: string; productId: string; active: boolean }):
  { request: AccountRequest; problem: null } | { request: null; problem: AccountDraftProblem } {
  const name = draft.name.trim();
  if (name === "" || [...name].length > MAX_TEXT_RUNES) return { request: null, problem: "name" };
  if (!draft.nodeId || !draft.runtimeId) return { request: null, problem: "target" };
  if (draft.kindRequired && draft.kind === "") return { request: null, problem: "kind" };
  const base = { name, node_id: draft.nodeId, runtime_id: draft.runtimeId, kind: draft.kind, active: draft.active };
  const product = draft.productId.trim();
  if (!draft.active && draft.price.trim() === "" && product === "") {
    return { request: { ...base, price_minor: 0, currency: "", dodo_product_id: "" }, problem: null };
  }
  const code = draft.currency.trim().toUpperCase();
  if (!isSupportedCurrency(code)) return { request: null, problem: "currency" };
  const minor = parseMoneyInput(draft.price, code);
  if (minor === null || minor <= 0 || minor > MAX_ACCOUNT_PRICE_MINOR) return { request: null, problem: "price" };
  if (product === "" || new TextEncoder().encode(product).length > MAX_PRODUCT_ID_BYTES) return { request: null, problem: "product" };
  return { request: { ...base, price_minor: minor, currency: code, dodo_product_id: product }, problem: null };
}

/**
 * pricing.ts is the operator side of token pricing (.scratch/token-pricing):
 * contract parsing for the versioned pricing configuration, price plans, the
 * official price catalog and its review, and the prices charging resolves;
 * plus the conversions between those and the editing forms. Units match the
 * control plane: a unit price is micro-credits per million tokens, a rate is
 * parts per million (1_000_000 is 1x), and one credit is one US dollar.
 *
 * pricing.ts 是 token 计价（.scratch/token-pricing）的运维侧：解析带版本的定价配置、
 * 价格方案、官方价目表及其审核、扣费所解析的价格，并在它们与编辑表单之间转换。单位与控制面
 * 一致：单价是「micro 点 / 百万 token」，倍率以百万分之一为单位（1_000_000 为 1 倍），
 * 1 点 = 1 美元。
 */
import { boolean, count, integer, list, MAX_PRICE_MICRO_PER_MTOK, page, parseScaled, formatScaled, record, required, text, timestamp } from "./billing.ts";
import type { Page } from "./contract.ts";
import { ApiError } from "./errors.ts";

/** RATE_ONE is a 1x rate in parts per million.
 *
 * RATE_ONE 是以百万分之一为单位的 1 倍倍率。 */
export const RATE_ONE = 1_000_000;

/** MAX_RATE mirrors pricing.MaxRate: 100x.
 *
 * MAX_RATE 镜像 pricing.MaxRate：100 倍。 */
export const MAX_RATE = 100 * RATE_ONE;

/** EDITED_METERS are the meters the price forms edit, in display order; any
 * other meter in a list is kept as it is.
 *
 * EDITED_METERS 是价格表单编辑的计量项（按显示顺序）；单价表中的其他计量项原样保留。 */
export const EDITED_METERS = ["input", "output", "input.cache_read", "input.cache_write", "input.cache_write.1h"] as const;
export type EditedMeter = (typeof EDITED_METERS)[number];

/** REQUIRED_METERS must be priced in an override, default or fixed price:
 * without them a request's root meters would be free.
 *
 * REQUIRED_METERS 必须出现在覆盖价、默认价与固定售价中：缺少它们，请求的根计量项会免费。 */
export const REQUIRED_METERS: readonly EditedMeter[] = ["input", "output"];

/** PriceItem mirrors pricing.PriceItem.
 *
 * PriceItem 镜像 pricing.PriceItem。 */
export interface PriceItem {
  meter: string;
  variant: string;
  aboveInputTokens: number;
  priceMicroPerM: number;
}

/** PricingRevision mirrors types.PricingRevision.
 *
 * PricingRevision 镜像 types.PricingRevision。 */
export interface PricingRevision {
  id: number;
  effectiveAt: string;
  createdBy: string;
  summary: string;
}

/** PricingModel is one alias's settings.
 *
 * PricingModel 是一个别名的设置。 */
export interface PricingModel {
  model: string;
  catalogModel: string;
  prices: PriceItem[];
  ratePPM: number;
}

/** PlanModel is one alias's terms in a plan: a factor, or a fixed price.
 *
 * PlanModel 是方案中某个别名的条款：系数，或固定售价。 */
export interface PlanModel {
  model: string;
  ratePPM: number | null;
  prices: PriceItem[];
}

/** PricePlan mirrors types.PricePlan; tenantCount is null for the default
 * plan, which every unassigned tenant belongs to.
 *
 * PricePlan 镜像 types.PricePlan；默认方案的 tenantCount 为 null，因为所有未分配的租户
 * 都属于它。 */
export interface PricePlan {
  id: string;
  name: string;
  defaultRatePPM: number;
  isDefault: boolean;
  models: PlanModel[];
  tenantCount: number | null;
}

/** PricingConfig mirrors types.PricingConfigResponse.
 *
 * PricingConfig 镜像 types.PricingConfigResponse。 */
export interface PricingConfig {
  revision: PricingRevision;
  defaultPrices: PriceItem[];
  models: PricingModel[];
  plans: PricePlan[];
  defaultPlan: string;
}

/** PriceSource is where an alias's prices come from.
 *
 * PriceSource 是别名单价的来源。 */
export type PriceSource = "override" | "catalog" | "default" | "plan_fixed" | "none";
const PRICE_SOURCES: readonly PriceSource[] = ["override", "catalog", "default", "plan_fixed", "none"];

/** PriceViewItem is one resolved price: base and sale, per meter and tier.
 *
 * PriceViewItem 是一条解析后的单价：按计量项与档位给出基础价与售价。 */
export interface PriceViewItem {
  meter: string;
  variant: string;
  aboveInputTokens: number;
  baseMicroPerM: number;
  saleMicroPerM: number;
}

/** PriceView mirrors types.PriceView.
 *
 * PriceView 镜像 types.PriceView。 */
export interface PriceView {
  model: string;
  routed: boolean;
  source: PriceSource;
  ref: string;
  modelRatePPM: number | null;
  planRatePPM: number | null;
  ratePPM: number;
  items: PriceViewItem[];
}

/** EffectivePrices mirrors types.EffectivePricesResponse.
 *
 * EffectivePrices 镜像 types.EffectivePricesResponse。 */
export interface EffectivePrices {
  revision: number;
  planId: string;
  items: PriceView[];
  unpricedAliases: string[];
  unpricedRequests24h: number;
}

/** CatalogPrice mirrors types.PriceCatalogPrice.
 *
 * CatalogPrice 镜像 types.PriceCatalogPrice。 */
export interface CatalogPrice extends PriceItem {
  derived: boolean;
}

/** CatalogEntry mirrors types.PriceCatalogEntry.
 *
 * CatalogEntry 镜像 types.PriceCatalogEntry。 */
export interface CatalogEntry {
  model: string;
  provider: string;
  mode: string;
  status: string;
  pending: string;
  prices: CatalogPrice[];
}

/** CatalogSync mirrors types.PriceCatalogSyncState.
 *
 * CatalogSync 镜像 types.PriceCatalogSyncState。 */
export interface CatalogSync {
  source: string;
  entryCount: number;
  version: number;
  syncedAt: string | null;
  checkedAt: string | null;
  lastError: string;
  lastErrorAt: string | null;
}

/** CatalogPage is one page of the catalog with the sync state.
 *
 * CatalogPage 是价目表的一页，附带同步状态。 */
export interface CatalogPage extends Page<CatalogEntry> {
  sync: CatalogSync;
}

/** CatalogPending mirrors types.PriceCatalogPendingItem. maxChangeBP is the
 * largest relative change in basis points, null when unbounded (a price that
 * was zero, or vanished).
 *
 * CatalogPending 镜像 types.PriceCatalogPendingItem。maxChangeBP 是以基点计的最大相对
 * 变动，无界时（原价为零，或单价被删除）为 null。 */
export interface CatalogPending {
  model: string;
  kind: string;
  status: string;
  maxChangeBP: number | null;
  detectedAt: string;
  current: CatalogPrice[];
  proposed: CatalogPrice[];
}

/** CatalogSyncResult mirrors types.PriceCatalogSyncResponse.
 *
 * CatalogSyncResult 镜像 types.PriceCatalogSyncResponse。 */
export interface CatalogSyncResult {
  skipped: boolean;
  added: number;
  applied: number;
  reviewed: number;
  removed: number;
}

/** PendingUsage mirrors types.PendingUsage: usage taken off the balance but
 * not yet sealed into a ledger entry. amountMicro is negative.
 *
 * PendingUsage 镜像 types.PendingUsage：已从余额扣除、尚未封账成账目的用量。
 * amountMicro 为负数。 */
export interface PendingUsage {
  amountMicro: number;
  requestCount: number;
  since: string | null;
  sealAt: string | null;
}

function fail(): never {
  throw new ApiError("contract");
}

function optionalString(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (value === undefined || value === null) return "";
  if (typeof value !== "string") fail();
  return value;
}

function optionalTime(source: Record<string, unknown>, key: string): string | null {
  const value = source[key];
  if (value === undefined || value === null || value === "") return null;
  return timestamp(source, key);
}

function optionalInteger(source: Record<string, unknown>, key: string): number | null {
  const value = source[key];
  if (value === undefined || value === null) return null;
  return integer(source, key);
}

function optionalCount(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  if (value === undefined || value === null) return 0;
  return count(source, key);
}

function optionalFlag(source: Record<string, unknown>, key: string): boolean {
  return source[key] === undefined ? false : boolean(source, key);
}

/** parsePriceItem validates one pricing.PriceItem.
 *
 * parsePriceItem 校验一个 pricing.PriceItem。 */
export function parsePriceItem(value: unknown): PriceItem {
  const source = record(value);
  return {
    meter: required(source, "meter"),
    variant: optionalString(source, "variant"),
    aboveInputTokens: optionalCount(source, "above_input_tokens"),
    priceMicroPerM: count(source, "price_micro_per_m"),
  };
}

function parseCatalogPrice(value: unknown): CatalogPrice {
  return { ...parsePriceItem(value), derived: optionalFlag(record(value), "derived") };
}

function parseRevision(value: unknown): PricingRevision {
  const source = record(value);
  return { id: count(source, "id"), effectiveAt: timestamp(source, "effective_at"), createdBy: text(source, "created_by"), summary: text(source, "summary") };
}

function parsePlan(value: unknown): PricePlan {
  const source = record(value);
  return {
    id: required(source, "id"),
    name: text(source, "name"),
    defaultRatePPM: count(source, "default_rate_ppm"),
    isDefault: boolean(source, "is_default"),
    models: list(source.models, (item) => {
      const terms = record(item);
      return { model: required(terms, "model"), ratePPM: optionalInteger(terms, "rate_ppm"), prices: list(terms.prices, parsePriceItem) };
    }),
    tenantCount: optionalInteger(source, "tenant_count"),
  };
}

/** parsePricingConfig validates GET /operator/v1/billing/pricing and every
 * pricing write that answers the whole configuration.
 *
 * parsePricingConfig 校验 GET /operator/v1/billing/pricing，以及所有返回完整配置的
 * 定价写操作。 */
export function parsePricingConfig(value: unknown): PricingConfig {
  const source = record(value);
  return {
    revision: parseRevision(source.revision),
    defaultPrices: list(source.default_prices, parsePriceItem),
    models: list(source.models, (item) => {
      const cfg = record(item);
      return { model: required(cfg, "model"), catalogModel: text(cfg, "catalog_model"), prices: list(cfg.prices, parsePriceItem), ratePPM: count(cfg, "rate_ppm") };
    }),
    plans: list(source.plans, parsePlan),
    defaultPlan: required(source, "default_plan"),
  };
}

/** parsePlanList validates GET /operator/v1/billing/plans.
 *
 * parsePlanList 校验 GET /operator/v1/billing/plans。 */
export function parsePlanList(value: unknown): { revision: PricingRevision; items: PricePlan[] } {
  const source = record(value);
  return { revision: parseRevision(source.revision), items: list(source.items, parsePlan) };
}

/** parsePlanResult validates a plan write's answer.
 *
 * parsePlanResult 校验方案写操作的答复。 */
export function parsePlanResult(value: unknown): { plan: PricePlan; revision: PricingRevision } {
  const source = record(value);
  return { plan: parsePlan(source.plan), revision: parseRevision(source.revision) };
}

/** parseRevisionResult validates an answer that carries only the new
 * revision (a plan deletion).
 *
 * parseRevisionResult 校验只带新版本的答复（删除方案）。 */
export function parseRevisionResult(value: unknown): PricingRevision {
  return parseRevision(record(value).revision);
}

function parsePriceView(value: unknown): PriceView {
  const source = record(value);
  const sourceValue = source.source;
  if (!(PRICE_SOURCES as readonly unknown[]).includes(sourceValue)) fail();
  return {
    model: required(source, "model"),
    routed: optionalFlag(source, "routed"),
    source: sourceValue as PriceSource,
    ref: optionalString(source, "ref"),
    modelRatePPM: optionalInteger(source, "model_rate_ppm"),
    planRatePPM: optionalInteger(source, "plan_rate_ppm"),
    ratePPM: count(source, "rate_ppm"),
    items: list(source.items, (item) => {
      const price = record(item);
      return {
        meter: required(price, "meter"),
        variant: optionalString(price, "variant"),
        aboveInputTokens: optionalCount(price, "above_input_tokens"),
        baseMicroPerM: count(price, "base_micro_per_m"),
        saleMicroPerM: count(price, "sale_micro_per_m"),
      };
    }),
  };
}

/** parseEffectivePrices validates GET /operator/v1/billing/effective-prices.
 *
 * parseEffectivePrices 校验 GET /operator/v1/billing/effective-prices。 */
export function parseEffectivePrices(value: unknown): EffectivePrices {
  const source = record(value);
  return {
    revision: optionalCount(source, "revision"),
    planId: optionalString(source, "plan_id"),
    items: list(source.items, parsePriceView),
    unpricedAliases: list(source.unpriced_aliases, (item) => (typeof item === "string" ? item : fail())),
    unpricedRequests24h: optionalCount(source, "unpriced_requests_24h"),
  };
}

function parseSync(value: unknown): CatalogSync {
  const source = record(value);
  return {
    source: text(source, "source"),
    entryCount: count(source, "entry_count"),
    version: count(source, "version"),
    syncedAt: optionalTime(source, "synced_at"),
    checkedAt: optionalTime(source, "checked_at"),
    lastError: optionalString(source, "last_error"),
    lastErrorAt: optionalTime(source, "last_error_at"),
  };
}

/** parseCatalogPage validates one page of GET /operator/v1/billing/catalog.
 *
 * parseCatalogPage 校验 GET /operator/v1/billing/catalog 的一页。 */
export function parseCatalogPage(value: unknown): CatalogPage {
  const base = page(value, (item) => {
    const source = record(item);
    return {
      model: required(source, "model"),
      provider: text(source, "provider"),
      mode: text(source, "mode"),
      status: required(source, "status"),
      pending: optionalString(source, "pending"),
      prices: list(source.prices, parseCatalogPrice),
    };
  });
  return { ...base, sync: parseSync(record(value).sync) };
}

/** parseCatalogPendingPage validates one page of the catalog review queue.
 *
 * parseCatalogPendingPage 校验价目表审核队列的一页。 */
export function parseCatalogPendingPage(value: unknown): Page<CatalogPending> {
  return page(value, (item) => {
    const source = record(item);
    return {
      model: required(source, "model"),
      kind: required(source, "kind"),
      status: text(source, "status"),
      maxChangeBP: optionalInteger(source, "max_change_bp"),
      detectedAt: timestamp(source, "detected_at"),
      current: list(source.current, parseCatalogPrice),
      proposed: list(source.proposed, parseCatalogPrice),
    };
  });
}

/** parseCatalogSyncResult validates POST /operator/v1/billing/catalog/sync,
 * keeping only the counts the page shows.
 *
 * parseCatalogSyncResult 校验 POST /operator/v1/billing/catalog/sync，只保留页面显示的
 * 计数。 */
export function parseCatalogSyncResult(value: unknown): CatalogSyncResult {
  const source = record(value);
  const size = (key: string) => {
    const items = source[key];
    if (items === undefined || items === null) return 0;
    if (!Array.isArray(items)) fail();
    return items.length;
  };
  return { skipped: optionalFlag(source, "skipped"), added: size("added"), applied: size("applied"), reviewed: size("reviewed"), removed: size("removed") };
}

/** parseCatalogDecision validates an approve or reject answer.
 *
 * parseCatalogDecision 校验批准或拒绝的答复。 */
export function parseCatalogDecision(value: unknown): { model: string } {
  return { model: required(record(value), "model") };
}

/** parsePendingUsage validates an optional types.PendingUsage.
 *
 * parsePendingUsage 校验可选的 types.PendingUsage。 */
export function parsePendingUsage(value: unknown): PendingUsage | null {
  if (value === undefined || value === null) return null;
  const source = record(value);
  return {
    amountMicro: integer(source, "amount_micro"),
    requestCount: count(source, "request_count"),
    since: optionalTime(source, "since"),
    sealAt: optionalTime(source, "seal_at"),
  };
}

/** formatRate renders a rate in parts per million as "1.5x".
 *
 * formatRate 把以百万分之一为单位的倍率渲染为 "1.5x"。 */
export function formatRate(ratePPM: number): string {
  return `${formatScaled(ratePPM, 6)}x`;
}

/** parseRateInput reads a rate typed as a multiple ("1.5") and returns parts
 * per million, or null outside [0, 100] or with more than six decimals.
 *
 * parseRateInput 读取以倍数输入的倍率（"1.5"）并返回百万分之一单位的值；超出 [0, 100]
 * 或小数超过六位时返回 null。 */
export function parseRateInput(raw: string): number | null {
  const value = parseScaled(raw, 6);
  return value === null || value > MAX_RATE ? null : value;
}

/** parsePriceInput reads a unit price typed in credits per million tokens and
 * returns micro-credits per million, or null.
 *
 * parsePriceInput 读取以「点 / 百万 token」输入的单价，返回「micro 点 / 百万」，无效时
 * 返回 null。 */
export function parsePriceInput(raw: string): number | null {
  const value = parseScaled(raw, 6);
  return value === null || value > MAX_PRICE_MICRO_PER_MTOK ? null : value;
}

/** formatMaxChange renders a pending change's largest relative move: "+12.5%"
 * style for a bounded one (always unsigned: the review shows the direction
 * per price), or null when unbounded.
 *
 * formatMaxChange 渲染待审核变动的最大相对幅度：有界时形如 "12.5%"（不带符号，方向在
 * 逐条单价中显示），无界时返回 null。 */
export function formatMaxChange(bp: number | null): string | null {
  return bp === null ? null : `${formatScaled(bp, 2)}%`;
}

/** isBaseItem reports whether an item is a base-tier, default-variant price,
 * the ones the forms edit.
 *
 * isBaseItem 报告某条单价是否为基础档、默认变体，即表单所编辑的那些。 */
export function isBaseItem(item: { variant: string; aboveInputTokens: number }): boolean {
  return item.variant === "" && item.aboveInputTokens === 0;
}

/** PriceDraft is a price form: one text field per edited meter, empty meaning
 * "no price of its own" (it falls back to its parent).
 *
 * PriceDraft 是价格表单：每个被编辑的计量项一个文本框，空值表示「没有自己的单价」
 * （回落到父项）。 */
export type PriceDraft = Record<EditedMeter, string>;

/** emptyPriceDraft is a form with no prices.
 *
 * emptyPriceDraft 是没有任何单价的表单。 */
export function emptyPriceDraft(): PriceDraft {
  return { input: "", output: "", "input.cache_read": "", "input.cache_write": "", "input.cache_write.1h": "" };
}

/** priceDraft fills a form from a list's base-tier, default-variant prices.
 *
 * priceDraft 用单价表中基础档、默认变体的单价填写表单。 */
export function priceDraft(items: readonly PriceItem[]): PriceDraft {
  const draft = emptyPriceDraft();
  for (const item of items) {
    if (isBaseItem(item) && (EDITED_METERS as readonly string[]).includes(item.meter)) {
      draft[item.meter as EditedMeter] = formatScaled(item.priceMicroPerM, 6);
    }
  }
  return draft;
}

/** PriceListProblem names the first problem with a price form: a field that
 * is not a valid price, or a required meter left empty.
 *
 * PriceListProblem 指出价格表单的第一个问题：不是有效单价的字段，或留空的必填计量项。 */
export type PriceListProblem = { kind: "invalid" | "missing"; meter: EditedMeter };

/** priceListFromDraft turns a form into a price list, keeping every item of
 * kept that the form does not edit (tiers, variants, other meters), so
 * editing the base prices never drops a long-context tier.
 *
 * priceListFromDraft 把表单转换为单价表，并保留 kept 中表单不编辑的所有条目（档位、变体、
 * 其他计量项），使编辑基础价时绝不会丢掉长上下文档位。 */
export function priceListFromDraft(draft: PriceDraft, kept: readonly PriceItem[] = []):
  { prices: PriceItem[]; problem: null } | { prices: null; problem: PriceListProblem } {
  const prices: PriceItem[] = kept.filter((item) => !(isBaseItem(item) && (EDITED_METERS as readonly string[]).includes(item.meter)));
  for (const meter of EDITED_METERS) {
    const raw = draft[meter].trim();
    if (raw === "") {
      if (REQUIRED_METERS.includes(meter)) return { prices: null, problem: { kind: "missing", meter } };
      continue;
    }
    const value = parsePriceInput(raw);
    if (value === null) return { prices: null, problem: { kind: "invalid", meter } };
    prices.push({ meter, variant: "", aboveInputTokens: 0, priceMicroPerM: value });
  }
  return { prices, problem: null };
}

/** catalogItems turns catalog prices into a plain price list (dropping the
 * derived marks), for "copy from the official price".
 *
 * catalogItems 把价目表单价转换为普通单价表（去掉推算标记），用于「从官方价复制」。 */
export function catalogItems(prices: readonly CatalogPrice[]): PriceItem[] {
  return prices.map(({ meter, variant, aboveInputTokens, priceMicroPerM }) => ({ meter, variant, aboveInputTokens, priceMicroPerM }));
}

/** priceItemsBody renders a price list for a request body.
 *
 * priceItemsBody 把单价表渲染为请求体格式。 */
export function priceItemsBody(items: readonly PriceItem[]): { meter: string; variant?: string; above_input_tokens?: number; price_micro_per_m: number }[] {
  return items.map((item) => ({
    meter: item.meter,
    ...(item.variant ? { variant: item.variant } : {}),
    ...(item.aboveInputTokens ? { above_input_tokens: item.aboveInputTokens } : {}),
    price_micro_per_m: item.priceMicroPerM,
  }));
}

/** tiersOf returns the distinct tiers in a list, ascending, base tier first.
 *
 * tiersOf 返回列表中互不相同的档位，升序，基础档在前。 */
export function tiersOf(items: readonly { aboveInputTokens: number }[]): number[] {
  return [...new Set([0, ...items.map((item) => item.aboveInputTokens)])].sort((a, b) => a - b);
}

/** findViewItem returns the default-variant price of meter at exactly tier,
 * or undefined.
 *
 * findViewItem 返回计量项 meter 在恰好 tier 档、默认变体下的单价；没有时返回 undefined。 */
export function findViewItem<T extends { meter: string; variant: string; aboveInputTokens: number }>(items: readonly T[], meter: string, tier = 0): T | undefined {
  return items.find((item) => item.meter === meter && item.variant === "" && item.aboveInputTokens === tier);
}

/** PriceChange is one price's before and after in a catalog review; change is
 * in basis points (null when unbounded: from or to zero, or added or removed).
 *
 * PriceChange 是价目表审核中一条单价的前后对照；change 以基点计（无界时为 null：从零起
 * 或降为零，或新增、删除）。 */
export interface PriceChange {
  meter: string;
  variant: string;
  aboveInputTokens: number;
  before: number | null;
  after: number | null;
  changeBP: number | null;
}

/** priceChanges lines up current and proposed prices by key, in key order,
 * leaving out prices that did not move.
 *
 * priceChanges 按键对齐当前与拟采用的单价，按键排序，省略没有变化的单价。 */
export function priceChanges(current: readonly PriceItem[], proposed: readonly PriceItem[]): PriceChange[] {
  const key = (item: PriceItem) => `${item.meter}\u0000${item.variant}\u0000${String(item.aboveInputTokens).padStart(12, "0")}`;
  const before = new Map(current.map((item) => [key(item), item]));
  const after = new Map(proposed.map((item) => [key(item), item]));
  const keys = [...new Set([...before.keys(), ...after.keys()])].sort();
  const out: PriceChange[] = [];
  for (const k of keys) {
    const b = before.get(k);
    const a = after.get(k);
    const ref = (a ?? b) as PriceItem;
    const bv = b ? b.priceMicroPerM : null;
    const av = a ? a.priceMicroPerM : null;
    if (bv === av) continue;
    const changeBP = bv !== null && av !== null && bv > 0 && av > 0 ? Math.round(((av - bv) * 10_000) / bv) : null;
    out.push({ meter: ref.meter, variant: ref.variant, aboveInputTokens: ref.aboveInputTokens, before: bv, after: av, changeBP });
  }
  return out;
}

/** PlanTermDraft is one alias row of the plan form.
 *
 * PlanTermDraft 是方案表单中的一个别名行。 */
export interface PlanTermDraft {
  model: string;
  mode: "rate" | "fixed";
  rate: string;
  prices: PriceDraft;
  kept: PriceItem[];
}

/** PlanDraft is the plan form.
 *
 * PlanDraft 是方案表单。 */
export interface PlanDraft {
  name: string;
  defaultRate: string;
  terms: PlanTermDraft[];
}

/** emptyPlanDraft is a new plan at 1x.
 *
 * emptyPlanDraft 是一个 1 倍的新方案。 */
export function emptyPlanDraft(): PlanDraft {
  return { name: "", defaultRate: "1", terms: [] };
}

/** emptyPlanTerm is a new alias row: a factor of 1x.
 *
 * emptyPlanTerm 是一个新的别名行：系数 1 倍。 */
export function emptyPlanTerm(): PlanTermDraft {
  return { model: "", mode: "rate", rate: "1", prices: emptyPriceDraft(), kept: [] };
}

/** planDraft fills the form from a plan.
 *
 * planDraft 用一个方案填写表单。 */
export function planDraft(plan: PricePlan): PlanDraft {
  return {
    name: plan.name,
    defaultRate: formatScaled(plan.defaultRatePPM, 6),
    terms: plan.models.map((terms) => terms.prices.length > 0
      ? { model: terms.model, mode: "fixed", rate: "1", prices: priceDraft(terms.prices), kept: terms.prices }
      : { model: terms.model, mode: "rate", rate: formatScaled(terms.ratePPM ?? RATE_ONE, 6), prices: emptyPriceDraft(), kept: [] }),
  };
}

/** PlanProblem names the first problem with the plan form.
 *
 * PlanProblem 指出方案表单的第一个问题。 */
export type PlanProblem =
  | { kind: "name" | "default_rate" }
  | { kind: "model" | "duplicate" | "rate"; row: number }
  | { kind: "price"; row: number; problem: PriceListProblem };

/** PlanRequestBody is the plan part of a plan write.
 *
 * PlanRequestBody 是方案写操作中的方案部分。 */
export interface PlanRequestBody {
  name: string;
  default_rate_ppm: number;
  models: { model: string; rate_ppm?: number; prices?: ReturnType<typeof priceItemsBody> }[];
}

/** planRequest validates the form and builds the body (without
 * base_revision). Names are at most 128 characters, aliases are trimmed and
 * must be distinct.
 *
 * planRequest 校验表单并构造请求体（不含 base_revision）。名称最多 128 个字符，别名去掉
 * 首尾空白后必须互不相同。 */
export function planRequest(draft: PlanDraft): { body: PlanRequestBody; problem: null } | { body: null; problem: PlanProblem } {
  const name = draft.name.trim();
  if (name === "" || [...name].length > 128) return { body: null, problem: { kind: "name" } };
  const defaultRate = parseRateInput(draft.defaultRate);
  if (defaultRate === null) return { body: null, problem: { kind: "default_rate" } };
  const seen = new Set<string>();
  const models: PlanRequestBody["models"] = [];
  for (const [row, term] of draft.terms.entries()) {
    const model = term.model.trim();
    if (model === "" || model.length > 128) return { body: null, problem: { kind: "model", row } };
    if (seen.has(model)) return { body: null, problem: { kind: "duplicate", row } };
    seen.add(model);
    if (term.mode === "rate") {
      const rate = parseRateInput(term.rate);
      if (rate === null) return { body: null, problem: { kind: "rate", row } };
      models.push({ model, rate_ppm: rate });
      continue;
    }
    const built = priceListFromDraft(term.prices, term.kept);
    if (built.problem !== null) return { body: null, problem: { kind: "price", row, problem: built.problem } };
    models.push({ model, prices: priceItemsBody(built.prices) });
  }
  return { body: { name, default_rate_ppm: defaultRate, models }, problem: null };
}

/** isConflict reports whether a write failed because the configuration
 * moved on since it was read (409): the page must re-read before retrying.
 *
 * isConflict 报告写操作是否因配置在读取之后已被修改（409）而失败：页面必须先重新读取
 * 再重试。 */
export function isConflict(error: unknown): boolean {
  return error instanceof ApiError && error.kind === "conflict";
}

/** TenantPricing is the pricing part of one operator tenant-list row.
 *
 * TenantPricing 是运维租户列表中一行的计价部分。 */
export interface TenantPricing {
  pricePlanId: string;
  pendingUsage: PendingUsage | null;
}

/** parseTenantPricing reads the pricing fields of every row of a
 * GET /operator/v1/billing/tenants page, by tenant id; the rest of the row is
 * parseBillingTenants's.
 *
 * parseTenantPricing 按租户 id 读取 GET /operator/v1/billing/tenants 一页中每一行的
 * 计价字段；其余字段由 parseBillingTenants 负责。 */
export function parseTenantPricing(value: unknown): Map<string, TenantPricing> {
  const out = new Map<string, TenantPricing>();
  for (const item of list(record(value).items, record)) {
    out.set(required(item, "id"), { pricePlanId: optionalString(item, "price_plan_id"), pendingUsage: parsePendingUsage(item.pending_usage) });
  }
  return out;
}

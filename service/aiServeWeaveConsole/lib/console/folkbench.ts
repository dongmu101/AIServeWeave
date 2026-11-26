/**
 * folkbench.ts converts the control plane's public plan prices
 * (GET /billing/v1/plan-prices) into Folkbench's price format (schema 1.1),
 * served at GET /api/pricing. Folkbench reads the rows as final sale prices
 * without converting them, so every number here must be what a request is
 * actually charged.
 *
 * Groups: the stored price plans are read as they are and each becomes one
 * Folkbench group, named after the plan. Plan names need not be unique, so a
 * name more than one plan shares is suffixed with the plan id to keep each
 * group × model row distinct.
 *
 * Units: one credit is one US dollar and a sale price is micro-credits per
 * million units, so a token price in USD per million tokens is the sale price
 * divided by one million. Each field resolves the way charging does
 * (common/pricing.Lookup): the meter's own base-tier price under the default
 * service tier, falling back meter by meter to its parent. A cache price the
 * operator did not set is therefore the input price, because that is what the
 * cached tokens cost.
 *
 * folkbench.ts 把控制面公开的方案售价（GET /billing/v1/plan-prices）转换为 Folkbench
 * 价格格式（schema 1.1），由 GET /api/pricing 提供。Folkbench 把这些行当作最终售价原样
 * 读取、不做换算，因此这里的每个数字都必须是请求实际被收取的价格。
 *
 * 分组：按原样读取已保存的价格方案，每个方案成为一个 Folkbench 分组，以方案名称命名。
 * 方案名称不要求唯一，因此多个方案共用的名称会追加方案 id，使每一行「分组 × 模型」
 * 互不相同。
 *
 * 单位：1 点 = 1 美元，售价为「micro 点 / 百万单位」，因此以美元计的每百万 token 单价
 * 就是售价除以一百万。每个字段的解析方式与扣费一致（common/pricing.Lookup）：取该计量项
 * 在默认服务等级、基础档的单价，没有时逐级回落到父计量项。运维没有单独设置的缓存单价
 * 因此等于输入单价，因为缓存 token 实际就按它收费。
 */
import { boolean, count, DEFAULT_PRICE_MODEL, list, record, required, text, timestamp } from "./billing.ts";

/** FOLKBENCH_SCHEMA_VERSION is the format version this module writes.
 *
 * FOLKBENCH_SCHEMA_VERSION 是本模块输出的格式版本。 */
export const FOLKBENCH_SCHEMA_VERSION = "1.1";

/** Meter names, mirroring common/pricing/meter.go.
 *
 * 计量项名称，镜像 common/pricing/meter.go。 */
const METER_INPUT = "input";
const METER_OUTPUT = "output";
const METER_CACHE_READ = "input.cache_read";
const METER_CACHE_WRITE = "input.cache_write";
const METER_CACHE_WRITE_1H = "input.cache_write.1h";

const MICRO_PER_UNIT = 1_000_000;

/** SalePriceItem mirrors types.CatalogPriceItem: one sale price, in
 * micro-credits per million units of its meter.
 *
 * SalePriceItem 镜像 types.CatalogPriceItem：一条售价，单位为「micro 点 / 该计量项的
 * 百万单位」。 */
export interface SalePriceItem {
  meter: string;
  variant: string;
  aboveInputTokens: number;
  priceMicroPerM: number;
}

/** PlanPrices mirrors types.PublicPlanPrices.
 *
 * PlanPrices 镜像 types.PublicPlanPrices。 */
export interface PlanPrices {
  id: string;
  name: string;
  isDefault: boolean;
  prices: { model: string; items: SalePriceItem[] }[];
}

/** PublicPlanPrices mirrors types.PublicPlanPricesResponse; updatedAt is null
 * when the control plane does not know it.
 *
 * PublicPlanPrices 镜像 types.PublicPlanPricesResponse；控制面不知道时 updatedAt 为 null。 */
export interface PublicPlanPrices {
  updatedAt: string | null;
  plans: PlanPrices[];
}

function optionalText(source: Record<string, unknown>, key: string): string {
  return source[key] === undefined ? "" : text(source, key);
}

function optionalCount(source: Record<string, unknown>, key: string): number {
  return source[key] === undefined || source[key] === null ? 0 : count(source, key);
}

/** parsePublicPlanPrices validates GET /billing/v1/plan-prices.
 *
 * parsePublicPlanPrices 校验 GET /billing/v1/plan-prices。 */
export function parsePublicPlanPrices(value: unknown): PublicPlanPrices {
  const source = record(value);
  const updatedAt = source.updated_at === undefined || source.updated_at === null ? null : timestamp(source, "updated_at");
  return {
    updatedAt,
    plans: list(source.plans, entry => {
      const plan = record(entry);
      return {
        id: required(plan, "id"),
        name: required(plan, "name"),
        isDefault: boolean(plan, "default"),
        prices: list(plan.prices, raw => {
          const price = record(raw);
          return {
            model: required(price, "model"),
            items: list(price.items, rawItem => {
              const item = record(rawItem);
              return {
                meter: required(item, "meter"),
                variant: optionalText(item, "variant"),
                aboveInputTokens: optionalCount(item, "above_input_tokens"),
                priceMicroPerM: count(item, "price_micro_per_m"),
              };
            }),
          };
        }),
      };
    }),
  };
}

/** FolkbenchModel is one price row: one group × one model.
 *
 * FolkbenchModel 是一行价格：一个分组 × 一个模型。 */
export interface FolkbenchModel {
  group_name: string;
  model_name: string;
  input_price: number | null;
  output_price: number | null;
  cache_input_price: number | null;
  cache_create_price: number | null;
  cache_create_price_1h: number | null;
  enabled: boolean;
  note?: string;
}

/** FolkbenchDocument is the whole response body.
 *
 * FolkbenchDocument 是完整的响应体。 */
export interface FolkbenchDocument {
  success: true;
  schema_version: string;
  data: {
    site_name: string;
    site_domain: string;
    currency: "USD";
    price_unit: "per_1m_tokens";
    updated_at: string;
    models: FolkbenchModel[];
  };
}

/** FolkbenchSite is what the document says about the site. `fallbackUpdatedAt`
 * is used only when the control plane does not know when prices changed.
 *
 * FolkbenchSite 是文档中关于站点的信息。`fallbackUpdatedAt` 仅在控制面不知道价格变化
 * 时间时使用。 */
export interface FolkbenchSite {
  name: string;
  domain: string;
  fallbackUpdatedAt: string;
}

/** parentMeter returns the meter one level up, or null for a root.
 *
 * parentMeter 返回上一层计量项；根计量项返回 null。 */
function parentMeter(meter: string): string | null {
  const at = meter.lastIndexOf(".");
  return at < 0 ? null : meter.slice(0, at);
}

/** lookupBase returns meter's default-variant base-tier price in
 * micro-credits per million, falling back to its ancestors, or null when no
 * ancestor has one either.
 *
 * lookupBase 返回 meter 在默认变体、基础档的单价（micro 点 / 百万），没有时回落到祖先
 * 计量项；所有祖先都没有时返回 null。 */
export function lookupBase(items: SalePriceItem[], meter: string): number | null {
  for (let name: string | null = meter; name !== null; name = parentMeter(name)) {
    const hit = items.find(item => item.meter === name && item.variant === "" && item.aboveInputTokens === 0);
    if (hit) return hit.priceMicroPerM;
  }
  return null;
}

function toUSD(micro: number | null): number | null {
  return micro === null ? null : micro / MICRO_PER_UNIT;
}

/** tierNote describes long-context tiers the row's base prices leave out.
 *
 * tierNote 说明行内基础档价格未体现的长上下文档位。 */
function tierNote(items: SalePriceItem[]): string | undefined {
  const tiers = [...new Set(items.filter(item => item.variant === "" && item.aboveInputTokens > 0).map(item => item.aboveInputTokens))].sort((a, b) => a - b);
  if (tiers.length === 0) return undefined;
  return `higher prices apply above ${tiers.join(" / ")} input tokens`;
}

/** groupNames maps each plan id to its group name: the plan's name, or the
 * name with the id appended when other plans share it.
 *
 * groupNames 把每个方案 id 映射为分组名：方案名称；有其他方案同名时追加 id。 */
export function groupNames(plans: Pick<PlanPrices, "id" | "name">[]): Map<string, string> {
  const uses = new Map<string, number>();
  for (const plan of plans) uses.set(plan.name, (uses.get(plan.name) ?? 0) + 1);
  return new Map(plans.map(plan => [plan.id, (uses.get(plan.name) ?? 0) > 1 ? `${plan.name} (${plan.id})` : plan.name]));
}

function byModel(a: { model: string }, b: { model: string }): number {
  return a.model < b.model ? -1 : a.model > b.model ? 1 : 0;
}

/** folkbenchPricing converts the plan prices into one row per plan × model,
 * plans in the control plane's order (the default plan first) and models
 * sorted within each. The default-price alias and any model with neither an
 * input nor an output token price are left out: Folkbench rows are per-token
 * prices.
 *
 * folkbenchPricing 把方案售价转换为每个「方案 × 模型」一行，方案保持控制面的顺序（默认
 * 方案在前），方案内按模型排序。默认价别名，以及既没有输入也没有输出 token 单价的模型
 * 不列出：Folkbench 的行是按 token 计的价格。 */
export function folkbenchPricing(source: PublicPlanPrices, site: FolkbenchSite): FolkbenchDocument {
  const names = groupNames(source.plans);
  const models: FolkbenchModel[] = [];
  for (const plan of source.plans) {
    for (const price of [...plan.prices].sort(byModel)) {
      if (price.model === DEFAULT_PRICE_MODEL) continue;
      const input = lookupBase(price.items, METER_INPUT);
      const output = lookupBase(price.items, METER_OUTPUT);
      if (input === null && output === null) continue;
      const row: FolkbenchModel = {
        group_name: names.get(plan.id) ?? plan.name,
        model_name: price.model,
        input_price: toUSD(input),
        output_price: toUSD(output),
        cache_input_price: toUSD(lookupBase(price.items, METER_CACHE_READ)),
        cache_create_price: toUSD(lookupBase(price.items, METER_CACHE_WRITE)),
        cache_create_price_1h: toUSD(lookupBase(price.items, METER_CACHE_WRITE_1H)),
        enabled: true,
      };
      const note = tierNote(price.items);
      if (note) row.note = note;
      models.push(row);
    }
  }
  return {
    success: true,
    schema_version: FOLKBENCH_SCHEMA_VERSION,
    data: {
      site_name: site.name,
      site_domain: site.domain,
      currency: "USD",
      price_unit: "per_1m_tokens",
      updated_at: source.updatedAt ?? site.fallbackUpdatedAt,
      models,
    },
  };
}

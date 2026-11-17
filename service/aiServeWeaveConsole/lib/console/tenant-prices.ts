import { integer, list, record, required, text } from "./billing.ts";
import { compareMeters } from "./usage-records.ts";

/**
 * This module mirrors GET /admin/v1/billing/prices
 * (internal/types.TenantPricesResponse): the sale prices of every model the
 * tenant may use, on its price plan. Prices are micro-credits per million
 * units (per million tokens for a token meter); one credit is one US dollar.
 *
 * 本模块镜像 GET /admin/v1/billing/prices（internal/types.TenantPricesResponse）：
 * 本租户按其价格方案可用的每个模型的售价。单价是「micro 点 / 百万单位」（token 计量项
 * 即每百万 token）；1 点 = 1 美元。
 */

/** TenantPriceItem is one sale price.
 * TenantPriceItem 是一条售价。 */
export interface TenantPriceItem {
  meter: string;
  variant: string;
  aboveInputTokens: number;
  saleMicroPerM: number;
}

/** TenantPrice is one model's sale prices and the tenant's combined rate.
 * TenantPrice 是一个模型的售价，以及本租户的总倍率。 */
export interface TenantPrice {
  model: string;
  source: string;
  ratePpm: number;
  items: TenantPriceItem[];
}

function parseItem(value: unknown): TenantPriceItem {
  const source = record(value);
  const above = source.above_input_tokens;
  return {
    meter: required(source, "meter"),
    variant: source.variant === undefined || source.variant === null ? "" : text(source, "variant"),
    aboveInputTokens: above === undefined || above === null ? 0 : integer(source, "above_input_tokens"),
    saleMicroPerM: integer(source, "sale_micro_per_m"),
  };
}

function parsePrice(value: unknown): TenantPrice {
  const source = record(value);
  return {
    model: required(source, "model"),
    source: text(source, "source"),
    ratePpm: integer(source, "rate_ppm"),
    items: list(source.items, parseItem),
  };
}

/** parseTenantPrices validates a tenant prices response.
 * parseTenantPrices 校验一次租户价格响应。 */
export function parseTenantPrices(value: unknown): TenantPrice[] {
  return list(record(value).items, parsePrice);
}

/** PriceTier is the prices that apply above one input threshold (0 for the
 * base tier) under one variant ("" for the default service tier).
 *
 * PriceTier 是在某个变体（"" 为默认服务等级）下、输入超过某个阈值（基础档为 0）时
 * 适用的单价。 */
export interface PriceTier {
  variant: string;
  aboveInputTokens: number;
  items: TenantPriceItem[];
}

/** priceTiers groups a model's prices into tiers: the default variant's base
 * tier first, then its higher tiers, then other variants; meters in display
 * order within a tier.
 *
 * priceTiers 把一个模型的单价按档位分组：先是默认变体的基础档，再是其更高的档位，最后是
 * 其他变体；档位内的计量项按展示顺序排列。 */
export function priceTiers(price: Pick<TenantPrice, "items">): PriceTier[] {
  const groups = new Map<string, PriceTier>();
  for (const item of price.items) {
    const key = `${item.variant}\u0000${item.aboveInputTokens}`;
    let tier = groups.get(key);
    if (!tier) {
      tier = { variant: item.variant, aboveInputTokens: item.aboveInputTokens, items: [] };
      groups.set(key, tier);
    }
    tier.items.push(item);
  }
  const tiers = [...groups.values()];
  for (const tier of tiers) tier.items.sort((a, b) => compareMeters(a.meter, b.meter));
  return tiers.sort((a, b) => {
    if (a.variant !== b.variant) {
      if (a.variant === "") return -1;
      if (b.variant === "") return 1;
      return a.variant < b.variant ? -1 : 1;
    }
    return a.aboveInputTokens - b.aboveInputTokens;
  });
}

/** basePrice returns the default variant's base-tier price of a meter, or
 * null when the model has none for it.
 *
 * basePrice 返回某计量项在默认变体基础档的单价；模型没有该单价时为 null。 */
export function basePrice(price: Pick<TenantPrice, "items">, meter: string): number | null {
  return price.items.find((item) => item.meter === meter && item.variant === "" && item.aboveInputTokens === 0)?.saleMicroPerM ?? null;
}

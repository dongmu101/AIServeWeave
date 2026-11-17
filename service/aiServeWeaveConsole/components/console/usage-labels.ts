import type { MessageKey } from "@/lib/i18n/messages";
import type { Translator } from "@/lib/i18n/translate";
import type { KnownMeter, RecordPricing } from "@/lib/console/usage-records";
import { isKnownMeter } from "@/lib/console/usage-records";

/**
 * Labels for token pricing shown on tenant pages: meters, price sources and
 * how a request's pricing reads. A meter or source this release does not
 * know is shown by its raw name, so a newer control plane never breaks the
 * page.
 *
 * 租户页面上 token 计价相关的标签：计量项、价格来源，以及一个请求的计价状态。本版本不
 * 认识的计量项或来源显示原始名称，使较新的控制面不会让页面出错。
 */

const METER_KEYS: Record<KnownMeter, MessageKey> = {
  input: "tenant.meterInput",
  "input.cache_read": "tenant.meterCacheRead",
  "input.cache_write": "tenant.meterCacheWrite",
  "input.cache_write.1h": "tenant.meterCacheWrite1h",
  "input.cache_write.12h": "tenant.meterCacheWrite12h",
  output: "tenant.meterOutput",
  "output.reasoning": "tenant.meterReasoning",
};

/** meterLabel names a meter. 为计量项命名。 */
export function meterLabel(meter: string, t: Translator): string {
  return isKnownMeter(meter) ? t(METER_KEYS[meter]) : meter;
}

const SOURCE_KEYS: Record<string, MessageKey> = {
  catalog: "tenant.priceSourceCatalog",
  override: "tenant.priceSourceOverride",
  default: "tenant.priceSourceDefault",
  plan_fixed: "tenant.priceSourcePlanFixed",
  none: "tenant.pricingUnpriced",
  legacy: "tenant.pricingLegacy",
};

/** priceSourceLabel names where a request's prices came from.
 * priceSourceLabel 说明一个请求的单价来源。 */
export function priceSourceLabel(source: string, t: Translator): string {
  if (source === "") return t("tenant.pricingPreUpgrade");
  return source in SOURCE_KEYS ? t(SOURCE_KEYS[source]!) : source;
}

/** pricingNoteKey is the badge a request's pricing shows, or null for an
 * ordinary priced request (and with billing off).
 *
 * pricingNoteKey 是一个请求的计价状态徽标；普通已计价的请求（以及计费关闭时）为 null。 */
export function pricingNoteKey(kind: RecordPricing): MessageKey | null {
  switch (kind) {
    case "pre_upgrade":
      return "tenant.pricingPreUpgrade";
    case "legacy":
      return "tenant.pricingLegacy";
    case "own_node":
      return "tenant.pricingOwnNode";
    case "unpriced":
      return "tenant.pricingUnpriced";
    default:
      return null;
  }
}

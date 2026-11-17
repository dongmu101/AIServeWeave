import { test } from "node:test";
import assert from "node:assert/strict";

import { ApiError } from "./errors.ts";
import { basePrice, parseTenantPrices, priceTiers } from "./tenant-prices.ts";

test("parses tenant prices and groups them into tiers", () => {
  const prices = parseTenantPrices({
    items: [{
      model: "claude-x", source: "catalog", rate_ppm: 800_000,
      items: [
        { meter: "output", base_micro_per_m: 15_000_000, sale_micro_per_m: 12_000_000 },
        { meter: "input", above_input_tokens: 200_000, base_micro_per_m: 6_000_000, sale_micro_per_m: 4_800_000 },
        { meter: "input", base_micro_per_m: 3_000_000, sale_micro_per_m: 2_400_000 },
        { meter: "input.cache_read", base_micro_per_m: 300_000, sale_micro_per_m: 240_000 },
        { meter: "input", variant: "priority", base_micro_per_m: 6_000_000, sale_micro_per_m: 4_800_000 },
      ],
    }],
  });
  const price = prices[0]!;
  assert.equal(price.ratePpm, 800_000);
  const tiers = priceTiers(price);
  assert.deepEqual(tiers.map((tier) => [tier.variant, tier.aboveInputTokens]), [["", 0], ["", 200_000], ["priority", 0]]);
  assert.deepEqual(tiers[0]?.items.map((item) => item.meter), ["input", "input.cache_read", "output"]);
  assert.equal(basePrice(price, "input"), 2_400_000);
  assert.equal(basePrice(price, "input.cache_write"), null);
});

test("an empty response is no prices, and malformed items are rejected", () => {
  assert.deepEqual(parseTenantPrices({}), []);
  assert.throws(() => parseTenantPrices({ items: [{ model: "m", rate_ppm: 1, items: [{ meter: "input" }] }] }), ApiError);
  assert.throws(() => parseTenantPrices({ items: [{ rate_ppm: 1, items: [] }] }), ApiError);
});

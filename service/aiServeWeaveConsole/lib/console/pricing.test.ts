import assert from "node:assert/strict";
import test from "node:test";

import { ApiError } from "./errors.ts";
import {
  catalogItems,
  emptyPlanDraft,
  emptyPlanTerm,
  emptyPriceDraft,
  findViewItem,
  formatMaxChange,
  formatRate,
  isConflict,
  parseCatalogPage,
  parseCatalogPendingPage,
  parseCatalogSyncResult,
  parseEffectivePrices,
  parsePlanList,
  parsePricingConfig,
  parseRateInput,
  parseTenantPricing,
  planDraft,
  planRequest,
  priceChanges,
  priceDraft,
  priceItemsBody,
  priceListFromDraft,
  tiersOf,
  type PriceItem,
} from "./pricing.ts";

const at = "2026-11-16T08:00:00Z";
const revision = { id: 3, effective_at: at, created_by: "op", summary: "set model x", created_at: at };

test("the pricing configuration parses, null lists included", () => {
  const config = parsePricingConfig({
    revision,
    default_prices: null,
    models: [{ model: "org/claude", catalog_model: "claude-x", prices: null, rate_ppm: 1_500_000 }],
    plans: [{ id: "plan_default", name: "Default", default_rate_ppm: 1_000_000, is_default: true, models: null }],
    default_plan: "plan_default",
  });
  assert.equal(config.revision.id, 3);
  assert.deepEqual(config.defaultPrices, []);
  assert.deepEqual(config.models[0], { model: "org/claude", catalogModel: "claude-x", prices: [], ratePPM: 1_500_000 });
  assert.equal(config.plans[0].tenantCount, null);
  assert.throws(() => parsePricingConfig({ revision, models: [], plans: [] }), (e: unknown) => e instanceof ApiError && e.kind === "contract");
});

test("plans keep a factor or a fixed price per alias", () => {
  const plans = parsePlanList({ revision, items: [
    { id: "plan_vip", name: "VIP", default_rate_ppm: 800_000, is_default: false, tenant_count: 2, models: [
      { model: "a", rate_ppm: 900_000 },
      { model: "b", prices: [{ meter: "input", price_micro_per_m: 100_000 }, { meter: "output", price_micro_per_m: 200_000 }] },
    ] },
  ] });
  const vip = plans.items[0];
  assert.equal(vip.tenantCount, 2);
  assert.deepEqual(vip.models[0], { model: "a", ratePPM: 900_000, prices: [] });
  assert.equal(vip.models[1].ratePPM, null);
  assert.deepEqual(vip.models[1].prices[0], { meter: "input", variant: "", aboveInputTokens: 0, priceMicroPerM: 100_000 });
});

test("effective prices parse with sources and the unpriced report", () => {
  const prices = parseEffectivePrices({
    revision: 3, plan_id: "plan_default", unpriced_requests_24h: 7, unpriced_aliases: ["local-llama"],
    items: [{ model: "claude-x", routed: true, source: "catalog", ref: "claude-x", model_rate_ppm: 1_000_000, plan_rate_ppm: 1_000_000, rate_ppm: 1_000_000,
      items: [{ meter: "input", base_micro_per_m: 3_000_000, sale_micro_per_m: 3_000_000 }, { meter: "input", above_input_tokens: 200_000, base_micro_per_m: 6_000_000, sale_micro_per_m: 6_000_000 }] }],
  });
  assert.deepEqual(prices.unpricedAliases, ["local-llama"]);
  assert.equal(prices.unpricedRequests24h, 7);
  assert.equal(findViewItem(prices.items[0].items, "input", 200_000)?.baseMicroPerM, 6_000_000);
  assert.deepEqual(tiersOf(prices.items[0].items), [0, 200_000]);
  assert.throws(() => parseEffectivePrices({ items: [{ model: "x", source: "free", rate_ppm: 0, items: [] }] }));
});

test("catalog pages carry the sync state and derived marks", () => {
  const catalog = parseCatalogPage({
    items: [{ model: "claude-x", provider: "anthropic", mode: "chat", status: "active", prices: [
      { meter: "input", price_micro_per_m: 3_000_000 },
      { meter: "input.cache_read", above_input_tokens: 200_000, price_micro_per_m: 600_000, derived: true },
    ], updated_at: at }],
    next_cursor: "claude-x",
    sync: { source: "embedded", entry_count: 3689, version: 4, synced_at: at, last_error: "" },
  });
  assert.equal(catalog.nextCursor, "claude-x");
  assert.equal(catalog.sync.entryCount, 3689);
  assert.equal(catalog.sync.checkedAt, null);
  assert.equal(catalog.items[0].prices[1].derived, true);
  assert.deepEqual(catalogItems(catalog.items[0].prices)[1], { meter: "input.cache_read", variant: "", aboveInputTokens: 200_000, priceMicroPerM: 600_000 });
});

test("pending reviews and sync results parse", () => {
  const pending = parseCatalogPendingPage({ items: [
    { model: "gpt-x", kind: "change", status: "active", max_change_bp: 5_000, detected_at: at, current: [{ meter: "input", price_micro_per_m: 2_000_000 }], proposed: [{ meter: "input", price_micro_per_m: 3_000_000 }] },
    { model: "old", kind: "remove", status: "removed_upstream", max_change_bp: null, detected_at: at, current: [] },
  ] });
  assert.equal(pending.items[0].maxChangeBP, 5_000);
  assert.equal(pending.items[1].maxChangeBP, null);
  assert.deepEqual(pending.items[1].proposed, []);
  assert.equal(formatMaxChange(5_000), "50%");
  assert.equal(formatMaxChange(1_234), "12.34%");
  assert.equal(formatMaxChange(null), null);
  assert.deepEqual(parseCatalogSyncResult({ skipped: false, added: ["a", "b"], applied: [{ model: "c", max_change_bp: 10 }], reviewed: null, removed: [], version: 5 }),
    { skipped: false, added: 2, applied: 1, reviewed: 0, removed: 0 });
});

test("price changes line up by key and skip unchanged prices", () => {
  const current: PriceItem[] = [
    { meter: "input", variant: "", aboveInputTokens: 0, priceMicroPerM: 2_000_000 },
    { meter: "output", variant: "", aboveInputTokens: 0, priceMicroPerM: 8_000_000 },
    { meter: "input.cache_read", variant: "", aboveInputTokens: 0, priceMicroPerM: 500_000 },
  ];
  const proposed: PriceItem[] = [
    { meter: "input", variant: "", aboveInputTokens: 0, priceMicroPerM: 3_000_000 },
    { meter: "output", variant: "", aboveInputTokens: 0, priceMicroPerM: 8_000_000 },
    { meter: "input.cache_write", variant: "", aboveInputTokens: 0, priceMicroPerM: 1_000_000 },
    { meter: "input.cache_read", variant: "", aboveInputTokens: 0, priceMicroPerM: 0 },
  ];
  assert.deepEqual(priceChanges(current, proposed).map((c) => [c.meter, c.before, c.after, c.changeBP]), [
    ["input", 2_000_000, 3_000_000, 5_000],
    ["input.cache_read", 500_000, 0, null],
    ["input.cache_write", null, 1_000_000, null],
  ]);
});

test("rates are typed as multiples and shown with an x", () => {
  assert.equal(parseRateInput("1.5"), 1_500_000);
  assert.equal(parseRateInput("0"), 0);
  assert.equal(parseRateInput("100"), 100_000_000);
  assert.equal(parseRateInput("100.000001"), null);
  assert.equal(parseRateInput("-1"), null);
  assert.equal(parseRateInput("0.0000001"), null);
  assert.equal(formatRate(1_500_000), "1.5x");
  assert.equal(formatRate(1_000_000), "1x");
});

test("price forms edit base prices and keep tiers", () => {
  const items: PriceItem[] = [
    { meter: "input", variant: "", aboveInputTokens: 0, priceMicroPerM: 3_000_000 },
    { meter: "output", variant: "", aboveInputTokens: 0, priceMicroPerM: 15_000_000 },
    { meter: "input", variant: "", aboveInputTokens: 200_000, priceMicroPerM: 6_000_000 },
    { meter: "input", variant: "priority", aboveInputTokens: 0, priceMicroPerM: 5_000_000 },
  ];
  const draft = priceDraft(items);
  assert.equal(draft.input, "3");
  assert.equal(draft.output, "15");
  assert.equal(draft["input.cache_read"], "");
  draft["input.cache_read"] = "0.3";
  draft.input = "2.5";
  const built = priceListFromDraft(draft, items);
  assert.equal(built.problem, null);
  assert.deepEqual(built.prices?.map((p) => [p.meter, p.variant, p.aboveInputTokens, p.priceMicroPerM]), [
    ["input", "", 200_000, 6_000_000],
    ["input", "priority", 0, 5_000_000],
    ["input", "", 0, 2_500_000],
    ["output", "", 0, 15_000_000],
    ["input.cache_read", "", 0, 300_000],
  ]);
  assert.deepEqual(priceListFromDraft({ ...emptyPriceDraft(), input: "1" }, []).problem, { kind: "missing", meter: "output" });
  assert.deepEqual(priceListFromDraft({ ...emptyPriceDraft(), input: "1", output: "x" }, []).problem, { kind: "invalid", meter: "output" });
  assert.deepEqual(priceListFromDraft({ ...emptyPriceDraft(), input: "1", output: "2", "input.cache_write": "1.0000001" }, []).problem, { kind: "invalid", meter: "input.cache_write" });
  assert.deepEqual(priceItemsBody([{ meter: "input", variant: "", aboveInputTokens: 0, priceMicroPerM: 1 }, { meter: "input", variant: "flex", aboveInputTokens: 128_000, priceMicroPerM: 2 }]),
    [{ meter: "input", price_micro_per_m: 1 }, { meter: "input", variant: "flex", above_input_tokens: 128_000, price_micro_per_m: 2 }]);
});

test("plan forms round-trip and validate", () => {
  const draft = planDraft({ id: "plan_vip", name: "VIP", defaultRatePPM: 800_000, isDefault: false, tenantCount: 1, models: [
    { model: "a", ratePPM: 900_000, prices: [] },
    { model: "b", ratePPM: null, prices: [{ meter: "input", variant: "", aboveInputTokens: 0, priceMicroPerM: 100_000 }, { meter: "output", variant: "", aboveInputTokens: 0, priceMicroPerM: 200_000 }] },
  ] });
  assert.equal(draft.defaultRate, "0.8");
  assert.equal(draft.terms[0].rate, "0.9");
  assert.equal(draft.terms[1].mode, "fixed");
  assert.deepEqual(planRequest(draft).body, { name: "VIP", default_rate_ppm: 800_000, models: [
    { model: "a", rate_ppm: 900_000 },
    { model: "b", prices: [{ meter: "input", price_micro_per_m: 100_000 }, { meter: "output", price_micro_per_m: 200_000 }] },
  ] });

  assert.deepEqual(planRequest({ ...emptyPlanDraft(), name: " " }).problem, { kind: "name" });
  assert.deepEqual(planRequest({ ...emptyPlanDraft(), name: "x", defaultRate: "101" }).problem, { kind: "default_rate" });
  const dup = { ...emptyPlanDraft(), name: "x", terms: [{ ...emptyPlanTerm(), model: "a" }, { ...emptyPlanTerm(), model: " a " }] };
  assert.deepEqual(planRequest(dup).problem, { kind: "duplicate", row: 1 });
  assert.deepEqual(planRequest({ ...emptyPlanDraft(), name: "x", terms: [{ ...emptyPlanTerm(), model: "a", rate: "abc" }] }).problem, { kind: "rate", row: 0 });
  assert.deepEqual(planRequest({ ...emptyPlanDraft(), name: "x", terms: [{ ...emptyPlanTerm(), model: "a", mode: "fixed" }] }).problem,
    { kind: "price", row: 0, problem: { kind: "missing", meter: "input" } });
});

test("tenant rows yield their plan and pending usage", () => {
  const rows = parseTenantPricing({ items: [
    { id: "tnt_a", price_plan_id: "plan_vip", pending_usage: { amount_micro: -400, request_count: 3, since: at, seal_at: at } },
    { id: "tnt_b" },
  ] });
  assert.deepEqual(rows.get("tnt_a"), { pricePlanId: "plan_vip", pendingUsage: { amountMicro: -400, requestCount: 3, since: at, sealAt: at } });
  assert.deepEqual(rows.get("tnt_b"), { pricePlanId: "", pendingUsage: null });
});

test("a 409 is a configuration conflict", () => {
  assert.equal(isConflict(new ApiError("conflict", 409)), true);
  assert.equal(isConflict(new ApiError("server", 500)), false);
  assert.equal(isConflict(new Error("x")), false);
});

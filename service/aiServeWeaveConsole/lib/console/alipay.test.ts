import { test } from "node:test";
import assert from "node:assert/strict";

import {
  actionOutcome,
  checkRedeemAmount,
  formatPlanPrice,
  newActionId,
  parseAlipayOverview,
  parseAlipayPurchase,
  parseAlipayRedemption,
  parseAlipayRedemptionPage,
  quotaShort,
} from "./alipay.ts";
import { ApiError } from "./errors.ts";

const at = "2026-11-11T08:00:00Z";
const redemption = { id: "apr_1", action_id: "a-1234567", amount: 5, quota_unit: "POINT", credits_micro: 10_000_000, status: "pending", reason_code: "", created_at: at, updated_at: at };
const overviewWire = {
  capability_status: "ENABLED", product_name: "点数包", billing_mode: "QUANTITY", quota_unit: "POINT",
  supported: true, can_purchase: true, can_redeem: true,
  plans: [{ sku_id: "sku_1", name: "100 积分", price: "9.90", currency: "CNY", quota_unit: "POINT", quota_amount: 100 }],
  access: { total: 100, used: 40, remaining: 60 }, credits_per_unit_micro: 2_000_000, max_amount: 99_999_999, pending: redemption,
};

test("parseAlipayOverview reads the contract and its optional parts", () => {
  const overview = parseAlipayOverview(overviewWire);
  assert.equal(overview.plans[0]?.price, "9.90");
  assert.deepEqual(overview.access, { total: 100, used: 40, remaining: 60 });
  assert.equal(overview.pending?.id, "apr_1");
  const bare = parseAlipayOverview({ ...overviewWire, access: null, pending: null, plans: null });
  assert.equal(bare.access, null);
  assert.equal(bare.pending, null);
  assert.deepEqual(bare.plans, []);
  assert.throws(() => parseAlipayOverview({ ...overviewWire, supported: "true" }), ApiError);
  assert.throws(() => parseAlipayOverview({ ...overviewWire, pending: { ...redemption, status: "succeeded" } }), ApiError, "pending must be pending");
  assert.throws(() => parseAlipayOverview({ ...overviewWire, plans: [{ sku_id: "", name: "x", price: "1", currency: "CNY", quota_unit: "POINT", quota_amount: 1 }] }), ApiError);
});

test("parseAlipayRedemption refuses unknown statuses and amounts", () => {
  assert.equal(parseAlipayRedemption({ ...redemption, status: "succeeded" }).creditsMicro, 10_000_000);
  assert.throws(() => parseAlipayRedemption({ ...redemption, status: "paid" }), ApiError);
  assert.throws(() => parseAlipayRedemption({ ...redemption, amount: 0 }), ApiError);
  assert.throws(() => parseAlipayRedemption({ ...redemption, credits_micro: -1 }), ApiError);
  const page = parseAlipayRedemptionPage({ items: [redemption], next_cursor: "" });
  assert.equal(page.items.length, 1);
  assert.equal(page.nextCursor, null);
});

test("parseAlipayPurchase only accepts an https entry", () => {
  assert.equal(parseAlipayPurchase({ id: "apu_1", purchase_url: "https://openapi.alipay.com/x", expire_time: "" }).purchaseUrl, "https://openapi.alipay.com/x");
  for (const bad of ["javascript:alert(1)", "http://openapi.alipay.com/x", "/relative", ""]) {
    assert.throws(() => parseAlipayPurchase({ id: "apu_1", purchase_url: bad }), ApiError, bad);
  }
});

test("checkRedeemAmount mirrors the control plane's bounds", () => {
  const points = { maxAmount: 99_999_999, creditsPerUnitMicro: 2_000_000 };
  assert.deepEqual(checkRedeemAmount(" 12 ", points), { amount: 12, creditsMicro: 24_000_000, problem: null });
  for (const bad of ["", "0", "-1", "1.5", "1e3", "abc", "01"]) {
    assert.equal(checkRedeemAmount(bad, points).problem, "invalid", bad);
  }
  assert.equal(checkRedeemAmount("100000000", points).problem, "above_max");
  assert.equal(checkRedeemAmount("2", { maxAmount: 1, creditsPerUnitMicro: 1 }).problem, "above_max", "a count package redeems one at a time");
  assert.equal(checkRedeemAmount("99999999", { maxAmount: 99_999_999, creditsPerUnitMicro: 1_000_000_000_000 }).problem, "above_max", "credits beyond a safe integer");
});

test("a write's action id survives only an unknown outcome", () => {
  assert.equal(actionOutcome(new ApiError("network")), "unknown");
  assert.equal(actionOutcome(new ApiError("timeout")), "unknown");
  assert.equal(actionOutcome(new ApiError("server", 503)), "unknown");
  assert.equal(actionOutcome(new ApiError("conflict", 409)), "definite");
  assert.equal(actionOutcome(new ApiError("invalid", 400)), "definite");
  assert.equal(newActionId(() => "fixed-id-1234"), "fixed-id-1234");
  assert.match(newActionId(), /^[A-Za-z0-9_-]{8,64}$/, "matches the control plane's action id pattern");
});

test("quota and price helpers", () => {
  assert.equal(quotaShort({ access: { total: 1, used: 0, remaining: 3 } }, 4), true);
  assert.equal(quotaShort({ access: { total: 1, used: 0, remaining: 3 } }, 3), false);
  assert.equal(quotaShort({ access: null }, 4), false, "an unknown quota does not block");
  assert.match(formatPlanPrice({ price: "9.90", currency: "CNY" }, "zh-CN"), /9\.90/);
  assert.equal(formatPlanPrice({ price: "about 10", currency: "CNY" }, "zh-CN"), "about 10 CNY");
});

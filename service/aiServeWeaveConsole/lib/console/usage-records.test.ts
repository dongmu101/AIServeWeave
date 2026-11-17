import { test } from "node:test";
import assert from "node:assert/strict";

import { parseLedgerPage } from "./billing.ts";
import { ApiError } from "./errors.ts";
import {
  compareMeters,
  formatRate,
  isKnownMeter,
  isoToLocalInput,
  LEDGER_EDGE_MS,
  ledgerUsageHref,
  meterDepth,
  parseUsageRecordPage,
  readUsageLink,
  recordPricing,
  sortedMeters,
} from "./usage-records.ts";

const at = "2026-11-16T08:00:00Z";

test("parses a priced record with its meter lines", () => {
  const page = parseUsageRecordPage({
    items: [{
      request_id: "req_1", created_at: at, model: "claude-x", endpoint: "anthropic_messages", key_id: "key_1", user_id: "usr_1",
      prompt_tokens: 1000, completion_tokens: 100, total_tokens: 1100, cache_read_tokens: 600, cache_write_tokens: 300,
      meters: [
        { meter: "output", quantity: 100, billed: 100, price_micro_per_m: 15_000_000, cost_micro: 1500 },
        { meter: "input", quantity: 1000, billed: 100, price_micro_per_m: 3_000_000, cost_micro: 300 },
        { meter: "input.cache_read", quantity: 600, billed: 600, price_micro_per_m: 300_000, cost_micro: 180 },
      ],
      cost_micro: 3105, base_cost_micro: 3105, rate_ppm: 1_000_000, price_source: "catalog", charged: true, tier: 200000,
    }],
    next_cursor: "c2",
  });
  const r = page.items[0]!;
  assert.equal(page.nextCursor, "c2");
  assert.equal(r.costMicro, 3105);
  assert.equal(r.ratePpm, 1_000_000);
  assert.equal(r.charged, true);
  assert.equal(r.tier, 200000);
  assert.equal(recordPricing(r), "priced");
  assert.deepEqual(sortedMeters(r.meters).map((l) => l.meter), ["input", "input.cache_read", "output"]);
});

test("a record with billing off has no pricing fields", () => {
  const r = parseUsageRecordPage({
    items: [{ request_id: "req_2", created_at: at, model: "m", endpoint: "chat", prompt_tokens: 3, completion_tokens: 2, total_tokens: 5,
      meters: [{ meter: "input", quantity: 3, billed: 3 }, { meter: "output", quantity: 2, billed: 2 }] }],
  }).items[0]!;
  assert.equal(r.costMicro, null);
  assert.equal(r.charged, null);
  assert.equal(r.cacheReadTokens, 0);
  assert.equal(r.meters[0]?.priceMicroPerM, null);
  assert.equal(recordPricing(r), "off");
});

test("recordPricing tells upgrade, legacy, own-node and unpriced records apart", () => {
  const base = { costMicro: 0, priceSource: "catalog", ownNode: false, unpriced: false };
  assert.equal(recordPricing({ ...base, priceSource: "" }), "pre_upgrade");
  assert.equal(recordPricing({ ...base, priceSource: "legacy", costMicro: 120 }), "legacy");
  assert.equal(recordPricing({ ...base, ownNode: true }), "own_node");
  assert.equal(recordPricing({ ...base, priceSource: "none" }), "unpriced");
  assert.equal(recordPricing({ ...base, unpriced: true }), "unpriced");
  assert.equal(recordPricing({ ...base, priceSource: "plan_fixed", costMicro: 10 }), "priced");
});

test("rejects malformed records", () => {
  assert.throws(() => parseUsageRecordPage({ items: [{ created_at: at, model: "m", prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }] }), ApiError);
  assert.throws(() => parseUsageRecordPage({ items: [{ request_id: "r", created_at: at, model: "m", prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, cost_micro: 1.5 }] }), ApiError);
  assert.throws(() => parseUsageRecordPage({ items: [{ request_id: "r", created_at: at, model: "m", prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, own_node: "yes" }] }), ApiError);
  assert.throws(() => parseUsageRecordPage({ items: [{ request_id: "r", created_at: at, model: "m", prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, meters: [{ quantity: 1, billed: 1 }] }] }), ApiError);
});

test("formatRate writes parts per million as a multiplier", () => {
  assert.equal(formatRate(1_200_000, "en-US"), "1.2×");
  assert.equal(formatRate(1_000_000, "en-US"), "1×");
  assert.equal(formatRate(333_333, "en-US"), "0.333333×");
  assert.equal(formatRate(0, "en-US"), "0×");
});

test("meters order known names first under their root, then the rest by name", () => {
  assert.deepEqual(["output", "input.zz_new", "input.cache_write.1h", "input", "image"].sort(compareMeters),
    ["input", "input.cache_write.1h", "output", "image", "input.zz_new"]);
  assert.equal(isKnownMeter("input.cache_write.1h"), true);
  assert.equal(isKnownMeter("input.audio"), false);
  assert.equal(meterDepth("input"), 0);
  assert.equal(meterDepth("input.cache_write.1h"), 2);
});

test("ledgerUsageHref widens the window by the push delay before the first charge", () => {
  const href = ledgerUsageHref("2026-11-16T08:00:00.000Z", "2026-11-16T09:00:00.000Z");
  const url = new URL(href, "http://console");
  assert.equal(url.pathname, "/console/usage");
  assert.equal(url.searchParams.get("tab"), "records");
  assert.equal(Date.parse(url.searchParams.get("since")!), Date.parse("2026-11-16T08:00:00Z") - LEDGER_EDGE_MS);
  assert.equal(url.searchParams.get("until"), "2026-11-16T09:00:01.000Z");
  assert.deepEqual(readUsageLink(Object.fromEntries(url.searchParams)), {
    tab: "records", since: url.searchParams.get("since"), until: "2026-11-16T09:00:01.000Z",
  });
});

test("readUsageLink drops unknown tabs and malformed times", () => {
  assert.deepEqual(readUsageLink({}), { tab: "model", since: "", until: "" });
  assert.deepEqual(readUsageLink({ tab: "nope", since: "yesterday", until: ["a", "b"] }), { tab: "model", since: "", until: "" });
  assert.equal(readUsageLink({ tab: "key" }).tab, "key");
});

test("isoToLocalInput is the inverse of a datetime-local value", () => {
  const local = isoToLocalInput("2026-11-16T08:30:00.000Z");
  assert.match(local, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/);
  assert.equal(new Date(local).toISOString(), "2026-11-16T08:30:00.000Z");
  assert.equal(isoToLocalInput(""), "");
  assert.equal(isoToLocalInput("nope"), "");
});

test("ledger pages carry period fields and the pending usage", () => {
  const ledger = parseLedgerPage({
    items: [{ id: "led_1", kind: "usage", amount_micro: -50, balance_after_micro: 950, ref: "2 requests", created_at: at,
      period_start: "2026-11-16T07:00:00Z", period_end: "2026-11-16T07:59:00Z", request_count: 2 }],
    pending_usage: { amount_micro: -40, request_count: 3, since: at, last: at, seal_at: "2026-11-17T00:00:00Z" },
  });
  assert.equal(ledger.items[0]?.requestCount, 2);
  assert.equal(ledger.items[0]?.periodStart, "2026-11-16T07:00:00Z");
  assert.deepEqual(ledger.pendingUsage, { amountMicro: -40, requestCount: 3, since: at, last: at, sealAt: "2026-11-17T00:00:00Z" });
  const old = parseLedgerPage({ items: [{ id: "led_2", kind: "usage", amount_micro: -1, balance_after_micro: 1, ref: "1 requests", created_at: at }] });
  assert.equal(old.items[0]?.periodStart, null);
  assert.equal(old.items[0]?.requestCount, 0);
  assert.equal(old.pendingUsage, null);
  assert.throws(() => parseLedgerPage({ items: [], pending_usage: { amount_micro: -1, request_count: -1 } }), ApiError);
  assert.throws(() => parseLedgerPage({ items: [{ id: "l", kind: "usage", amount_micro: 1, balance_after_micro: 1, ref: "", created_at: at, period_start: "x" }] }), ApiError);
});

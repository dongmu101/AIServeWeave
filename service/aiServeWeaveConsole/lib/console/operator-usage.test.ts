import { test } from "node:test";
import assert from "node:assert/strict";
import { groupUsage, rangeSince, usageTotals } from "./operator-usage.ts";
import { parseUsageSummary } from "./usage.ts";

const row = (tenant_id: string, model: string, total: number, extra: Record<string, number> = {}) => ({
  tenant_id, model, prompt_tokens: total - 10, completion_tokens: 10, total_tokens: total, request_count: 1, ...extra,
});

test("totals add every field and keep cost null only while billing is off", () => {
  const off = parseUsageSummary({ items: [row("t1", "m1", 100), row("t2", "m1", 50)] }).items;
  assert.deepEqual(usageTotals(off), { promptTokens: 130, completionTokens: 20, totalTokens: 150, requestCount: 2, cacheReadTokens: 0, cacheWriteTokens: 0, costMicro: null });
  const on = parseUsageSummary({ items: [row("t1", "m1", 100, { cost_micro: 7, cache_read_tokens: 40 }), row("t2", "m1", 50, { cost_micro: 0 })] }).items;
  assert.equal(usageTotals(on).costMicro, 7);
  assert.equal(usageTotals(on).cacheReadTokens, 40);
  assert.equal(usageTotals([]).costMicro, null);
});

test("groups fold by tenant or model with shares and the other dimension's breadth", () => {
  const items = parseUsageSummary({ items: [row("t1", "m1", 100), row("t1", "m2", 100), row("t2", "m1", 200), row("t3", "m3", 600)] }).items;
  assert.deepEqual(groupUsage(items, "tenant").map((g) => [g.id, g.totals.totalTokens, g.share, g.breadth]), [
    ["t3", 600, 0.6, 1],
    ["t1", 200, 0.2, 2],
    ["t2", 200, 0.2, 1],
  ]);
  assert.deepEqual(groupUsage(items, "model").map((g) => [g.id, g.totals.totalTokens, g.breadth]), [
    ["m3", 600, 1],
    ["m1", 300, 2],
    ["m2", 100, 1],
  ]);
});

test("a summary with no tokens has zero shares rather than NaN", () => {
  const items = parseUsageSummary({ items: [row("t1", "m1", 0, { prompt_tokens: 0, completion_tokens: 0 })] }).items;
  assert.equal(groupUsage(items, "tenant")[0].share, 0);
});

test("presets start 7 or 30 days back, and all is unbounded", () => {
  const now = Date.parse("2026-11-18T12:00:00Z");
  assert.equal(rangeSince("all", now), "");
  assert.equal(rangeSince("7d", now), "2026-11-11T12:00:00.000Z");
  assert.equal(rangeSince("30d", now), "2026-10-19T12:00:00.000Z");
});

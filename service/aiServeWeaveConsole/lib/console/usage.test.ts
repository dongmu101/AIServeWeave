import { test } from "node:test";
import assert from "node:assert/strict";
import { addCost, billingShown, parseUsageSummary, usageByModel } from "./usage.ts";
import { ApiError } from "./errors.ts";

test("parses a tenant-scoped usage summary without tenant_id", () => {
  const summary = parseUsageSummary({
    items: [
      {
        model: "gpt-oss-120b",
        prompt_tokens: 100,
        completion_tokens: 50,
        total_tokens: 150,
        request_count: 4,
      },
    ],
  });
  assert.equal(summary.items[0]?.tenantId, "");
  assert.equal(summary.items[0]?.model, "gpt-oss-120b");
  assert.equal(summary.items[0]?.totalTokens, 150);
  assert.equal(summary.items[0]?.requestCount, 4);
});

test("parses an operator-scoped usage summary with tenant_id", () => {
  const summary = parseUsageSummary({
    items: [
      {
        tenant_id: "tenant-1",
        model: "llama-3-70b",
        prompt_tokens: 10,
        completion_tokens: 5,
        total_tokens: 15,
        request_count: 1,
      },
    ],
  });
  assert.equal(summary.items[0]?.tenantId, "tenant-1");
});

test("a response with no items field parses as an empty list", () => {
  // Matches this module's list() convention (mirrored from lib/console/metrics.ts):
  // an absent array field is empty, not malformed.
  const summary = parseUsageSummary({});
  assert.deepEqual(summary.items, []);
});

test("rejects an item missing model", () => {
  assert.throws(
    () =>
      parseUsageSummary({
        items: [{ prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, request_count: 1 }],
      }),
    ApiError
  );
});

test("rejects an item missing a token field", () => {
  assert.throws(
    () =>
      parseUsageSummary({
        items: [{ model: "m", prompt_tokens: 1, completion_tokens: 1, request_count: 1 }],
      }),
    ApiError
  );
});

test("an empty items list parses cleanly", () => {
  const summary = parseUsageSummary({ items: [] });
  assert.deepEqual(summary.items, []);
});

test("parses a by-key usage summary with user_id and key_id", () => {
  const summary = parseUsageSummary({
    items: [
      { user_id: "usr_1", key_id: "key_1", model: "m", prompt_tokens: 3, completion_tokens: 2, total_tokens: 5, request_count: 1 },
      { model: "m", prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, request_count: 1 },
    ],
  });
  assert.equal(summary.items[0]?.userId, "usr_1");
  assert.equal(summary.items[0]?.keyId, "key_1");
  assert.equal(summary.items[1]?.userId, "");
  assert.equal(summary.items[1]?.keyId, "");
});

test("usageByModel folds rows per id and model, skipping unattributed rows", () => {
  const { items } = parseUsageSummary({
    items: [
      { user_id: "usr_1", key_id: "key_a", model: "m1", prompt_tokens: 1, completion_tokens: 2, total_tokens: 3, request_count: 1 },
      { user_id: "usr_1", key_id: "key_b", model: "m1", prompt_tokens: 10, completion_tokens: 20, total_tokens: 30, request_count: 1 },
      { user_id: "usr_1", key_id: "key_b", model: "m2", prompt_tokens: 100, completion_tokens: 200, total_tokens: 300, request_count: 1 },
      { model: "m1", prompt_tokens: 5, completion_tokens: 5, total_tokens: 10, request_count: 1 },
    ],
  });
  const byUser = usageByModel(items, (item) => item.userId);
  assert.deepEqual(byUser.get("usr_1"), [
    { model: "m2", promptTokens: 100, completionTokens: 200, costMicro: null },
    { model: "m1", promptTokens: 11, completionTokens: 22, costMicro: null },
  ]);
  assert.equal(byUser.size, 1);
  assert.deepEqual(usageByModel(items, (item) => item.keyId).get("key_a"), [{ model: "m1", promptTokens: 1, completionTokens: 2, costMicro: null }]);
});

test("cache tokens and cost are optional: absent from an older control plane or with billing off", () => {
  const { items } = parseUsageSummary({
    items: [
      { model: "m", prompt_tokens: 1000, completion_tokens: 5, total_tokens: 1005, request_count: 1, cache_read_tokens: 600, cache_write_tokens: 300, cost_micro: 3105 },
      { model: "m", prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, request_count: 1 },
    ],
  });
  assert.equal(items[0]?.cacheReadTokens, 600);
  assert.equal(items[0]?.cacheWriteTokens, 300);
  assert.equal(items[0]?.costMicro, 3105);
  assert.equal(items[1]?.cacheReadTokens, 0);
  assert.equal(items[1]?.costMicro, null);
  assert.equal(billingShown(items), true);
  assert.equal(billingShown(items.slice(1)), false);
  assert.throws(() => parseUsageSummary({ items: [{ model: "m", prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, request_count: 1, cost_micro: "1" }] }), ApiError);
});

test("usageByModel adds up costs, staying null while no row has one", () => {
  const { items } = parseUsageSummary({
    items: [
      { key_id: "key_a", model: "m1", prompt_tokens: 1, completion_tokens: 2, total_tokens: 3, request_count: 1, cost_micro: 10 },
      { key_id: "key_a", model: "m1", prompt_tokens: 1, completion_tokens: 2, total_tokens: 3, request_count: 1 },
      { key_id: "key_a", model: "m1", prompt_tokens: 1, completion_tokens: 2, total_tokens: 3, request_count: 1, cost_micro: 5 },
    ],
  });
  assert.equal(usageByModel(items, (item) => item.keyId).get("key_a")?.[0]?.costMicro, 15);
  assert.equal(addCost(null, null), null);
  assert.equal(addCost(null, 0), 0);
  assert.equal(addCost(3, null), 3);
});

import { test } from "node:test";
import assert from "node:assert/strict";
import { parseUsageSummary } from "./usage.ts";
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

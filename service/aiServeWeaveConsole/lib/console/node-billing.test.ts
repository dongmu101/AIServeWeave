import assert from "node:assert/strict";
import test from "node:test";

import { ApiError } from "./errors.ts";
import { parseNodeBilling, parseNodeBillingList } from "./node-billing.ts";

test("parseNodeBillingList returns the free nodes", () => {
  assert.deepEqual(parseNodeBillingList({ items: [{ node_id: "a", free: true }, { node_id: "b", free: true, updated_by: "u" }] }), new Set(["a", "b"]));
  assert.deepEqual(parseNodeBillingList({ items: [] }), new Set());
});

test("parseNodeBillingList refuses charged, duplicate or malformed rows", () => {
  assert.throws(() => parseNodeBillingList({ items: [{ node_id: "a", free: false }] }), ApiError);
  assert.throws(() => parseNodeBillingList({ items: [{ node_id: "a", free: true }, { node_id: "a", free: true }] }), ApiError);
  assert.throws(() => parseNodeBillingList({ items: [{ node_id: "a b", free: true }] }), ApiError);
  assert.throws(() => parseNodeBillingList({}), ApiError);
});

test("parseNodeBilling reads a write's answer", () => {
  assert.deepEqual(parseNodeBilling({ node_id: "a", free: false }), { nodeId: "a", free: false });
  assert.throws(() => parseNodeBilling({ node_id: "a" }), ApiError);
});

import test from "node:test";
import assert from "node:assert/strict";
import { parseAccountSale, parseAccountSales, parseAccountPurchase } from "./account-sales.ts";
import { parseBillingOverview } from "./billing.ts";
import { resolveUpstream, resolveOperatorUpstream } from "./upstream-routes.ts";

const sale = { id: "acc_a", node_id: "agent", runtime_id: "claude-a", name: "Claude A", price_micro: 100_000_000, active: true, owned: false };
test("monthly account responses validate prices, expiry and unique runtime identities", () => {
  assert.equal(parseAccountSale(sale).expiresAt, null);
  assert.equal(parseAccountSales({ items: [sale] })[0].runtimeId, "claude-a");
  for (const value of [{ ...sale, price_micro: -1 }, { ...sale, price_micro: 0.5 }, { ...sale, price_micro: 2 ** 53 }, { ...sale, owned: true }, { ...sale, expires_at: "invalid" }]) {
    assert.throws(() => parseAccountSale(value));
  }
  assert.throws(() => parseAccountSales({ items: [sale, { ...sale, id: "acc_b" }] }));
  assert.throws(() => parseAccountSales({ items: Array(1001).fill(sale) }));
  assert.equal(parseAccountPurchase({ id: "request", account_id: sale.id, price_micro: sale.price_micro, expires_at: "2026-12-09T00:00:00Z" }).accountId, sale.id);
  assert.equal(parseBillingOverview({ mode: "account_monthly", balance_micro: 0, blocked: false, has_customer: false, packages: [] }).mode, "account_monthly");
});

test("monthly account routes isolate tenant purchasing from operator inventory writes", () => {
  for (const [method, path, operator] of [
    ["GET", "/admin/v1/billing/accounts", false],
    ["POST", "/admin/v1/billing/accounts/acc_a/purchase", false],
    ["GET", "/operator/v1/billing/accounts", true],
    ["POST", "/operator/v1/billing/accounts", true],
    ["PUT", "/operator/v1/billing/accounts/acc_a", true],
  ] as const) {
    const segments = path.slice(1).split("/");
    const correct = operator ? resolveOperatorUpstream : resolveUpstream;
    const wrong = operator ? resolveUpstream : resolveOperatorUpstream;
    assert.deepEqual(correct(method, segments, new URLSearchParams("tenant_id=forged")), { path, search: "" });
    assert.equal(wrong(method, segments, new URLSearchParams()), null);
  }
  assert.equal(resolveUpstream("PUT", ["admin", "v1", "billing", "accounts", "acc_a"], new URLSearchParams()), null);
  assert.equal(resolveOperatorUpstream("DELETE", ["operator", "v1", "billing", "accounts", "acc_a"], new URLSearchParams()), null);
});

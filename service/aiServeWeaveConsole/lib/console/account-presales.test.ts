import test from "node:test";
import assert from "node:assert/strict";
import {
  assignableAccounts, parseAccountPresale, parseAccountPresales, parseRefundCreated, presaleAwaiting, presaleCheckoutBody, presaleRequest, refundReason,
  MAX_PRESALE_CAPACITY, MAX_PRESALES,
} from "./account-presales.ts";
import { parseAccountSale } from "./account-sales.ts";
import { parseCatalog, parseCheckoutOrder, parseOrderPage } from "./billing.ts";
import { resolveOperatorUpstream, resolveUpstream } from "./upstream-routes.ts";

const tenantView = { id: "pre_a", kind: "claude", name: "Claude presale", price_minor: 1500, currency: "USD", active: true, available: true };
const operatorView = { ...tenantView, dodo_product_id: "pdt_pre", capacity: 5, outstanding: 2, updated_at: "2026-11-11T00:00:00Z" };

test("presale responses carry a kind, a fiat price and operator-only stock", () => {
  const tenant = parseAccountPresale(tenantView);
  assert.equal(tenant.kind, "claude");
  assert.equal(tenant.capacity, null);
  assert.equal(tenant.outstanding, null);
  assert.equal(tenant.dodoProductId, "");
  const operator = parseAccountPresale(operatorView);
  assert.deepEqual([operator.capacity, operator.outstanding, operator.dodoProductId], [5, 2, "pdt_pre"]);
  // A late payment may leave more orders waiting than the capacity.
  assert.equal(parseAccountPresale({ ...operatorView, outstanding: 7 }).outstanding, 7);
  for (const value of [{ ...tenantView, kind: "grok" }, { ...tenantView, kind: "" }, { ...tenantView, price_minor: 0 }, { ...tenantView, currency: "usd" },
    { ...tenantView, available: "yes" }, { ...operatorView, capacity: undefined }, { ...operatorView, capacity: MAX_PRESALE_CAPACITY + 1 }, { ...operatorView, outstanding: -1 }]) {
    assert.throws(() => parseAccountPresale(value));
  }
  assert.equal(parseAccountPresales({ items: [tenantView] }).length, 1);
  assert.throws(() => parseAccountPresales({ items: [tenantView, tenantView] }));
  assert.throws(() => parseAccountPresales({ items: Array.from({ length: MAX_PRESALES + 1 }, (_, i) => ({ ...tenantView, id: `pre_${i}` })) }));
  assert.deepEqual(presaleCheckoutBody(tenant), { expected_price_minor: 1500, currency: "USD" });
});

test("operator presale drafts are priced in the major unit and bounded in capacity", () => {
  const draft = { kind: "codex" as const, name: " Codex presale ", price: "15", currency: "usd", productId: " pdt_pre ", capacity: "10", active: true };
  assert.deepEqual(presaleRequest(draft), { request: { kind: "codex", name: "Codex presale", price_minor: 1500, currency: "USD", dodo_product_id: "pdt_pre", capacity: 10, active: true }, problem: null });
  for (const [change, problem] of [
    [{ kind: "" }, "kind"], [{ name: " " }, "name"], [{ currency: "US" }, "currency"], [{ price: "0" }, "price"], [{ price: "1.234" }, "price"],
    [{ productId: "" }, "product"], [{ capacity: "0" }, "capacity"], [{ capacity: "10001" }, "capacity"], [{ capacity: "1.5" }, "capacity"], [{ capacity: "" }, "capacity"],
  ] as const) {
    assert.equal(presaleRequest({ ...draft, ...change }).problem, problem);
  }
});

test("presale orders wait without a package until an account is assigned", () => {
  const order = { id: "ord_p", package_id: "", presale_id: "pre_a", amount_minor: 1500, currency: "USD", credits_micro: 0, refunded_minor: 0,
    refundable_minor: 1500, status: "paid", created_at: "2026-11-11T00:00:00Z", updated_at: "2026-11-11T00:00:00Z" };
  const waiting = parseCheckoutOrder(order);
  assert.equal(waiting.presaleId, "pre_a");
  assert.equal(waiting.refundableMinor, 1500);
  assert.equal(presaleAwaiting(waiting), true);
  const assigned = parseOrderPage({ items: [{ ...order, account_id: "acc_a", account_expires_at: "2026-12-11T00:00:00Z" }] }).items[0];
  assert.equal(assigned.accountId, "acc_a");
  assert.equal(presaleAwaiting(assigned), false);
  assert.equal(presaleAwaiting({ ...waiting, status: "pending" }), false);
  assert.equal(presaleAwaiting({ ...waiting, presaleId: "" }), false);
  // Without either id the order is a credit order and needs its package.
  assert.throws(() => parseCheckoutOrder({ ...order, presale_id: undefined }));
  assert.equal(parseCheckoutOrder({ ...order, refundable_minor: undefined }).refundableMinor, 0);
});

test("assignable accounts match the kind and are free or the tenant's own", () => {
  const now = Date.parse("2026-11-11T00:00:00Z");
  const base = { id: "acc_free", node_id: "agent", runtime_id: "claude-free", kind: "claude", name: "Free", price_minor: 0, currency: "", active: false, owned: false };
  const accounts = [
    base,
    { ...base, id: "acc_codex", runtime_id: "codex-a", kind: "codex" },
    { ...base, id: "acc_legacy", runtime_id: "claude-legacy", kind: undefined },
    { ...base, id: "acc_held", runtime_id: "claude-held", tenant_id: "tnt_other", expires_at: "2026-12-01T00:00:00Z" },
    { ...base, id: "acc_expired", runtime_id: "claude-expired", tenant_id: "tnt_other", expires_at: "2026-11-01T00:00:00Z" },
    { ...base, id: "acc_mine", runtime_id: "claude-mine", tenant_id: "tnt_buyer", expires_at: "2026-12-01T00:00:00Z" },
    { ...base, id: "acc_tenant_node", node_id: "tnode.tnt_x.box", runtime_id: "claude-t" },
  ].map(parseAccountSale);
  assert.deepEqual(assignableAccounts(accounts, "claude", "tnt_buyer", now).map(account => account.id), ["acc_free", "acc_expired", "acc_mine"]);
  assert.deepEqual(assignableAccounts(accounts, "codex", "tnt_buyer", now).map(account => account.id), ["acc_codex"]);
});

test("refund reasons are required and bounded like the control plane's", () => {
  assert.equal(refundReason("  cannot deliver "), "cannot deliver");
  assert.equal(refundReason("   "), null);
  assert.equal(refundReason("x".repeat(501)), null);
  assert.equal(refundReason("界".repeat(400)), null);
  assert.deepEqual(parseRefundCreated({ id: "rfd_1", status: "processing", amount_minor: 1500 }), { id: "rfd_1", status: "processing" });
  assert.throws(() => parseRefundCreated({ status: "processing" }));
});

test("the public catalog lists presales with their kind and availability", () => {
  const catalog = parseCatalog({ packages: [], accounts: [], prices: [], presales: [{ id: "pre_a", kind: "codex", name: "Codex", price_minor: 900, currency: "EUR", available: false }] });
  assert.deepEqual(catalog.presales, [{ id: "pre_a", kind: "codex", name: "Codex", priceMinor: 900, currency: "EUR", available: false }]);
  assert.deepEqual(parseCatalog({ packages: [], accounts: [], prices: [] }).presales, []);
  assert.throws(() => parseCatalog({ packages: [], accounts: [], prices: [], presales: [{ id: "pre_a", kind: "grok", name: "x", price_minor: 1, currency: "EUR", available: true }] }));
});

test("presale routes keep tenant checkout and operator delivery on separate surfaces", () => {
  for (const [method, path, operator] of [
    ["GET", "/admin/v1/billing/presales", false],
    ["POST", "/admin/v1/billing/presales/pre_a/checkout", false],
    ["GET", "/operator/v1/billing/presales", true],
    ["POST", "/operator/v1/billing/presales", true],
    ["PUT", "/operator/v1/billing/presales/pre_a", true],
    ["POST", "/operator/v1/billing/orders/ord_a/assign", true],
  ] as const) {
    const segments = path.slice(1).split("/");
    const correct = operator ? resolveOperatorUpstream : resolveUpstream;
    const wrong = operator ? resolveUpstream : resolveOperatorUpstream;
    assert.deepEqual(correct(method, segments, new URLSearchParams("tenant_id=forged")), { path, search: "" });
    assert.equal(wrong(method, segments, new URLSearchParams()), null);
  }
  assert.deepEqual(resolveOperatorUpstream("GET", ["operator", "v1", "billing", "orders"], new URLSearchParams("fulfillment=awaiting&limit=20&x=1")),
    { path: "/operator/v1/billing/orders", search: "?limit=20&fulfillment=awaiting" });
  assert.deepEqual(resolveUpstream("GET", ["admin", "v1", "billing", "orders"], new URLSearchParams("fulfillment=awaiting")), { path: "/admin/v1/billing/orders", search: "" });
  assert.equal(resolveUpstream("POST", ["admin", "v1", "billing", "orders", "ord_a", "assign"], new URLSearchParams()), null);
  assert.equal(resolveUpstream("PUT", ["admin", "v1", "billing", "presales", "pre_a"], new URLSearchParams()), null);
  assert.equal(resolveOperatorUpstream("DELETE", ["operator", "v1", "billing", "presales", "pre_a"], new URLSearchParams()), null);
  assert.equal(resolveOperatorUpstream("GET", ["operator", "v1", "billing", "orders", "ord_a", "assign"], new URLSearchParams()), null);
});

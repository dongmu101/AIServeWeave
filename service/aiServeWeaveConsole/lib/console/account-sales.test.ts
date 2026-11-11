import test from "node:test";
import assert from "node:assert/strict";
import { accountCheckoutBody, accountKindLabel, accountPurchasable, accountRequest, parseAccountSale, parseAccountSales, MAX_ACCOUNT_PRICE_MINOR } from "./account-sales.ts";
import { parseBillingOverview, parseOrderPage } from "./billing.ts";
import { resolveUpstream, resolveOperatorUpstream } from "./upstream-routes.ts";

const sale = { id: "acc_a", node_id: "agent", runtime_id: "claude-a", name: "Claude A", price_minor: 2000, currency: "USD", active: true, owned: false };
test("monthly account responses validate fiat prices, expiry and unique runtime identities", () => {
  const parsed = parseAccountSale(sale);
  assert.equal(parsed.expiresAt, null);
  assert.equal(parsed.dodoProductId, "");
  assert.equal(accountPurchasable(parsed), true);
  assert.equal(parseAccountSales({ items: [sale] })[0].runtimeId, "claude-a");
  assert.equal(parseAccountSale({ ...sale, dodo_product_id: "pdt_1", tenant_id: "tnt_a", expires_at: "2026-12-09T00:00:00Z" }).dodoProductId, "pdt_1");
  // A legacy credit-priced offer has no fiat price and cannot be bought.
  const legacy = parseAccountSale({ ...sale, price_minor: 0, currency: "", active: false });
  assert.equal(accountPurchasable(legacy), false);
  for (const value of [{ ...sale, price_minor: -1 }, { ...sale, price_minor: 0.5 }, { ...sale, price_minor: MAX_ACCOUNT_PRICE_MINOR + 1 }, { ...sale, currency: "usd" },
    { ...sale, currency: "" }, { ...sale, price_micro: 100, price_minor: undefined }, { ...sale, owned: true }, { ...sale, expires_at: "invalid" }]) {
    assert.throws(() => parseAccountSale(value));
  }
  assert.throws(() => parseAccountSales({ items: [sale, { ...sale, id: "acc_b" }] }));
  assert.throws(() => parseAccountSales({ items: Array(1001).fill(sale) }));
  assert.deepEqual(accountCheckoutBody(parsed), { expected_price_minor: 2000, currency: "USD" });
  assert.equal(parseBillingOverview({ mode: "account_monthly", balance_micro: 0, blocked: false, has_customer: false, packages: [] }).mode, "account_monthly");
});

test("operator account drafts are priced in the currency's major unit", () => {
  const draft = { name: " Claude A ", nodeId: "agent", runtimeId: "claude-a", kind: "claude" as const, kindRequired: true, price: "19.99", currency: "usd", productId: " pdt_1 ", active: true };
  assert.deepEqual(accountRequest(draft), { request: { name: "Claude A", node_id: "agent", runtime_id: "claude-a", kind: "claude", price_minor: 1999, currency: "USD", dodo_product_id: "pdt_1", active: true }, problem: null });
  assert.equal(accountRequest({ ...draft, currency: "JPY", price: "3000" }).request?.price_minor, 3000);
  for (const [change, problem] of [
    [{ name: " " }, "name"], [{ runtimeId: "" }, "target"], [{ kind: "" }, "kind"], [{ currency: "US" }, "currency"], [{ currency: "ISO" }, "currency"], [{ price: "0" }, "price"],
    [{ price: "1.234" }, "price"], [{ currency: "JPY", price: "12.5" }, "price"], [{ price: "1000001" }, "price"], [{ productId: "" }, "product"],
  ] as const) {
    assert.equal(accountRequest({ ...draft, ...change }).problem, problem);
  }
  // Archived inventory may stay unpriced for presale assignment; listing needs a price.
  const unpriced = { ...draft, price: " ", productId: "", active: false };
  assert.deepEqual(accountRequest(unpriced).request, { name: "Claude A", node_id: "agent", runtime_id: "claude-a", kind: "claude", price_minor: 0, currency: "", dodo_product_id: "", active: false });
  assert.equal(accountRequest({ ...unpriced, active: true }).problem, "price");
  assert.equal(accountRequest({ ...unpriced, productId: "pdt_1" }).problem, "price");
  // An edit may leave a legacy row's kind empty, which keeps it.
  assert.equal(accountRequest({ ...draft, kind: "", kindRequired: false }).request?.kind, "");
});

test("account kinds are claude or codex, and legacy rows have none", () => {
  assert.equal(parseAccountSale(sale).kind, "");
  assert.equal(parseAccountSale({ ...sale, kind: "codex" }).kind, "codex");
  assert.equal(parseAccountSale({ ...sale, kind: "" }).kind, "");
  assert.throws(() => parseAccountSale({ ...sale, kind: "ollama" }));
  assert.equal(accountKindLabel("claude"), "Claude Code");
  assert.equal(accountKindLabel("codex"), "Codex");
});

test("account orders carry the account and its granted expiry instead of a package", () => {
  const order = { id: "ord_a", amount_minor: 2000, currency: "USD", credits_micro: 0, refunded_minor: 0, status: "paid", created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-01T00:00:00Z" };
  const [granted, pending] = parseOrderPage({ items: [
    { ...order, package_id: "", account_id: "acc_a", account_expires_at: "2026-12-01T00:00:00Z" },
    { ...order, package_id: "", account_id: "acc_a", status: "pending" },
  ] }).items;
  assert.equal(granted.accountId, "acc_a");
  assert.equal(granted.accountExpiresAt, "2026-12-01T00:00:00Z");
  assert.equal(pending.accountExpiresAt, null);
  assert.throws(() => parseOrderPage({ items: [{ ...order, package_id: "" }] }));
  assert.equal(parseOrderPage({ items: [{ ...order, package_id: "pkg_a" }] }).items[0].accountId, "");
});

test("monthly account routes isolate tenant checkout from operator inventory writes", () => {
  for (const [method, path, operator] of [
    ["GET", "/admin/v1/billing/accounts", false],
    ["POST", "/admin/v1/billing/accounts/acc_a/checkout", false],
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
  assert.equal(resolveUpstream("POST", ["admin", "v1", "billing", "accounts", "acc_a", "purchase"], new URLSearchParams()), null);
  assert.equal(resolveUpstream("PUT", ["admin", "v1", "billing", "accounts", "acc_a"], new URLSearchParams()), null);
  assert.equal(resolveOperatorUpstream("DELETE", ["operator", "v1", "billing", "accounts", "acc_a"], new URLSearchParams()), null);
});

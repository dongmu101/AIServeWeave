import { test } from "node:test";
import assert from "node:assert/strict";

import {
  abortableSleep,
  adjustRequest,
  checkoutBody,
  currencyDigits,
  customAmountProblem,
  customCredits,
  emptyPackageDraft,
  formatCredits,
  formatMoney,
  formatScaled,
  formatSignedCredits,
  isPaymentReturn,
  modelPriceRequest,
  packageDraft,
  packageRequest,
  parseBillingOverview,
  parseBillingTenants,
  parseCheckout,
  parseCreditPackage,
  parseCreditPackages,
  parseCreditsInput,
  parseLedgerPage,
  parseModelPrices,
  parseMoneyInput,
  parseOrderPage,
  parsePortalLink,
  pollUntilSettled,
  type CreditPackage,
  type PackageDraft,
} from "./billing.ts";
import { ApiError } from "./errors.ts";

const at = "2026-10-29T00:00:00Z";
const fixedWire = { id: "pkg_fixed", name: "Starter", kind: "fixed", credits_micro: 10_000_000, price_minor: 999, currency: "USD", active: true, sort_order: 1, created_at: at, updated_at: at };
const customWire = { id: "pkg_custom", name: "Any amount", kind: "custom", currency: "USD", credits_per_minor_unit_micro: 10_000, min_amount_minor: 500, max_amount_minor: 100_000, active: true, sort_order: 2, created_at: at, updated_at: at };

/** rejects asserts that a parser refuses a value as a contract violation.
 * rejects 断言解析器以契约违例拒绝某个值。 */
function rejects(name: string, run: () => unknown) {
  assert.throws(run, (error: unknown) => error instanceof ApiError && error.kind === "contract", name);
}

test("billing responses parse in the shapes the control plane sends", () => {
  const overview = parseBillingOverview({ mode: "prepaid", balance_micro: -1_500_000, blocked: true, has_customer: false, packages: [fixedWire, customWire] });
  assert.equal(overview.mode, "prepaid");
  assert.equal(overview.balanceMicro, -1_500_000);
  assert.equal(overview.blocked, true);
  assert.equal(overview.packages.length, 2);
  assert.equal(overview.packages[0]?.dodoProductId, "", "the tenant view carries no product id");
  assert.equal(overview.packages[1]?.creditsMicro, 0, "omitted custom fields read as zero");
  assert.equal(overview.packages[1]?.creditsPerMinorUnitMicro, 10_000);
  assert.deepEqual(parseBillingOverview({ mode: "exempt", balance_micro: 0, blocked: false, has_customer: true, packages: null }).packages, []);

  assert.equal(parseCreditPackages({ items: [{ ...fixedWire, dodo_product_id: "pdt_1" }] })[0]?.dodoProductId, "pdt_1");
  assert.deepEqual(parseCreditPackages({ items: [] }), []);

  const ledger = parseLedgerPage({ items: [{ id: "led_1", kind: "usage", amount_micro: -42, balance_after_micro: 100, ref: "", created_at: at }], next_cursor: "c1" });
  assert.equal(ledger.items[0]?.amountMicro, -42);
  assert.equal(ledger.items[0]?.tenantId, "");
  assert.equal(ledger.nextCursor, "c1");
  const operatorLedger = parseLedgerPage({ items: [{ id: "led_2", tenant_id: "tnt_1", kind: "a_future_kind", amount_micro: 1, balance_after_micro: 1, ref: "r", created_at: at }] });
  assert.equal(operatorLedger.items[0]?.tenantId, "tnt_1");
  assert.equal(operatorLedger.items[0]?.kind, "a_future_kind", "an unknown kind is shown rather than breaking the page");
  assert.equal(operatorLedger.nextCursor, null);

  const orders = parseOrderPage({ items: [{ id: "ord_1", package_id: "pkg", amount_minor: 999, currency: "USD", credits_micro: 1, refunded_minor: 0, status: "pending", created_at: at, updated_at: at }], next_cursor: "" });
  assert.equal(orders.items[0]?.paymentId, "");
  assert.equal(orders.nextCursor, null);

  const prices = parseModelPrices({ items: [{ model: "*", input_micro_per_mtok: 1_000_000, output_micro_per_mtok: 2_000_000, updated_by: "op_1", updated_at: at }] });
  assert.equal(prices[0]?.model, "*");
  const tenants = parseBillingTenants({ items: [{ id: "tnt_1", name: "", status: "active", mode: "exempt", balance_micro: 0, blocked: false, created_at: at }] });
  assert.equal(tenants.items[0]?.mode, "exempt");

  assert.deepEqual(parseCheckout({ order_id: "ord_1", checkout_url: "https://checkout.example.test/s/1" }), { orderId: "ord_1", checkoutUrl: "https://checkout.example.test/s/1" });
  assert.equal(parsePortalLink({ url: "https://portal.example.test/x" }), "https://portal.example.test/x");
});

test("billing parsers reject what the contract does not allow", () => {
  const cases: { name: string; run: () => unknown }[] = [
    { name: "unknown mode", run: () => parseBillingOverview({ mode: "postpaid", balance_micro: 0, blocked: false, has_customer: false, packages: [] }) },
    { name: "fractional balance", run: () => parseBillingOverview({ mode: "prepaid", balance_micro: 1.5, blocked: false, has_customer: false, packages: [] }) },
    { name: "unsafe balance", run: () => parseBillingOverview({ mode: "prepaid", balance_micro: 2 ** 60, blocked: false, has_customer: false, packages: [] }) },
    { name: "missing blocked", run: () => parseBillingOverview({ mode: "prepaid", balance_micro: 0, has_customer: false, packages: [] }) },
    { name: "packages not an array", run: () => parseBillingOverview({ mode: "prepaid", balance_micro: 0, blocked: false, has_customer: false, packages: {} }) },
    { name: "unknown package kind", run: () => parseCreditPackage({ ...fixedWire, kind: "subscription" }) },
    { name: "negative price", run: () => parseCreditPackage({ ...fixedWire, price_minor: -1 }) },
    { name: "missing currency", run: () => parseCreditPackage({ ...fixedWire, currency: undefined }) },
    { name: "bad timestamp", run: () => parseCreditPackage({ ...fixedWire, created_at: "yesterday" }) },
    { name: "not an object", run: () => parseCreditPackage([]) },
    { name: "checkout javascript link", run: () => parseCheckout({ order_id: "o", checkout_url: "javascript:alert(1)" }) },
    { name: "checkout plain http", run: () => parseCheckout({ order_id: "o", checkout_url: "http://checkout.example.test" }) },
    { name: "checkout relative link", run: () => parseCheckout({ order_id: "o", checkout_url: "/console" }) },
    { name: "portal without url", run: () => parsePortalLink({}) },
    { name: "ledger cursor not a string", run: () => parseLedgerPage({ items: [], next_cursor: 3 }) },
    { name: "ledger missing kind", run: () => parseLedgerPage({ items: [{ id: "l", amount_micro: 1, balance_after_micro: 1, ref: "", created_at: at }] }) },
    { name: "order negative refund", run: () => parseOrderPage({ items: [{ id: "o", package_id: "p", amount_minor: 1, currency: "USD", credits_micro: 1, refunded_minor: -1, status: "paid", created_at: at, updated_at: at }] }) },
    { name: "price negative", run: () => parseModelPrices({ items: [{ model: "m", input_micro_per_mtok: -1, output_micro_per_mtok: 0, updated_by: "", updated_at: at }] }) },
    { name: "tenant unknown mode", run: () => parseBillingTenants({ items: [{ id: "t", name: "", status: "active", mode: "free", balance_micro: 0, blocked: false, created_at: at }] }) },
  ];
  for (const item of cases) {
    rejects(item.name, item.run);
  }
});

test("money and credits convert between minor units and display exactly", () => {
  assert.equal(currencyDigits("USD"), 2);
  assert.equal(currencyDigits("JPY"), 0);
  assert.equal(currencyDigits("BHD"), 3);
  assert.equal(currencyDigits("not-a-code"), 2);

  assert.equal(formatMoney(999, "USD", "en-US"), "$9.99");
  assert.equal(formatMoney(1000, "JPY", "en-US"), "¥1,000");
  assert.match(formatMoney(1234, "BHD", "en-US"), /^BHD\s1\.234$/u);
  assert.match(formatMoney(5, "??", "en-US"), /0\.05 \?\?/);

  assert.equal(formatCredits(1_500_000, "en-US"), "1.5");
  assert.equal(formatCredits(1, "en-US"), "0.000001", "a single micro-credit is still visible");
  assert.equal(formatCredits(0, "en-US"), "0");
  assert.equal(formatSignedCredits(2_000_000, "en-US"), "+2");
  assert.equal(formatSignedCredits(-250_000, "en-US"), "-0.25");
  assert.equal(formatSignedCredits(0, "en-US"), "0");

  const money: { raw: string; currency: string; want: number | null }[] = [
    { raw: "12.5", currency: "USD", want: 1250 },
    { raw: "12,50", currency: "USD", want: 1250 },
    { raw: " 7 ", currency: "USD", want: 700 },
    { raw: "0.001", currency: "USD", want: null },
    { raw: "1000", currency: "JPY", want: 1000 },
    { raw: "12.5", currency: "JPY", want: null },
    { raw: "-1", currency: "USD", want: null },
    { raw: "", currency: "USD", want: null },
    { raw: "1e3", currency: "USD", want: null },
    { raw: "99999999999999999", currency: "USD", want: null },
  ];
  for (const item of money) {
    assert.equal(parseMoneyInput(item.raw, item.currency), item.want, `${item.raw} ${item.currency}`);
  }

  const credits: { raw: string; negative: boolean; want: number | null }[] = [
    { raw: "0.1", negative: false, want: 100_000 },
    { raw: "1.000001", negative: false, want: 1_000_001 },
    { raw: "1.0000001", negative: false, want: null },
    { raw: "-2.5", negative: true, want: -2_500_000 },
    { raw: "-2.5", negative: false, want: null },
    { raw: "+3", negative: false, want: 3_000_000 },
    { raw: "abc", negative: true, want: null },
  ];
  for (const item of credits) {
    assert.equal(parseCreditsInput(item.raw, { allowNegative: item.negative }), item.want, item.raw);
  }

  for (const [value, digits, want] of [[1_500_000, 6, "1.5"], [1, 6, "0.000001"], [-2_500_000, 6, "-2.5"], [1250, 2, "12.5"], [1000, 0, "1000"], [0, 2, "0"]] as const) {
    assert.equal(formatScaled(value, digits), want, `${value}/${digits}`);
    assert.equal(parseCreditsInput(formatScaled(value, 6), { allowNegative: true }) !== null, true);
  }
});

test("a custom amount is checked against the package range before checkout", () => {
  const pkg = parseCreditPackage(customWire);
  const cases: { raw: string; amount: number | null; problem: string | null }[] = [
    { raw: "5", amount: 500, problem: null },
    { raw: "1000", amount: 100_000, problem: null },
    { raw: "4.99", amount: null, problem: "below_min" },
    { raw: "1000.01", amount: null, problem: "above_max" },
    { raw: "0", amount: null, problem: "invalid" },
    { raw: "five", amount: null, problem: "invalid" },
    { raw: "", amount: null, problem: "invalid" },
  ];
  for (const item of cases) {
    const result = customAmountProblem(pkg, item.raw);
    assert.equal(result.amountMinor, item.amount, item.raw);
    assert.equal(result.problem, item.problem, item.raw);
  }
  assert.equal(customCredits(pkg, 500), 5_000_000);
  assert.equal(customCredits({ creditsPerMinorUnitMicro: 2 ** 40 }, 2 ** 20), null);

  assert.deepEqual(checkoutBody(pkg, 500), { package_id: "pkg_custom", amount_minor: 500 });
  assert.deepEqual(checkoutBody(parseCreditPackage(fixedWire), 500), { package_id: "pkg_fixed" }, "a fixed package never sends an amount");
});

test("package drafts validate per kind and zero the other kind's fields", () => {
  const fixed: PackageDraft = { ...emptyPackageDraft(), name: " Starter ", dodoProductId: "pdt_1", credits: "10", price: "9.99" };
  const custom: PackageDraft = { ...emptyPackageDraft(), name: "Any", kind: "custom", dodoProductId: "pdt_2", creditsPerMinorUnit: "0.01", minAmount: "5", maxAmount: "1000", sortOrder: "-1" };
  assert.deepEqual(packageRequest(fixed), {
    problem: null,
    request: { name: "Starter", kind: "fixed", dodo_product_id: "pdt_1", currency: "USD", active: true, credits_micro: 10_000_000, price_minor: 999, credits_per_minor_unit_micro: 0, min_amount_minor: 0, max_amount_minor: 0, sort_order: 0 },
  });
  assert.deepEqual(packageRequest(custom), {
    problem: null,
    request: { name: "Any", kind: "custom", dodo_product_id: "pdt_2", currency: "USD", active: true, credits_micro: 0, price_minor: 0, credits_per_minor_unit_micro: 10_000, min_amount_minor: 500, max_amount_minor: 100_000, sort_order: -1 },
  });

  const cases: { name: string; draft: PackageDraft; problem: string }[] = [
    { name: "blank name", draft: { ...fixed, name: "  " }, problem: "name" },
    { name: "long name", draft: { ...fixed, name: "n".repeat(129) }, problem: "name" },
    { name: "blank product", draft: { ...fixed, dodoProductId: "" }, problem: "product" },
    { name: "long product", draft: { ...fixed, dodoProductId: "p".repeat(65) }, problem: "product" },
    { name: "lowercase currency", draft: { ...fixed, currency: "usd" }, problem: "currency" },
    { name: "zero credits", draft: { ...fixed, credits: "0" }, problem: "credits" },
    { name: "price with too many digits", draft: { ...fixed, price: "9.999" }, problem: "price" },
    { name: "JPY price with a fraction", draft: { ...fixed, currency: "JPY", price: "100.5" }, problem: "price" },
    { name: "fractional sort order", draft: { ...fixed, sortOrder: "1.5" }, problem: "sort_order" },
    { name: "zero rate", draft: { ...custom, creditsPerMinorUnit: "0" }, problem: "rate" },
    { name: "max below min", draft: { ...custom, maxAmount: "4" }, problem: "range" },
    { name: "zero min", draft: { ...custom, minAmount: "0" }, problem: "range" },
    { name: "overflowing max", draft: { ...custom, creditsPerMinorUnit: "1000000", maxAmount: "99999999999" }, problem: "range" },
  ];
  for (const item of cases) {
    assert.equal(packageRequest(item.draft).problem, item.problem, item.name);
  }

  const stored: CreditPackage = parseCreditPackage({ ...customWire, dodo_product_id: "pdt_2", currency: "JPY", min_amount_minor: 500, max_amount_minor: 10_000 });
  const round = packageDraft(stored);
  assert.equal(round.minAmount, "500", "JPY amounts round-trip without a decimal point");
  assert.equal(round.creditsPerMinorUnit, "0.01");
  assert.equal(round.credits, "");
  assert.equal(packageRequest(round).problem, null);
});

test("model prices and adjustments convert credits to micro-credits", () => {
  assert.deepEqual(modelPriceRequest({ model: " * ", input: "1.5", output: "0" }), { problem: null, request: { model: "*", input_micro_per_mtok: 1_500_000, output_micro_per_mtok: 0 } });
  assert.deepEqual(modelPriceRequest({ model: "org/model:tag", input: "1000000", output: "1000000" }).problem, null);
  const prices: { name: string; draft: { model: string; input: string; output: string }; problem: string }[] = [
    { name: "blank alias", draft: { model: "", input: "1", output: "1" }, problem: "model" },
    { name: "long alias", draft: { model: "m".repeat(129), input: "1", output: "1" }, problem: "model" },
    { name: "above the cap", draft: { model: "m", input: "1000000.000001", output: "1" }, problem: "price" },
    { name: "negative", draft: { model: "m", input: "-1", output: "1" }, problem: "price" },
    { name: "blank output", draft: { model: "m", input: "1", output: "" }, problem: "price" },
  ];
  for (const item of prices) {
    assert.equal(modelPriceRequest(item.draft).problem, item.problem, item.name);
  }

  assert.deepEqual(adjustRequest({ amount: "-12.5", reason: " chargeback " }), { problem: null, request: { amount_micro: -12_500_000, reason: "chargeback" } });
  const adjustments: { name: string; draft: { amount: string; reason: string }; problem: string }[] = [
    { name: "zero", draft: { amount: "0", reason: "r" }, problem: "amount" },
    { name: "not a number", draft: { amount: "ten", reason: "r" }, problem: "amount" },
    { name: "blank reason", draft: { amount: "1", reason: "   " }, problem: "reason" },
    { name: "long reason", draft: { amount: "1", reason: "r".repeat(129) }, problem: "reason" },
    { name: "multi-byte reason over 128 bytes", draft: { amount: "1", reason: "退".repeat(43) }, problem: "reason" },
  ];
  for (const item of adjustments) {
    assert.equal(adjustRequest(item.draft).problem, item.problem, item.name);
  }
});

test("a payment return is recognised from its parameters, not trusted for its value", () => {
  assert.equal(isPaymentReturn({}), false);
  assert.equal(isPaymentReturn({ status: "succeeded" }), true);
  assert.equal(isPaymentReturn({ status: "failed", payment_id: "pay_1" }), true);
  assert.equal(isPaymentReturn({ payment_id: "pay_1" }), true);
  assert.equal(isPaymentReturn({ tab: "orders" }), false);
});

test("polling after a payment return is bounded and stops on confirmation", async () => {
  /** harness runs pollUntilSettled with a scripted read and a recording sleep.
   * harness 用脚本化的读取与记录型 sleep 运行 pollUntilSettled。 */
  async function harness(script: (attempt: number) => number | Error, options: { abortAfter?: number } = {}) {
    const controller = new AbortController();
    const waits: number[] = [];
    const seen: number[] = [];
    let reads = 0;
    const outcome = await pollUntilSettled<number>({
      signal: controller.signal,
      intervalMs: 3000,
      maxAttempts: 10,
      sleep: async (ms) => { waits.push(ms); },
      read: async () => {
        reads += 1;
        if (options.abortAfter !== undefined && reads > options.abortAfter) controller.abort();
        const next = script(reads);
        if (next instanceof Error) throw next;
        return next;
      },
      onResult: (value) => seen.push(value),
      settled: (value) => value >= 3,
    });
    return { outcome, waits, seen, reads };
  }

  const settled = await harness((attempt) => attempt);
  assert.equal(settled.outcome, "settled");
  assert.equal(settled.reads, 3);
  assert.deepEqual(settled.waits, [3000, 3000], "no wait before the first read");
  assert.deepEqual(settled.seen, [1, 2, 3]);

  const exhausted = await harness(() => 0);
  assert.equal(exhausted.outcome, "exhausted");
  assert.equal(exhausted.reads, 10, "the poll never exceeds its bound");

  const failing = await harness((attempt) => (attempt < 5 ? new ApiError("network") : 3));
  assert.equal(failing.outcome, "settled", "a failed read counts as an attempt but does not end the poll");
  assert.equal(failing.reads, 5);
  assert.deepEqual(failing.seen, [3]);

  const allFailing = await harness(() => new ApiError("server"));
  assert.equal(allFailing.outcome, "exhausted");
  assert.equal(allFailing.reads, 10);

  const aborted = await harness(() => 0, { abortAfter: 2 });
  assert.equal(aborted.outcome, "aborted");
  assert.equal(aborted.reads, 3);
  assert.deepEqual(aborted.seen, [0, 0], "a read finishing after cancellation is not reported");

  let attempts = 0;
  const gaveUp = await pollUntilSettled({
    signal: new AbortController().signal,
    sleep: async () => {},
    read: async () => { attempts += 1; throw new ApiError(attempts < 2 ? "network" : "not_found", attempts < 2 ? 0 : 404); },
    onResult: () => {},
    settled: () => true,
    giveUp: (error) => error instanceof ApiError && error.kind === "not_found",
  });
  assert.equal(gaveUp, "exhausted");
  assert.equal(attempts, 2, "a transient failure is retried, a 404 ends the poll");

  const preAborted = new AbortController();
  preAborted.abort();
  let calls = 0;
  const none = await pollUntilSettled({ signal: preAborted.signal, sleep: async () => {}, read: async () => { calls += 1; return 1; }, onResult: () => {}, settled: () => true });
  assert.equal(none, "aborted");
  assert.equal(calls, 0);
});

test("the browser sleep resolves on time or as soon as it is canceled", async () => {
  const done = new AbortController();
  await abortableSleep(0, done.signal);
  const early = new AbortController();
  early.abort();
  await abortableSleep(60_000, early.signal);
  const later = new AbortController();
  const pending = abortableSleep(60_000, later.signal);
  later.abort();
  await pending;
});

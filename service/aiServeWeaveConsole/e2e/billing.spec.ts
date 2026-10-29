import { test, expect, BILLING_OVERVIEW } from "./fixtures.ts";

const CHECKOUT_URL = "https://checkout.example.invalid/session/p10";

test("a custom top-up converts the amount live and sends only the package and amount", async ({ page }) => {
  const writes: unknown[] = [];
  await page.route("**/api/admin/admin/v1/billing/checkout", async route => {
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: { order_id: "ord_new", checkout_url: CHECKOUT_URL } });
  });
  await page.route(`${CHECKOUT_URL}**`, route => route.fulfill({ contentType: "text/html", body: "<title>checkout</title>" }));
  await page.goto("/console/billing");
  const amount = page.getByLabel("金额（USD）", { exact: true });
  const buy = page.getByRole("button", { name: "购买", exact: true });
  await amount.fill("4.99");
  await expect(page.getByText("金额不能低于 US$5.00。", { exact: true })).toBeVisible();
  await expect(buy.nth(1)).toBeDisabled();
  await amount.fill("12.5");
  await expect(page.getByText("可获得 12.5 点", { exact: true })).toBeVisible();
  await buy.nth(1).click();
  await page.waitForURL(CHECKOUT_URL);
  expect(writes).toEqual([{ package_id: "pkg_custom", amount_minor: 1250 }]);
});

test("a fixed package never sends an amount, and a provider outage shows fixed copy", async ({ page }) => {
  const writes: unknown[] = [];
  await page.route("**/api/admin/admin/v1/billing/checkout", async route => {
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 503, json: { error: "dodo: upstream said something internal" } });
  });
  await page.goto("/console/billing");
  await page.getByRole("button", { name: "购买", exact: true }).first().click();
  await expect(page.locator("main").getByRole("alert")).toHaveText("支付服务暂时不可用，请稍后再试。");
  await expect(page.locator("main")).not.toContainText("upstream said");
  expect(writes).toEqual([{ package_id: "pkg_fixed" }]);
});

test("a payment return waits for the server and never trusts the query", async ({ page }) => {
  let reads = 0;
  await page.route(/\/api\/admin\/admin\/v1\/billing\/orders(\?|$)/, async route => {
    // Only the poll asks for a single order; the orders table asks for a page.
    // 只有轮询只请求一笔订单；订单表请求的是一整页。
    const poll = new URL(route.request().url()).searchParams.get("limit") === "1";
    if (poll) reads++;
    const status = poll && reads < 2 ? "pending" : "paid";
    await route.fulfill({ json: { items: [{ id: "ord_p10", package_id: "pkg_fixed", amount_minor: 999, currency: "USD", credits_micro: 10_000_000, refunded_minor: 0, status, created_at: "2026-09-12T00:00:00Z", updated_at: "2026-09-12T00:00:00Z" }] } });
  });
  await page.goto("/console/billing?status=succeeded&payment_id=pay_forged");
  await expect(page.getByRole("status").filter({ hasText: "支付确认中" })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "服务端已确认" })).toBeVisible({ timeout: 10_000 });
  expect(reads).toBeGreaterThanOrEqual(2);
});

test("billing that the control plane has not enabled renders as an empty state", async ({ page }) => {
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ status: 404, json: { error: "not_found" } }));
  await page.goto("/console/billing");
  await expect(page.getByText("计费未开启", { exact: true })).toBeVisible();
  await expect(page.locator("main").getByRole("alert")).toHaveCount(0);
});

test("an exhausted prepaid balance is announced with the 402 consequence", async ({ page }) => {
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: { ...BILLING_OVERVIEW, balance_micro: 0, blocked: true } }));
  await page.goto("/console/billing");
  await expect(page.locator("main").getByRole("alert")).toContainText("HTTP 402");
});

test("operator billing edits send micro-credit and minor-unit integers", async ({ page }) => {
  const writes: { method: string; path: string; body: unknown }[] = [];
  await page.route("**/api/operator/operator/v1/billing/**", async route => {
    const request = route.request();
    if (request.method() === "GET") return route.fallback();
    const url = new URL(request.url());
    writes.push({ method: request.method(), path: url.pathname + url.search, body: request.postDataJSON() });
    if (url.pathname.endsWith("/model-prices")) await route.fulfill({ json: { model: "fixture-model", input_micro_per_mtok: 1_500_000, output_micro_per_mtok: 0, updated_by: "op_p10", updated_at: "2026-09-12T00:00:00Z" } });
    else if (url.pathname.endsWith("/adjust")) await route.fulfill({ json: { id: "led_adj", tenant_id: "tnt_p10", kind: "adjust", amount_micro: -2_500_000, balance_after_micro: 0, ref: "chargeback", created_at: "2026-09-12T00:00:00Z" } });
    else await route.fulfill({ status: 201, json: { id: "pkg_new", name: "JPY pack", kind: "fixed", credits_micro: 1_000_000, price_minor: 1000, currency: "JPY", active: true, sort_order: 0, dodo_product_id: "pdt_jpy", created_at: "2026-09-12T00:00:00Z", updated_at: "2026-09-12T00:00:00Z" } });
  });
  await page.goto("/operator/billing");

  await page.getByRole("button", { name: "新建充值包", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("名称", { exact: true }).fill("JPY pack");
  await dialog.getByLabel("货币（ISO 4217，如 USD）", { exact: true }).fill("jpy");
  await dialog.getByLabel("Dodo 产品 ID", { exact: true }).fill("pdt_jpy");
  await dialog.getByLabel("点数", { exact: true }).fill("1");
  await dialog.getByLabel("价格（主货币单位）", { exact: true }).fill("10.5");
  await dialog.getByRole("button", { name: "创建", exact: true }).click();
  await expect(dialog.getByRole("alert")).toHaveText("价格须大于 0，小数位不超过该货币的最小单位。");
  await dialog.getByLabel("价格（主货币单位）", { exact: true }).fill("1000");
  await dialog.getByRole("button", { name: "创建", exact: true }).click();
  await expect(dialog).toHaveCount(0);

  await page.getByRole("tab", { name: "模型单价", exact: true }).click();
  await page.getByRole("button", { name: "设置单价", exact: true }).click();
  await page.getByRole("dialog").getByLabel("模型别名", { exact: true }).fill("fixture-model");
  await page.getByRole("dialog").getByLabel("输入单价（点/百万 token）", { exact: true }).fill("1.5");
  await page.getByRole("dialog").getByLabel("输出单价（点/百万 token）", { exact: true }).fill("0");
  await page.getByRole("dialog").getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  await page.getByRole("tab", { name: "商户", exact: true }).click();
  await page.getByRole("button", { name: "调整余额", exact: true }).click();
  await page.getByRole("dialog").getByLabel("调整点数（负数为扣减）", { exact: true }).fill("-2.5");
  await page.getByRole("dialog").getByLabel("原因（记入账目，最多 128 个字符）", { exact: true }).fill("chargeback");
  await page.getByRole("dialog").getByRole("button", { name: "调整余额", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  expect(writes).toEqual([
    { method: "POST", path: "/api/operator/operator/v1/billing/packages", body: { name: "JPY pack", kind: "fixed", dodo_product_id: "pdt_jpy", currency: "JPY", active: true, credits_micro: 1_000_000, price_minor: 1000, credits_per_minor_unit_micro: 0, min_amount_minor: 0, max_amount_minor: 0, sort_order: 0 } },
    { method: "PUT", path: "/api/operator/operator/v1/billing/model-prices", body: { model: "fixture-model", input_micro_per_mtok: 1_500_000, output_micro_per_mtok: 0 } },
    { method: "POST", path: "/api/operator/operator/v1/billing/tenants/tnt_p10/adjust", body: { amount_micro: -2_500_000, reason: "chargeback" } },
  ]);
});

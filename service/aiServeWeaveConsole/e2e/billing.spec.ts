import { test, expect, BILLING_OVERVIEW } from "./fixtures.ts";
import { deriveSessionKey, seal, SESSION_COOKIE } from "../lib/console/session-payload.ts";

const CHECKOUT_URL = "https://checkout.example.invalid/session/p10";

test("deleting an order requires confirmation, keeps failures open, and persists after reload", async ({ page }) => {
  let deletes = 0;
  let removed = false;
  const at = "2026-11-13T00:00:00Z";
  const order = { id: "ord_delete", package_id: "pkg_fixed", amount_minor: 999, currency: "USD", credits_micro: 10_000_000, refunded_minor: 0, status: "paid", created_at: at, updated_at: at };
  await page.route(/\/api\/admin\/admin\/v1\/billing\/orders(\?|$)/, route => route.fulfill({ json: { items: removed ? [] : [order], next_cursor: "" } }));
  await page.route("**/api/admin/admin/v1/billing/orders/ord_delete", async route => {
    expect(route.request().method()).toBe("DELETE");
    deletes++;
    if (deletes === 1) await route.fulfill({ status: 503, json: { error: "unavailable" } });
    else {
      removed = true;
      await route.fulfill({ status: 204 });
    }
  });
  await page.goto("/console/billing?tab=orders");
  await page.getByRole("button", { name: "删除", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("ord_delete");
  await expect(dialog).toContainText("删除不会取消付款");
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  expect(deletes).toBe(0);
  await expect(page.getByText("ord_delete", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "删除", exact: true }).click();
  await dialog.getByRole("button", { name: "删除", exact: true }).click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  expect(deletes).toBe(1);
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "删除", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText("ord_delete", { exact: true })).toHaveCount(0);
  expect(deletes).toBe(2);
  await page.reload();
  await expect(page.getByText("ord_delete", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "删除", exact: true })).toHaveCount(0);
});

test("deleting the last order on a later page returns to the previous page on mobile", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  let removed = false;
  const at = "2026-11-13T00:00:00Z";
  const base = { package_id: "pkg_fixed", amount_minor: 999, currency: "USD", credits_micro: 10_000_000, refunded_minor: 0, status: "pending", created_at: at, updated_at: at };
  await page.route(/\/api\/admin\/admin\/v1\/billing\/orders(\?|$)/, route => {
    const later = new URL(route.request().url()).searchParams.has("cursor");
    return route.fulfill({ json: { items: later ? removed ? [] : [{ ...base, id: "ord_last" }] : [{ ...base, id: "ord_first" }], next_cursor: !later && !removed ? "later" : "" } });
  });
  await page.route("**/api/admin/admin/v1/billing/orders/ord_last", async route => {
    expect(route.request().method()).toBe("DELETE");
    removed = true;
    await route.fulfill({ status: 204 });
  });
  await page.goto("/console/billing?tab=orders");
  await page.getByRole("button", { name: "下一页", exact: true }).click();
  await expect(page.getByText("ord_last", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "删除", exact: true }).click();
  const dialog = page.getByRole("dialog");
  const box = await dialog.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(390);
  await page.screenshot({ path: "/tmp/aisw-order-delete-mobile.png", animations: "disabled" });
  await dialog.getByRole("button", { name: "删除", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText("ord_first", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "上一页", exact: true })).toBeDisabled();
});

test("members can view orders without a delete control", async ({ page }) => {
  await page.context().addCookies([{
    name: SESSION_COOKIE,
    value: seal({ token: "synthetic-member-token", expiresAt: new Date(Date.now() + 3_600_000).toISOString(), user: { id: "usr_member", tenantId: "tnt_p10", email: "member@example.invalid", name: "Member", role: "member" } }, deriveSessionKey("p10-browser-acceptance-only-not-a-deployment-secret")),
    url: "http://127.0.0.1:3100", httpOnly: true, sameSite: "Lax",
  }]);
  await page.goto("/console/billing?tab=orders");
  await expect(page.getByText("ord_p10", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "删除", exact: true })).toHaveCount(0);
});

test("account editor selects actual Claude Code and Codex accounts reported by an Agent", async ({ page }) => {
  const at = "2026-11-09T00:00:00Z";
  await page.route("**/api/operator/operator/v1/nodes", route => route.fulfill({ json: {
    collected_at: at, partial: false, replicas: [], nodes: [{ node_id: "agent-one", live: true, maintenance: false, observed_at: at, labels: {}, runtimes: [
      { id: "claude-a", kind: "claude", state: "healthy", version: "test", models: [] },
      { id: "codex-a", kind: "codex", state: "healthy", version: "test", models: [] },
      { id: "ollama-a", kind: "ollama", state: "healthy", version: "test", models: [] },
    ] }],
  } }));
  await page.goto("/operator/billing");
  await page.getByRole("tab", { name: "账号现货", exact: true }).click();
  await page.getByRole("button", { name: "添加账号", exact: true }).click();
  await page.getByRole("combobox", { name: "选择 Agent", exact: true }).selectOption("agent-one");
  const accounts = page.getByRole("combobox", { name: "选择 Claude Code / Codex 账号", exact: true });
  await expect(accounts.getByRole("option", { name: "Claude Code · claude-a", exact: true })).toHaveCount(1);
  await expect(accounts.getByRole("option", { name: "Codex · codex-a", exact: true })).toHaveCount(1);
  await expect(accounts.getByRole("option").filter({ hasText: "ollama" })).toHaveCount(0);
  await accounts.selectOption("codex-a");
  await expect(page.getByLabel("账号名称", { exact: true })).toHaveValue("Codex · codex-a");
  await accounts.selectOption("claude-a");
  await expect(page.getByLabel("账号名称", { exact: true })).toHaveValue("Claude Code · claude-a");
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(page.getByRole("combobox", { name: "选择 Agent", exact: true })).toHaveValue("");
  await expect(accounts).toBeDisabled();
});

test("monthly accounts open a fiat checkout at the confirmed price and never retry it", async ({ page }) => {
  const account = { id: "acc_monthly", node_id: "agent-a", runtime_id: "claude-a", name: "Dedicated Claude", price_minor: 1999, currency: "USD", active: true, owned: false };
  const writes: { expected_price_minor: number; currency: string }[] = [];
  await page.route("**/api/admin/admin/v1/billing/accounts", route => route.fulfill({ json: { items: [account, { ...account, id: "acc_legacy", runtime_id: "codex-a", name: "Legacy Codex", price_minor: 0, currency: "", active: false }] } }));
  await page.route("**/api/admin/admin/v1/billing/accounts/acc_monthly/checkout", async route => {
    writes.push(route.request().postDataJSON());
    if (writes.length === 1) { await route.fulfill({ status: 503, json: { error: "private diagnostic" } }); return; }
    await route.fulfill({ status: 201, json: { order_id: "ord_account", checkout_url: CHECKOUT_URL } });
  });
  await page.route(`${CHECKOUT_URL}**`, route => route.fulfill({ contentType: "text/html", body: "<title>checkout</title>" }));
  await page.goto("/console/billing?tab=accounts");
  await expect(page.getByText("US$19.99 / 月", { exact: true })).toBeVisible();
  // An unpriced account the tenant does not hold cannot be bought, so it is not listed.
  // 商户未持有且未定价的账号无法购买，因此不列出。
  await expect(page.getByText("Legacy Codex", { exact: true })).toHaveCount(0);
  const buttons = page.getByRole("button", { name: "购买一个月", exact: true });
  await expect(buttons).toHaveCount(1);
  await buttons.first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("US$19.99");
  await expect(dialog).toContainText("仅允许使用已购买且未到期的账号");
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("支付服务暂时不可用");
  expect(writes).toHaveLength(1);
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await page.waitForURL(`${CHECKOUT_URL}**`);
  expect(writes).toEqual([{ expected_price_minor: 1999, currency: "USD" }, { expected_price_minor: 1999, currency: "USD" }]);
});

test("account checkout explains a disabled payment provider", async ({ page }) => {
  const account = { id: "acc_monthly", node_id: "agent-a", runtime_id: "claude-a", name: "Dedicated Claude", price_minor: 1999, currency: "USD", active: true, owned: false };
  await page.route("**/api/admin/admin/v1/billing/accounts", route => route.fulfill({ json: { items: [account] } }));
  await page.route("**/api/admin/admin/v1/billing/accounts/acc_monthly/checkout", route => route.fulfill({ status: 404, json: { error: "not found" } }));
  await page.goto("/console/billing?tab=accounts");
  await page.getByRole("button", { name: "购买一个月", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("在线支付未开启");
});

test("operators list individual accounts and preserve their runtime identity when editing", async ({ page }) => {
  const writes: unknown[] = [];
  const account = { id: "acc_monthly", node_id: "agent-a", runtime_id: "claude-a", name: "Dedicated Claude", price_minor: 500, currency: "EUR", dodo_product_id: "pdt_pwyw", active: true, owned: false };
  await page.route("**/api/operator/operator/v1/billing/accounts", async route => {
    if (route.request().method() === "GET") { await route.fulfill({ json: { items: writes.length ? [account] : [] } }); return; }
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: account });
  });
  await page.route("**/api/operator/operator/v1/nodes", route => route.fulfill({ json: {
    collected_at: "2026-11-09T00:00:00Z", partial: false, replicas: [], nodes: [{ node_id: "agent-a", live: true, maintenance: false, observed_at: "2026-11-09T00:00:00Z", runtimes: [
      { id: "claude-a", kind: "claude", state: "healthy", version: "test", models: [] },
    ] }],
  } }));
  await page.goto("/operator/billing");
  await page.getByRole("tab", { name: "账号现货", exact: true }).click();
  await page.getByRole("button", { name: "添加账号", exact: true }).click();
  await page.getByLabel("账号名称", { exact: true }).fill(account.name);
  await page.getByRole("combobox", { name: "选择 Agent", exact: true }).selectOption(account.node_id);
  await page.getByRole("combobox", { name: "选择 Claude Code / Codex 账号", exact: true }).selectOption(account.runtime_id);
  await page.getByLabel("币种（ISO 4217，如 USD）", { exact: true }).selectOption("EUR");
  await page.getByLabel("月价（法币）", { exact: true }).fill("5.001");
  await page.getByLabel("Dodo 产品 ID", { exact: true }).fill("pdt_pwyw");
  await expect(page.getByText("请输入大于零的月价，小数位不得超过该币种的最小单位。", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "保存账号", exact: true })).toBeDisabled();
  await page.getByLabel("月价（法币）", { exact: true }).fill("5");
  await page.getByRole("button", { name: "保存账号", exact: true }).click();
  await expect(page.getByText(account.name, { exact: true })).toBeVisible();
  expect(writes).toEqual([{ name: account.name, node_id: account.node_id, runtime_id: account.runtime_id, kind: "claude", price_minor: 500, currency: "EUR", dodo_product_id: "pdt_pwyw", active: true }]);
  await page.getByRole("button", { name: "编辑账号", exact: true }).click();
  await expect(page.getByLabel("月价（法币）", { exact: true })).toHaveValue("5");
  await expect(page.getByLabel("Agent ID", { exact: true })).toBeDisabled();
  await expect(page.getByLabel("运行时 ID", { exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await page.getByRole("button", { name: "添加账号", exact: true }).click();
  await expect(page.getByText("没有可添加的账号", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "保存账号", exact: true })).toBeDisabled();
});

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

test("billing sections open by mode, follow the query, and survive a reload", async ({ page }) => {
  const account = { id: "acc_mine", node_id: "agent-a", runtime_id: "claude-a", kind: "claude", name: "Mine", price_minor: 1999, currency: "USD", active: true, owned: true, expires_at: "2026-12-01T00:00:00Z" };
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: { ...BILLING_OVERVIEW, mode: "account_monthly" } }));
  await page.route("**/api/admin/admin/v1/billing/accounts", route => route.fulfill({ json: { items: [account] } }));
  await page.goto("/console/billing");
  await expect(page.getByRole("tab", { name: "账号现货", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("heading", { name: "我的账号", exact: true })).toBeVisible();
  await expect(page.getByText("暂无可购买的账号", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "续费一个月", exact: true })).toBeEnabled();
  await page.getByRole("tab", { name: "账目", exact: true }).click();
  await expect(page).toHaveURL(/[?&]tab=ledger(&|$)/);
  await expect(page.getByText("变动后余额", { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("tab", { name: "账目", exact: true })).toHaveAttribute("aria-selected", "true");  // Nothing is on presale, so that section is not offered, even when asked for.
  // 没有预售商品，因此即使指定也不提供该分区。
  await expect(page.getByRole("tab", { name: "账号购买", exact: true })).toHaveCount(0);
  await page.goto("/console/billing?tab=presales");
  await expect(page.getByRole("tab", { name: "账号现货", exact: true })).toHaveAttribute("aria-selected", "true");
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
  await dialog.getByLabel("货币（ISO 4217，如 USD）", { exact: true }).selectOption("JPY");
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


test("disabled account sales explain the deployment setting", async ({ page }) => {
  await page.route("**/api/operator/operator/v1/billing/accounts", route => route.fulfill({ status: 404, json: { error: "not_found" } }));
  await page.goto("/operator/billing");
  await page.getByRole("tab", { name: "账号现货", exact: true }).click();
  await expect(page.getByText("未开启账号包月", { exact: true })).toBeVisible();
  await expect(page.getByText("请在控制面开启 AccountSales.Enabled；商户在线购买还需要开启 Billing.Enabled 并配置 Dodo，然后重新启动控制面。", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "添加账号", exact: true })).toBeDisabled();
  await expect(page.locator("main")).not.toContainText("目标不存在，或不属于当前租户");
});


test("a failed Agent inventory can be retried from the account editor", async ({ page }) => {
  let reads = 0;
  await page.route("**/api/operator/operator/v1/nodes", route => {
    reads += 1;
    return route.fulfill(reads === 1 ? { status: 404, json: { error: "not_found" } } : {
      json: { collected_at: "2026-11-09T00:00:00Z", partial: false, replicas: [], nodes: [] },
    });
  });
  await page.goto("/operator/billing");
  await page.getByRole("tab", { name: "账号现货", exact: true }).click();
  await page.getByRole("button", { name: "添加账号", exact: true }).click();
  const retry = page.getByRole("button", { name: "重试", exact: true });
  await expect(retry).toBeEnabled();
  await expect(page.getByText("没有可添加的账号", { exact: true })).toHaveCount(0);
  await retry.click();
  await expect(page.getByText("没有可添加的账号", { exact: true })).toBeVisible();
  expect(reads).toBe(2);
});


test("tenants pre-order a presale, cannot buy a sold-out one, and see the paid order awaiting delivery", async ({ page }) => {
  const presale = { id: "pre_claude", kind: "claude", name: "Claude Code 预售", price_minor: 1500, currency: "USD", active: true, available: true };
  const writes: unknown[] = [];
  await page.route("**/api/admin/admin/v1/billing/presales", route => route.fulfill({ json: { items: [presale, { ...presale, id: "pre_codex", kind: "codex", name: "Codex 预售", available: false }] } }));
  await page.route("**/api/admin/admin/v1/billing/presales/pre_claude/checkout", async route => {
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: { order_id: "ord_presale", checkout_url: CHECKOUT_URL } });
  });
  await page.route(/\/api\/admin\/admin\/v1\/billing\/orders(\?|$)/, route => route.fulfill({ json: { items: [{
    id: "ord_waiting", package_id: "", presale_id: "pre_claude", amount_minor: 1500, currency: "USD", credits_micro: 0, refunded_minor: 0, refundable_minor: 0,
    status: "paid", created_at: "2026-11-11T00:00:00Z", updated_at: "2026-11-11T00:00:00Z",
  }] } }));
  await page.route(`${CHECKOUT_URL}**`, route => route.fulfill({ contentType: "text/html", body: "<title>checkout</title>" }));
  await page.goto("/console/billing");
  await expect(page.getByRole("tab", { name: "账号购买", exact: true })).toHaveAttribute("aria-selected", "true");
  await page.goto("/console/billing?tab=orders");
  await expect(page.getByText("已付款，待发货", { exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "账号购买", exact: true }).click();
  await expect(page.getByText("账号购买", { exact: true }).nth(1)).toBeVisible();
  await expect(page.getByText("已售罄", { exact: true })).toBeVisible();
  const buttons = page.getByRole("button", { name: "购买一个月", exact: true });
  await expect(buttons).toHaveCount(2);
  await expect(buttons.nth(1)).toBeDisabled();
  await buttons.first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("US$15.00");
  await expect(dialog).toContainText("Claude Code 账号");
  await expect(dialog).toContainText("一个月自分配时起算");
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await page.waitForURL(`${CHECKOUT_URL}**`);
  expect(writes).toEqual([{ expected_price_minor: 1500, currency: "USD" }]);
});

test("a full presale explains itself and the checkout is not retried", async ({ page }) => {
  const presale = { id: "pre_claude", kind: "claude", name: "Claude Code 预售", price_minor: 1500, currency: "USD", active: true, available: true };
  let writes = 0;
  await page.route("**/api/admin/admin/v1/billing/presales", route => route.fulfill({ json: { items: [presale] } }));
  await page.route("**/api/admin/admin/v1/billing/presales/pre_claude/checkout", route => { writes++; return route.fulfill({ status: 409, json: { error: "conflict" } }); });
  await page.goto("/console/billing?tab=presales");
  await page.getByRole("button", { name: "购买一个月", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("预售名额已满");
  expect(writes).toBe(1);
});

test("operators create a presale and assign an awaiting order an account of the same kind", async ({ page }) => {
  const at = "2026-11-11T00:00:00Z";
  const presale = { id: "pre_claude", kind: "claude", name: "Claude Code 预售", price_minor: 1500, currency: "USD", active: true, available: true,
    dodo_product_id: "pdt_pre", capacity: 3, outstanding: 1, updated_at: at };
  const awaiting = { id: "ord_waiting", tenant_id: "tnt_buyer", package_id: "", presale_id: "pre_claude", amount_minor: 1500, currency: "USD", credits_micro: 0,
    refunded_minor: 0, refundable_minor: 1500, status: "paid", payment_id: "pay_1", created_at: at, updated_at: at };
  const account = { id: "acc_free", node_id: "agent-a", runtime_id: "claude-free", kind: "claude", name: "Claude 空闲", price_minor: 0, currency: "", active: false, owned: false };
  const presaleWrites: unknown[] = [];
  const assignWrites: unknown[] = [];
  let assigned = false;
  await page.route("**/api/operator/operator/v1/billing/presales", async route => {
    if (route.request().method() === "GET") { await route.fulfill({ json: { items: presaleWrites.length ? [presale] : [] } }); return; }
    presaleWrites.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: presale });
  });
  await page.route(url => url.pathname === "/api/operator/operator/v1/billing/orders", route => {
    expect(new URL(route.request().url()).searchParams.get("fulfillment")).toBe("awaiting");
    return route.fulfill({ json: { items: assigned ? [] : [awaiting], next_cursor: "" } });
  });
  await page.route("**/api/operator/operator/v1/billing/accounts", route => route.fulfill({ json: { items: [
    account,
    { ...account, id: "acc_codex", runtime_id: "codex-a", kind: "codex", name: "Codex 空闲" },
    { ...account, id: "acc_held", runtime_id: "claude-held", name: "Claude 他人", tenant_id: "tnt_other", expires_at: "2099-01-01T00:00:00Z", owned: false },
  ] } }));
  await page.route("**/api/operator/operator/v1/billing/orders/ord_waiting/assign", async route => {
    assignWrites.push(route.request().postDataJSON());
    assigned = true;
    await route.fulfill({ json: { ...awaiting, account_id: "acc_free", account_expires_at: "2026-12-11T00:00:00Z" } });
  });
  await page.goto("/operator/billing");
  await page.getByRole("tab", { name: "账号购买", exact: true }).click();
  await page.getByRole("button", { name: "添加预售", exact: true }).click();
  await page.getByRole("combobox", { name: "账号类型", exact: true }).selectOption("claude");
  await expect(page.getByLabel("预售名称", { exact: true })).toHaveValue("Claude Code");
  await page.getByLabel("预售名称", { exact: true }).fill(presale.name);
  await page.getByLabel("月价（法币）", { exact: true }).fill("15");
  await page.getByLabel("Dodo 产品 ID", { exact: true }).fill("pdt_pre");
  await page.getByLabel("名额上限", { exact: true }).fill("0");
  await expect(page.getByText("名额上限须为 1 到 10000 之间的整数。", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "保存预售", exact: true })).toBeDisabled();
  await page.getByLabel("名额上限", { exact: true }).fill("3");
  await page.getByRole("button", { name: "保存预售", exact: true }).click();
  await expect(page.getByText("待发货 1 / 名额 3", { exact: true })).toBeVisible();
  expect(presaleWrites).toEqual([{ kind: "claude", name: presale.name, price_minor: 1500, currency: "USD", dodo_product_id: "pdt_pre", capacity: 3, active: true, alipay_amount: 0 }]);

  await expect(page.getByText("ord_waiting", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "分配账号", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("tnt_buyer");
  const select = dialog.getByRole("combobox", { name: "选择账号", exact: true });
  await expect(select.getByRole("option")).toHaveCount(2);
  await expect(select.getByRole("option").filter({ hasText: "Claude 空闲" })).toHaveCount(1);
  await expect(select.getByRole("option").filter({ hasText: "Codex" })).toHaveCount(0);
  await expect(select.getByRole("option").filter({ hasText: "他人" })).toHaveCount(0);
  const confirm = dialog.getByRole("button", { name: "分配账号", exact: true });
  await expect(confirm).toBeDisabled();
  await select.selectOption("acc_free");
  await confirm.click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("status").filter({ hasText: "已分配账号" })).toBeVisible();
  await expect(page.getByText("没有待发货的订单", { exact: true })).toBeVisible();
  expect(assignWrites).toEqual([{ account_id: "acc_free" }]);
});

test("Alipay redeems with one action id across a lost answer and opens the purchase entry", async ({ page }) => {
  const at = "2026-11-11T08:00:00Z";
  const overview = {
    capability_status: "ENABLED", product_name: "点数包", billing_mode: "QUANTITY", quota_unit: "POINT", supported: true, can_purchase: true, can_redeem: true,
    plans: [{ sku_id: "sku_100", name: "100 积分", price: "9.90", currency: "CNY", quota_unit: "POINT", quota_amount: 100 }],
    access: { total: 100, used: 40, remaining: 60 }, credits_per_unit_micro: 2_000_000, max_amount: 99_999_999, pending: null,
  };
  const entry = "https://openapi.alipay.example.invalid/entry";
  const redeems: { action_id: string; amount: number }[] = [];
  const purchases: unknown[] = [];
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: { ...BILLING_OVERVIEW, has_customer: false, packages: [], payment_methods: { dodo: false, alipay: true } } }));
  await page.route("**/api/admin/admin/v1/billing/alipay", route => route.fulfill({ json: overview }));
  await page.route(/\/api\/admin\/admin\/v1\/billing\/alipay\/redemptions(\?|$)/, async route => {
    if (route.request().method() === "GET") {
      await route.fulfill({ json: { items: [], next_cursor: "" } });
      return;
    }
    const body = route.request().postDataJSON();
    redeems.push(body);
    if (redeems.length === 1) {
      await route.fulfill({ status: 503, json: { error: "opcpay: internal detail" } });
      return;
    }
    await route.fulfill({ json: { id: "apr_1", action_id: body.action_id, amount: body.amount, quota_unit: "POINT", credits_micro: body.amount * 2_000_000, status: "succeeded", reason_code: "", created_at: at, updated_at: at } });
  });
  await page.route("**/api/admin/admin/v1/billing/alipay/purchases", async route => {
    purchases.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: { id: "apu_1", purchase_url: entry, expire_time: "" } });
  });
  await page.route(`${entry}**`, route => route.fulfill({ contentType: "text/html", body: "<title>alipay</title>" }));

  await page.goto("/console/billing");
  await expect(page.getByRole("tab", { name: "支付宝", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("tab", { name: "充值包", exact: true })).toHaveCount(0);
  await expect(page.getByText("60 积分", { exact: true })).toBeVisible();
  const amount = page.getByLabel("兑换数量（积分）", { exact: true });
  const redeem = page.getByRole("button", { name: "兑换为点数", exact: true });
  await amount.fill("61");
  await expect(page.getByText("支付宝额度不足，请先用支付宝购买套餐。", { exact: true })).toBeVisible();
  await expect(redeem).toBeDisabled();
  await amount.fill("5");
  await expect(page.getByText("可获得 10 点", { exact: true })).toBeVisible();
  await redeem.click();
  await expect(page.locator("main").getByRole("alert")).toHaveText("支付服务暂时不可用，请稍后再试。");
  await expect(page.locator("main")).not.toContainText("internal detail");
  await expect(page.getByText(/上次请求结果未知/).first()).toBeVisible();
  await redeem.click();
  await expect(page.getByText("兑换成功，已记入 10 点。", { exact: true })).toBeVisible();
  expect(redeems).toHaveLength(2);
  expect(redeems[0]).toEqual(redeems[1]);
  expect(Object.keys(redeems[0]).sort()).toEqual(["action_id", "amount"]);
  await page.screenshot({ path: "test-results/alipay-billing.png", fullPage: true });

  await page.getByRole("button", { name: "用支付宝购买", exact: true }).click();
  await page.waitForURL(entry);
  expect(purchases).toHaveLength(1);
  expect(Object.keys(purchases[0] as object).sort()).toEqual(["action_id", "sku_id"]);
});

// ---- 易支付 (EZFP) ----
const EZFP_AT = "2026-11-12T00:00:00Z";
const EZFP_METHODS = { dodo: true, alipay: false, ezfp: true, ezfp_types: ["alipay", "wxpay"] };
const CNY_PACKAGE = { id: "pkg_fixed", name: "CNY pack", kind: "fixed", credits_micro: 10_000_000, price_minor: 6800, currency: "CNY", active: true, sort_order: 1, dodo_available: true, ezfp_available: true, created_at: EZFP_AT, updated_at: EZFP_AT };
const EZFP_ORDER_ID = `ord_${"0123456789abcdef".repeat(2).slice(0, 28)}`;
const ezfpOverview = (patch: Record<string, unknown> = {}) => ({ ...BILLING_OVERVIEW, packages: [CNY_PACKAGE], payment_methods: EZFP_METHODS, ...patch });

test("choosing WeChat Pay sends one EZFP checkout with provider and pay_type and redirects", async ({ page }) => {
  const writes: unknown[] = [];
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: ezfpOverview() }));
  await page.route("**/api/admin/admin/v1/billing/checkout", async route => {
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: { order_id: EZFP_ORDER_ID, checkout_url: CHECKOUT_URL } });
  });
  await page.route(`${CHECKOUT_URL}**`, route => route.fulfill({ contentType: "text/html", body: "<title>checkout</title>" }));
  await page.goto("/console/billing");
  const group = page.getByRole("group", { name: "购买", exact: true });
  await expect(group.getByRole("button")).toHaveText(["银行卡 / Dodo", "支付宝", "微信支付"]);
  await group.getByRole("button", { name: "微信支付", exact: true }).click();
  await page.waitForURL(CHECKOUT_URL);
  expect(writes).toEqual([{ package_id: "pkg_fixed", provider: "ezfp", pay_type: "wxpay" }]);
});

test("a Dodo click on a package that EZFP also sells sends neither provider nor pay_type", async ({ page }) => {
  const writes: unknown[] = [];
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: ezfpOverview() }));
  await page.route("**/api/admin/admin/v1/billing/checkout", async route => {
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: { order_id: EZFP_ORDER_ID, checkout_url: CHECKOUT_URL } });
  });
  await page.route(`${CHECKOUT_URL}**`, route => route.fulfill({ contentType: "text/html", body: "<title>checkout</title>" }));
  await page.goto("/console/billing");
  await page.getByRole("button", { name: "银行卡 / Dodo", exact: true }).click();
  await page.waitForURL(CHECKOUT_URL);
  expect(writes).toEqual([{ package_id: "pkg_fixed" }]);
});

test("a package only Dodo can sell keeps the single 购买 button, and an EZFP-only deployment lists its packages", async ({ page }) => {
  // Not payable through EZFP (non-CNY): today's single button, even with EZFP on.
  // 不能经易支付支付（非 CNY）：即使开启易支付，仍是今天的单个按钮。
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: ezfpOverview({ packages: [{ ...CNY_PACKAGE, currency: "USD", ezfp_available: false }] }) }));
  await page.goto("/console/billing");
  await expect(page.getByRole("button", { name: "购买", exact: true })).toHaveCount(1);
  await expect(page.getByRole("group", { name: "购买", exact: true })).toHaveCount(0);

  // Dodo off, EZFP on with one type: packages and orders are still offered.
  // Dodo 关闭、易支付开启且只有一种类型：充值包与订单分区仍然提供。
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: ezfpOverview({ payment_methods: { dodo: false, alipay: false, ezfp: true, ezfp_types: ["wxpay", "bogus"] }, packages: [{ ...CNY_PACKAGE, dodo_available: false }] }) }));
  await page.reload();
  await expect(page.getByRole("tab", { name: "充值包", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("tab", { name: "订单", exact: true })).toHaveCount(1);
  await expect(page.getByRole("button", { name: "微信支付", exact: true })).toHaveCount(1);
  await expect(page.getByRole("button", { name: "支付宝", exact: true })).toHaveCount(0);
});

test("an EZFP return strips the URL, asks the control plane once, and trusts only the server", async ({ page }) => {
  const events: string[] = [];
  let polls = 0;
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: ezfpOverview() }));
  await page.route(`**/api/admin/admin/v1/billing/orders/${EZFP_ORDER_ID}/refresh`, async route => {
    events.push(`refresh:${route.request().method()}:${route.request().postData() ?? ""}`);
    await route.fulfill({ json: { id: EZFP_ORDER_ID, package_id: "pkg_fixed", amount_minor: 6800, currency: "CNY", credits_micro: 10_000_000, refunded_minor: 0, status: "pending", provider: "ezfp", pay_type: "wxpay", payment_id: "T1", created_at: EZFP_AT, updated_at: EZFP_AT } });
  });
  await page.route(/\/api\/admin\/admin\/v1\/billing\/orders(\?|$)/, async route => {
    const poll = new URL(route.request().url()).searchParams.get("limit") === "1";
    if (poll) { polls++; events.push("poll"); }
    const status = poll && polls < 2 ? "pending" : "paid";
    await route.fulfill({ json: { items: [{ id: EZFP_ORDER_ID, package_id: "pkg_fixed", amount_minor: 6800, currency: "CNY", credits_micro: 10_000_000, refunded_minor: 0, status, provider: "ezfp", pay_type: "wxpay", payment_id: "T1", created_at: EZFP_AT, updated_at: EZFP_AT }], next_cursor: "" } });
  });
  // TRADE_SUCCESS in the query proves nothing: the notice follows the server's order status.
  // 查询里的 TRADE_SUCCESS 什么也证明不了：提示跟随的是服务端的订单状态。
  await page.goto(`/console/billing?out_trade_no=${EZFP_ORDER_ID}&trade_no=T1&trade_status=TRADE_SUCCESS&sign=x&sign_type=RSA&buyer=b&money=68.00&pid=1000&type=wxpay&name=n&param=p&timestamp=1`);
  await expect(page.getByRole("status").filter({ hasText: "支付确认中" })).toBeVisible();
  await expect(page).toHaveURL(/\/console\/billing\?tab=orders$/);
  expect(page.url()).not.toMatch(/sign|buyer|money|trade_status/);
  await expect(page.getByRole("status").filter({ hasText: "服务端已确认" })).toBeVisible({ timeout: 10_000 });
  expect(events.filter(event => event.startsWith("refresh"))).toEqual(["refresh:POST:"]);
  expect(events[0], "the re-check is sent before the polling starts").toMatch(/^refresh/);
  expect(polls).toBeGreaterThanOrEqual(2);
  await expect(page.getByRole("tab", { name: "订单", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("易支付 · 微信", { exact: true })).toBeVisible();
});

test("an EZFP return without our order id and a trade number is an ordinary visit", async ({ page }) => {
  let refreshes = 0;
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: ezfpOverview() }));
  await page.route("**/api/admin/admin/v1/billing/orders/**", route => { refreshes++; return route.fulfill({ status: 500, json: {} }); });
  await page.goto("/console/billing?out_trade_no=ord_not-ours&trade_no=T1&trade_status=TRADE_SUCCESS");
  await expect(page.getByRole("tab", { name: "充值包", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("status").filter({ hasText: "支付确认中" })).toHaveCount(0);
  expect(refreshes).toBe(0);
});

test("a pending EZFP order is re-checked once per click and its row follows the server", async ({ page }) => {
  let posts = 0;
  const base = { id: EZFP_ORDER_ID, package_id: "pkg_fixed", amount_minor: 6800, currency: "CNY", credits_micro: 10_000_000, refunded_minor: 0, provider: "ezfp", pay_type: "alipay", payment_id: "T1", created_at: EZFP_AT, updated_at: EZFP_AT };
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: ezfpOverview() }));
  await page.route(/\/api\/admin\/admin\/v1\/billing\/orders(\?|$)/, route => route.fulfill({ json: { items: [
    { ...base, status: "pending" },
    { ...base, id: "ord_paid", status: "paid" },
    { ...base, id: "ord_dodo", status: "pending", provider: "dodo", pay_type: "", payment_id: "" },
  ], next_cursor: "" } }));
  await page.route(`**/api/admin/admin/v1/billing/orders/${EZFP_ORDER_ID}/refresh`, async route => {
    posts++;
    await route.fulfill({ json: { ...base, status: posts === 1 ? "pending" : "paid" } });
  });
  await page.goto("/console/billing?tab=orders");
  await expect(page.getByText("易支付 · 支付宝", { exact: true })).toHaveCount(2);
  await expect(page.getByText("Dodo", { exact: true })).toBeVisible();
  const recheck = page.getByRole("button", { name: "再次确认", exact: true });
  await expect(recheck, "only the pending EZFP order can be re-checked").toHaveCount(1);
  await recheck.click();
  await expect(page.getByText("仍在等待支付确认，稍后可再次确认。", { exact: true })).toBeVisible();
  expect(posts).toBe(1);
  await recheck.click();
  await expect(recheck).toHaveCount(0);
  await expect(page.getByText("待支付", { exact: true })).toHaveCount(1);
  expect(posts).toBe(2);
});

test("a CNY presale sold only through EZFP is not sold out and is paid with the chosen type", async ({ page }) => {
  const writes: unknown[] = [];
  const presale = { id: "pre_cny", kind: "claude", name: "Claude Code 预售", price_minor: 6800, currency: "CNY", active: true, available: false, ezfp_available: true };
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: ezfpOverview({ packages: [] }) }));
  await page.route("**/api/admin/admin/v1/billing/presales", route => route.fulfill({ json: { items: [presale] } }));
  await page.route("**/api/admin/admin/v1/billing/presales/pre_cny/checkout", async route => {
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: { order_id: EZFP_ORDER_ID, checkout_url: CHECKOUT_URL } });
  });
  await page.route(`${CHECKOUT_URL}**`, route => route.fulfill({ contentType: "text/html", body: "<title>checkout</title>" }));
  await page.goto("/console/billing?tab=presales");
  await expect(page.getByText("已售罄", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "银行卡 / Dodo", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "支付宝", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("¥68.00");
  await expect(dialog).toContainText("支付方式：支付宝");
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await page.waitForURL(CHECKOUT_URL);
  expect(writes).toEqual([{ expected_price_minor: 6800, currency: "CNY", provider: "ezfp", pay_type: "alipay" }]);
});

test("a monthly account in CNY offers Dodo only where Dodo can sell it, and EZFP carries its choice into the dialog", async ({ page }) => {
  const writes: unknown[] = [];
  const account = { id: "acc_cny", node_id: "agent-a", runtime_id: "claude-a", name: "Dedicated Claude", price_minor: 6800, currency: "CNY", active: true, owned: false, dodo_available: false, ezfp_available: true };
  await page.route("**/api/admin/admin/v1/billing", route => route.fulfill({ json: ezfpOverview() }));
  await page.route("**/api/admin/admin/v1/billing/accounts", route => route.fulfill({ json: { items: [account] } }));
  await page.route("**/api/admin/admin/v1/billing/accounts/acc_cny/checkout", async route => {
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: { order_id: EZFP_ORDER_ID, checkout_url: CHECKOUT_URL } });
  });
  await page.route(`${CHECKOUT_URL}**`, route => route.fulfill({ contentType: "text/html", body: "<title>checkout</title>" }));
  await page.goto("/console/billing?tab=accounts");
  await expect(page.getByRole("button", { name: "银行卡 / Dodo", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "微信支付", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("支付方式：微信支付");
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await page.waitForURL(CHECKOUT_URL);
  expect(writes).toEqual([{ expected_price_minor: 6800, currency: "CNY", provider: "ezfp", pay_type: "wxpay" }]);
});

test("operators can save a CNY package without a Dodo product id, but no other currency", async ({ page }) => {
  const writes: unknown[] = [];
  await page.route("**/api/operator/operator/v1/billing/packages", async route => {
    if (route.request().method() === "GET") { await route.fulfill({ json: { items: [] } }); return; }
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: { id: "pkg_cny", name: "CNY pack", kind: "fixed", credits_micro: 10_000_000, price_minor: 6800, currency: "CNY", active: true, sort_order: 0, dodo_product_id: "", created_at: EZFP_AT, updated_at: EZFP_AT } });
  });
  await page.goto("/operator/billing");
  await page.getByRole("button", { name: "新建充值包", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("币种为 CNY 时可留空，仅能经易支付购买。");
  await dialog.getByLabel("名称", { exact: true }).fill("CNY pack");
  await dialog.getByLabel("点数", { exact: true }).fill("10");
  await dialog.getByLabel("价格（主货币单位）", { exact: true }).fill("68");
  await dialog.getByRole("button", { name: "创建", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("Dodo 产品 ID");
  expect(writes).toHaveLength(0);
  await dialog.getByLabel("货币（ISO 4217，如 USD）", { exact: true }).selectOption("CNY");
  await dialog.getByRole("button", { name: "创建", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(writes).toEqual([{ name: "CNY pack", kind: "fixed", dodo_product_id: "", currency: "CNY", active: true, credits_micro: 10_000_000, price_minor: 6800, credits_per_minor_unit_micro: 0, min_amount_minor: 0, max_amount_minor: 0, sort_order: 0 }]);
});

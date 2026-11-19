import { test, expect, CATALOG_ENTRY, EFFECTIVE_PRICES, LONG_TEXT, PRICING_CONFIG } from "./fixtures.ts";

/** Operator token pricing pages (.scratch/token-pricing, item 07). The
 * control plane is the fixture interceptor; each case overrides the reads it
 * needs and records the writes.
 *
 * 运维 token 计价页面（.scratch/token-pricing 第 07 项）。控制面由夹具拦截器充当；
 * 每个用例覆盖它需要的读取并记录写操作。 */

type Write = { method: string; path: string; body: unknown };

test("model pricing warns about unpriced aliases, copies the official price and sends the revision", async ({ page }) => {
  const writes: Write[] = [];
  let conflict = false;
  await page.route("**/api/operator/operator/v1/billing/effective-prices", route => route.fulfill({ json: {
    ...EFFECTIVE_PRICES, unpriced_aliases: ["local-llama"], unpriced_requests_24h: 12,
    items: [...EFFECTIVE_PRICES.items, { model: "local-llama", routed: true, source: "none", rate_ppm: 1_000_000, items: [] }],
  } }));
  await page.route("**/api/operator/operator/v1/billing/pricing/models", async route => {
    const request = route.request();
    writes.push({ method: request.method(), path: new URL(request.url()).pathname, body: request.postDataJSON() });
    if (conflict) await route.fulfill({ status: 409, json: { error: "conflict" } });
    else await route.fulfill({ json: PRICING_CONFIG });
  });
  await page.goto("/operator/billing");
  await page.getByRole("tab", { name: "模型定价", exact: true }).click();

  await expect(page.locator("main").getByRole("alert").filter({ hasText: "有 1 个别名正在以 0 价计费" })).toContainText("最近 24 小时有 12 次请求按 0 价计费");
  await expect(page.getByText("3,689 个条目，来源：embedded", { exact: true })).toBeVisible();
  const row = page.getByRole("row").filter({ hasText: `org/${LONG_TEXT}` });
  await expect(row).toContainText("1.5x");
  await expect(row).toContainText("4.5 / 22.5");
  await row.getByRole("button", { name: "全部单价", exact: true }).click();
  await expect(page.getByText("输入超过 200,000 token", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: `编辑 org/${LONG_TEXT} 的定价`, exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel("参照模型（价目表条目）", { exact: true })).toHaveValue("claude-x");
  await expect(dialog.getByLabel("从价目表中选择…", { exact: true })).toHaveValue("claude-x");
  await expect(dialog.getByLabel("模型倍率", { exact: true })).toHaveValue("1.5");
  await dialog.getByRole("button", { name: "从官方价复制", exact: true }).click();
  await expect(dialog.getByLabel("输入单价（点 / 百万 token）", { exact: true })).toHaveValue("3");
  await expect(dialog.getByLabel("缓存读取单价（点 / 百万 token）", { exact: true })).toHaveValue("0.3");
  await dialog.getByLabel("输入单价（点 / 百万 token）", { exact: true }).fill("2.5");
  await dialog.getByLabel("输出单价（点 / 百万 token）", { exact: true }).fill("");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog.getByRole("alert")).toHaveText("输出单价必须填写。");
  expect(writes).toEqual([]);

  await dialog.getByLabel("输出单价（点 / 百万 token）", { exact: true }).fill("15");
  conflict = true;
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog.getByRole("alert")).toHaveText("定价配置已被其他人修改，已重新读取，请确认后再试。");
  expect(writes).toEqual([{ method: "PUT", path: "/api/operator/operator/v1/billing/pricing/models", body: {
    model: `org/${LONG_TEXT}`, catalog_model: "claude-x", rate_ppm: 1_500_000, base_revision: 7,
    prices: [{ meter: "input", price_micro_per_m: 2_500_000 }, { meter: "output", price_micro_per_m: 15_000_000 }, { meter: "input.cache_read", price_micro_per_m: 300_000 }],
  } }]);
});

test("the catalog review shows old and new prices and approves exactly once", async ({ page }) => {
  const writes: Write[] = [];
  let decided = false;
  await page.route("**/api/operator/operator/v1/billing/catalog/pending*", route => route.fulfill({ json: { next_cursor: "", items: decided ? [] : [{
    model: "gpt-x", kind: "change", status: "active", max_change_bp: 5_000, detected_at: "2026-11-16T08:00:00Z",
    current: [{ meter: "input", price_micro_per_m: 2_000_000 }, { meter: "output", price_micro_per_m: 8_000_000 }],
    proposed: [{ meter: "input", price_micro_per_m: 3_000_000 }, { meter: "output", price_micro_per_m: 8_000_000 }],
  }] } }));
  await page.route("**/api/operator/operator/v1/billing/catalog/pending/approve", async route => {
    const request = route.request();
    writes.push({ method: request.method(), path: new URL(request.url()).pathname, body: request.postDataJSON() });
    decided = true;
    await route.fulfill({ json: { model: "gpt-x", kind: "change", version: 2, before: [], after: [] } });
  });
  await page.goto("/operator/billing");
  const tab = page.getByRole("tab", { name: /价目表审核/ });
  await expect(tab.getByLabel("1 项待审核", { exact: true })).toBeVisible();
  await tab.click();
  await expect(page.getByText("最大变动 50%", { exact: true })).toBeVisible();
  const changes = page.getByRole("row").filter({ hasText: "输入" });
  await expect(changes).toContainText("2");
  await expect(changes).toContainText("3");
  await expect(changes).toContainText("+50%");
  await expect(page.getByRole("row").filter({ hasText: "输出" })).toHaveCount(0);

  await page.getByRole("button", { name: "批准", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "批准", exact: true }).click();
  await expect(page.getByText("没有待审核的变动", { exact: true })).toBeVisible();
  expect(writes).toEqual([{ method: "POST", path: "/api/operator/operator/v1/billing/catalog/pending/approve", body: { model: "gpt-x" } }]);
});

test("plans are created with factors and a tenant is moved onto one", async ({ page }) => {
  const writes: Write[] = [];
  const vip = { id: "plan_vip", name: "VIP", default_rate_ppm: 800_000, is_default: false, tenant_count: 0, models: [{ model: "a", rate_ppm: 900_000 }] };
  let plans: unknown[] = PRICING_CONFIG.plans;
  await page.route("**/api/operator/operator/v1/billing/plans", async route => {
    const request = route.request();
    if (request.method() === "GET") return route.fulfill({ json: { revision: PRICING_CONFIG.revision, items: plans } });
    writes.push({ method: request.method(), path: new URL(request.url()).pathname, body: request.postDataJSON() });
    plans = [...PRICING_CONFIG.plans, vip];
    await route.fulfill({ status: 201, json: { plan: vip, revision: { ...PRICING_CONFIG.revision, id: 8 } } });
  });
  await page.route("**/api/operator/operator/v1/billing/tenants/*/plan", async route => {
    const request = route.request();
    writes.push({ method: request.method(), path: new URL(request.url()).pathname, body: request.postDataJSON() });
    await route.fulfill({ status: 204 });
  });
  await page.route("**/api/operator/operator/v1/billing/tenants?*", route => route.fulfill({ json: { next_cursor: "", items: [
    { id: "tnt_p10", name: LONG_TEXT, status: "active", mode: "prepaid", balance_micro: 0, blocked: true, created_at: "2026-09-12T00:00:00Z",
      price_plan_id: "", pending_usage: { amount_micro: -400_000, request_count: 3, since: "2026-11-16T00:00:00Z", seal_at: "2026-11-17T00:00:00Z" } },
  ] } }));
  await page.goto("/operator/billing");
  await page.getByRole("tab", { name: "价格方案", exact: true }).click();
  await expect(page.getByRole("row").filter({ hasText: "Default" })).toContainText("所有未指定方案的租户");
  await expect(page.getByRole("row").filter({ hasText: "Default" }).getByRole("button", { name: "删除", exact: true })).toHaveCount(0);

  await page.getByRole("button", { name: "新建方案", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("名称", { exact: true }).fill("VIP");
  await dialog.getByLabel("默认系数", { exact: true }).fill("0.8");
  await dialog.getByRole("button", { name: "添加别名", exact: true }).click();
  await dialog.getByLabel("模型别名", { exact: true }).fill("a");
  await dialog.getByLabel("系数", { exact: true }).fill("101");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog.getByRole("alert")).toHaveText("第 1 行 倍率须在 0 到 100 之间，最多六位小数。");
  await dialog.getByLabel("系数", { exact: true }).fill("0.9");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("row").filter({ hasText: "VIP" })).toContainText("0.8x");

  await page.getByRole("tab", { name: "商户", exact: true }).click();
  await expect(page.getByRole("row").filter({ hasText: "tnt_p10" })).toContainText("0.4 点 · 3 次请求");
  await page.getByLabel(`${LONG_TEXT} 的价格方案`, { exact: true }).selectOption("plan_vip");
  await expect(page.getByText("价格方案已更新", { exact: true })).toBeVisible();

  expect(writes).toEqual([
    { method: "POST", path: "/api/operator/operator/v1/billing/plans", body: { name: "VIP", default_rate_ppm: 800_000, models: [{ model: "a", rate_ppm: 900_000 }], base_revision: 7 } },
    { method: "PUT", path: "/api/operator/operator/v1/billing/tenants/tnt_p10/plan", body: { plan_id: "plan_vip" } },
  ]);
});

for (const width of [1280, 320]) {
  test(`the pricing tabs fit a ${width}px screen without page-level horizontal scrolling`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route("**/api/operator/operator/v1/billing/catalog/pending*", route => route.fulfill({ json: { next_cursor: "", items: [{
      model: `${LONG_TEXT}/model`, kind: "remove", status: "removed_upstream", max_change_bp: null, detected_at: "2026-11-16T08:00:00Z", current: CATALOG_ENTRY.prices,
    }] } }));
    await page.goto("/operator/billing");
    for (const name of [/模型定价/, /价目表审核/, /价格方案/]) {
      await page.getByRole("tab", { name }).click();
      await expect(page.locator("main").getByRole("heading").first()).toBeVisible();
      await page.waitForLoadState("networkidle");
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);
    }
    await page.getByRole("tab", { name: /价目表审核/ }).click();
    await expect(page.getByText("变动无界（含新增、删除或从 0 起）", { exact: true })).toBeVisible();
  });
}

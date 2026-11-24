import { test, expect } from "./fixtures.ts";

// Token pricing on tenant pages (.scratch/token-pricing, item 08): credits on
// the usage page, per-request detail, model prices and the usage ledger.
// 租户页面上的 token 计价（.scratch/token-pricing 第 08 项）：用量页的点数、逐笔明细、
// 模型价格与用量账目。

const at = "2026-11-16T08:00:00Z";

const pricedSummary = {
  items: [
    { key_id: "key_p10", user_id: "usr_p10", model: "claude-x", prompt_tokens: 1000, completion_tokens: 100, total_tokens: 1100, request_count: 2,
      cache_read_tokens: 600, cache_write_tokens: 300, cost_micro: 3_105 },
    { model: "local-llama", prompt_tokens: 50, completion_tokens: 10, total_tokens: 60, request_count: 1, cache_read_tokens: 0, cache_write_tokens: 0, cost_micro: 0 },
  ],
};

const pricedRecord = {
  request_id: "req_priced", created_at: at, model: "claude-x", endpoint: "anthropic_messages", key_id: "key_p10", user_id: "usr_p10",
  prompt_tokens: 1000, completion_tokens: 100, total_tokens: 1100, cache_read_tokens: 600, cache_write_tokens: 300,
  meters: [
    { meter: "input", quantity: 1000, billed: 100, price_micro_per_m: 3_000_000, cost_micro: 300 },
    { meter: "input.cache_read", quantity: 600, billed: 600, price_micro_per_m: 300_000, cost_micro: 180 },
    { meter: "input.cache_write", quantity: 300, billed: 300, price_micro_per_m: 3_750_000, cost_micro: 1_125 },
    { meter: "output", quantity: 100, billed: 100, price_micro_per_m: 15_000_000, cost_micro: 1_500 },
  ],
  cost_micro: 3_105, base_cost_micro: 2_587, rate_ppm: 1_200_000, price_source: "catalog", charged: true, tier: 200_000,
};

const ownNodeRecord = {
  request_id: "req_own", created_at: at, model: "local-llama", endpoint: "chat", prompt_tokens: 50, completion_tokens: 10, total_tokens: 60,
  meters: [{ meter: "input", quantity: 50, billed: 50, price_micro_per_m: 0, cost_micro: 0 }, { meter: "output", quantity: 10, billed: 10, price_micro_per_m: 0, cost_micro: 0 }],
  cost_micro: 0, base_cost_micro: 0, rate_ppm: 1_000_000, price_source: "default", charged: false, own_node: true,
};

test("the usage page shows credits and cache tokens, and expands a request into its meters", async ({ page }) => {
  const recordQueries: URLSearchParams[] = [];
  await page.route(/\/api\/admin\/admin\/v1\/usage\/summary(\?|$)/, route => route.fulfill({ json: pricedSummary }));
  await page.route(/\/api\/admin\/admin\/v1\/usage\/records(\?|$)/, route => {
    const query = new URL(route.request().url()).searchParams;
    recordQueries.push(query);
    const items = query.get("model") === "local-llama" ? [ownNodeRecord] : [pricedRecord, ownNodeRecord];
    return route.fulfill({ json: { items, next_cursor: "" } });
  });
  await page.goto("/console/usage");

  await expect(page.getByText("消耗点数", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("0.003105", { exact: true }).first()).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "缓存读取" }).first()).toBeVisible();

  await page.getByRole("tab", { name: "明细", exact: true }).click();
  await expect(page.getByText("req_priced")).toHaveCount(0);
  await expect(page.getByText("自有节点不计费", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "查看计费明细" }).first().click();
  await expect(page.getByRole("cell", { name: "缓存写入（5 分钟）", exact: true })).toBeVisible();
  await expect(page.getByText("倍率：1.2×", { exact: true })).toBeVisible();
  await expect(page.getByText("价格来源：官方价", { exact: true })).toBeVisible();
  await expect(page.getByText("长上下文档位：输入超过 200,000 token", { exact: true })).toBeVisible();
  expect(recordQueries.at(-1)?.get("limit")).toBe("50");

  await page.getByRole("combobox", { name: "按模型筛选" }).click();
  await page.getByRole("option", { name: "local-llama", exact: true }).click();
  await expect.poll(() => recordQueries.at(-1)?.get("model")).toBe("local-llama");
});

test("with billing off the usage page shows no credits anywhere", async ({ page }) => {
  await page.route(/\/api\/admin\/admin\/v1\/usage\/records(\?|$)/, route => route.fulfill({ json: { items: [{
    request_id: "req_free", created_at: at, model: "fixture-model", endpoint: "chat", prompt_tokens: 3, completion_tokens: 2, total_tokens: 5,
    meters: [{ meter: "input", quantity: 3, billed: 3 }, { meter: "output", quantity: 2, billed: 2 }],
  }], next_cursor: "" } }));
  await page.goto("/console/usage?tab=records");
  await expect(page.getByRole("tab", { name: "明细", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("button", { name: "查看计费明细" })).toBeVisible();
  await expect(page.getByText("消耗点数")).toHaveCount(0);
  await page.getByRole("button", { name: "查看计费明细" }).click();
  await expect(page.getByRole("columnheader", { name: "单价（点 / 百万）" })).toHaveCount(0);
});

test("the ledger shows pending usage and links a sealed usage entry to its requests", async ({ page }) => {
  const recordQueries: URLSearchParams[] = [];
  await page.route(/\/api\/admin\/admin\/v1\/billing\/ledger(\?|$)/, route => route.fulfill({ json: {
    items: [{ id: "led_usage", kind: "usage", amount_micro: -50_000, balance_after_micro: 9_950_000, ref: "12 requests", created_at: "2026-11-16T16:00:30Z",
      period_start: "2026-11-16T01:00:00Z", period_end: "2026-11-16T15:58:00Z", request_count: 12 }],
    pending_usage: { amount_micro: -4_000, request_count: 3, since: "2026-11-16T16:01:00Z", last: "2026-11-16T16:05:00Z", seal_at: "2026-11-17T16:00:00Z" },
    next_cursor: "",
  } }));
  await page.route(/\/api\/admin\/admin\/v1\/usage\/records(\?|$)/, route => {
    recordQueries.push(new URL(route.request().url()).searchParams);
    return route.fulfill({ json: { items: [], next_cursor: "" } });
  });
  await page.goto("/console/billing?tab=ledger");
  await expect(page.getByText("进行中", { exact: true })).toBeVisible();
  await expect(page.getByText(/^3 次请求，已从余额扣除，尚未封账/)).toBeVisible();
  await expect(page.getByText(/· 12 次请求$/)).toBeVisible();
  await expect(page.getByText(/账目按扣费时间分段/)).toBeVisible();

  await page.getByRole("link", { name: "查看这段时间的明细" }).click();
  await expect(page).toHaveURL(/\/console\/usage\?tab=records&since=/);
  await expect(page.getByRole("tab", { name: "明细", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect.poll(() => recordQueries.at(-1)?.get("until")).toBe("2026-11-16T15:58:01.000Z");
  expect(recordQueries.at(-1)?.get("since")).toBe("2026-11-16T00:50:00.000Z");
});

test("model prices list base-tier sale prices and expand to every tier", async ({ page }) => {
  await page.route("**/api/admin/admin/v1/billing/prices", route => route.fulfill({ json: { items: [{
    model: "claude-x", routed: true, source: "catalog", rate_ppm: 800_000,
    items: [
      { meter: "input", base_micro_per_m: 3_000_000, sale_micro_per_m: 2_400_000 },
      { meter: "input.cache_read", base_micro_per_m: 300_000, sale_micro_per_m: 240_000 },
      { meter: "output", base_micro_per_m: 15_000_000, sale_micro_per_m: 12_000_000 },
      { meter: "input", above_input_tokens: 200_000, base_micro_per_m: 6_000_000, sale_micro_per_m: 4_800_000 },
    ],
  }] } }));
  await page.goto("/console/billing?tab=prices");
  await expect(page.getByRole("tab", { name: "模型价格", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("columnheader").filter({ hasText: /^(输入|输出|缓存读取|缓存写入（5 分钟）)$/ })).toHaveText([
    "输入", "输出", "缓存读取", "缓存写入（5 分钟）",
  ]);
  await expect(page.getByText("倍率 0.8×", { exact: true })).toBeVisible();
  const modelRow = page.getByRole("row").filter({ hasText: "claude-x" });
  await expect(modelRow.getByRole("cell").nth(1)).toHaveText("2.4");
  await expect(modelRow.getByRole("cell").nth(2)).toHaveText("12");
  await page.getByRole("button", { name: "查看全部档位" }).click();
  await expect(page.getByText("输入超过 200,000 token", { exact: true })).toBeVisible();
  await expect(page.getByText("4.8", { exact: true })).toBeVisible();
});

test("usage, prices and the ledger fit a 320px screen without scrolling sideways", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 720 });
  await page.route(/\/api\/admin\/admin\/v1\/usage\/summary(\?|$)/, route => route.fulfill({ json: pricedSummary }));
  await page.route(/\/api\/admin\/admin\/v1\/usage\/records(\?|$)/, route => route.fulfill({ json: { items: [pricedRecord, ownNodeRecord], next_cursor: "" } }));
  for (const target of ["/console/usage", "/console/usage?tab=records", "/console/billing?tab=prices", "/console/billing?tab=ledger"]) {
    await page.goto(target);
    await page.waitForLoadState("networkidle");
    if (target.endsWith("records")) await page.getByRole("button", { name: "查看计费明细" }).first().click();
    expect(await page.evaluate(() => document.documentElement.scrollWidth), target).toBeLessThanOrEqual(321);
  }
});

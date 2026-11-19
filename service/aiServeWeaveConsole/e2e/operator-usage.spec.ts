import { test, expect } from "./fixtures.ts";

// The operator usage page folds one cross-tenant summary by tenant and by model.
// 运维用量页把一份跨租户汇总按租户与按模型折叠。
const row = (tenant_id: string, model: string, total: number, cost_micro: number) => ({
  tenant_id, model, prompt_tokens: total - 100, completion_tokens: 100, total_tokens: total, request_count: 2, cache_read_tokens: 10, cache_write_tokens: 0, cost_micro,
});
const summary = { items: [row("tnt_a", "model-x", 3000, 3_000_000), row("tnt_a", "model-y", 1000, 1_000_000), row("tnt_b", "model-x", 5000, 5_000_000)] };
const tenants = { items: [{ id: "tnt_a", name: "Alpha Studio", status: "active", created_at: "2026-11-01T00:00:00Z" }], next_cursor: "" };

test("operator usage ranks tenants by name, drills into one, and keeps it across windows", async ({ page }) => {
  const reads: string[] = [];
  await page.route(/\/api\/operator\/operator\/v1\/usage\/summary(\?|$)/, route => {
    reads.push(new URL(route.request().url()).search);
    return route.fulfill({ json: summary });
  });
  await page.route(/\/api\/operator\/operator\/v1\/tenants(\?|$)/, route => route.fulfill({ json: tenants }));
  await page.goto("/operator/usage");

  const rows = page.locator("tbody tr");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText("tnt_b", { useInnerText: true });
  await expect(rows.nth(0)).toContainText("55.6%");
  await expect(rows.nth(1)).toContainText("Alpha Studio");
  await expect(page.locator("tfoot")).toContainText("9,000");
  await expect(page.getByText("消耗点数", { exact: true }).first()).toBeVisible();

  await page.getByRole("button", { name: /Alpha Studio/ }).click();
  await expect(page.getByRole("tab")).toHaveCount(1);
  await expect(page.getByRole("combobox", { name: "租户" })).toContainText("Alpha Studio");
  await expect(page.locator('[data-slot="card"]').first()).toContainText("4,000");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText("model-x");
  await expect(rows.nth(0)).toContainText("75%");

  await page.getByRole("button", { name: "近 7 天", exact: true }).click();
  await expect.poll(() => reads.length).toBe(2);
  expect(reads[1]).toMatch(/^\?since=/);
  expect(reads.every(search => !search.includes("tenant_id"))).toBe(true);
  await expect(page.getByRole("combobox", { name: "租户" })).toContainText("Alpha Studio");

  await page.getByRole("button", { name: "清除", exact: true }).click();
  await expect(page.getByRole("tab")).toHaveCount(2);
});

test("operator usage hides credits with billing off and says when a window is empty", async ({ page }) => {
  let items: unknown[] = summary.items.map(entry => Object.fromEntries(Object.entries(entry).filter(([name]) => name !== "cost_micro")));
  await page.route(/\/api\/operator\/operator\/v1\/usage\/summary(\?|$)/, route => route.fulfill({ json: { items } }));
  await page.route(/\/api\/operator\/operator\/v1\/tenants(\?|$)/, route => route.fulfill({ status: 503, json: { error: "unavailable" } }));
  await page.goto("/operator/usage");
  await expect(page.locator("tbody tr").first()).toContainText("tnt_b");
  await expect(page.getByText("消耗点数")).toHaveCount(0);
  await expect(page.getByRole("button", { name: /tnt_a/ })).toBeVisible();
  items = [];
  await page.getByRole("button", { name: "近 30 天", exact: true }).click();
  await expect(page.getByText("所选条件下没有用量记录", { exact: true })).toBeVisible();
});

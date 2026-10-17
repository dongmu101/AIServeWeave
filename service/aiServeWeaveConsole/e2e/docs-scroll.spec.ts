import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures.ts";

const doc = {
  slug: "chat-completions", category: "文本对话", category_order: 10, sort_order: 10,
  title: "Chat Completions", method: "POST", path: "/v1/chat/completions",
  summary: "发送消息并获取模型回复。", updated_at: "2026-10-17T00:00:00Z",
  content: {
    auth: "bearer", body_type: "json",
    body_params: Array.from({ length: 40 }, (_, index) => ({
      name: `parameter_${index}`, type: "string", description: "用于验证长文档的连续滚动。",
    })),
    request_examples: [{ lang: "curl", label: "cURL", code: Array.from({ length: 80 }, (_, index) => `example line ${index}`).join("\n") }],
    response_examples: [{ status: 200, label: "成功", body: "{}" }],
  },
};

// pageOffset supports both document and main scrolling so the same check catches the old layout.
// pageOffset 同时支持整页与 main 滚动，使同一检查可以捕获旧布局的问题。
async function pageOffset(page: Page) {
  return page.evaluate(() => window.scrollY + (document.querySelector("main")?.scrollTop ?? 0));
}

test.beforeEach(async ({ page }) => {
  await page.route("**/api/admin/admin/v1/api-docs", route => route.fulfill({ json: { items: [doc] } }));
  await page.route("**/api/admin/admin/v1/api-docs/chat-completions", route => route.fulfill({ json: doc }));
});

for (const width of [1440, 1280, 1024, 390, 320]) {
  test(`docs keep the top menu stationary while scrolling at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/console/docs/chat-completions");
    await expect(page.getByRole("heading", { name: doc.title, exact: true })).toBeVisible();
    const menu = page.getByRole("banner");
    const before = await menu.boundingBox();
    expect(before?.height, "top menu must stay compact even with a long account name").toBe(56);
    const account = await menu.getByRole("button", { name: "账户菜单", exact: true }).boundingBox();
    expect(account).not.toBeNull();
    expect(account!.x + account!.width, "account menu must fit inside the viewport").toBeLessThanOrEqual(width);
    const parameter = page.getByText("parameter_0", { exact: true });
    await parameter.hover();
    const offset = await pageOffset(page);
    await page.mouse.wheel(0, 500);
    await expect.poll(() => pageOffset(page)).toBeGreaterThan(offset + 100);
    const after = await menu.boundingBox();
    expect(after?.y, "top menu must remain at its original position").toBe(before?.y);
    await expect(menu.getByRole("navigation", { name: "控制台导航" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);
    expect(await page.evaluate(() => document.documentElement.scrollHeight), "document must not add a second page scrollbar").toBeLessThanOrEqual(900);
    await page.screenshot({ path: test.info().outputPath(`docs-scroll-${width}.png`) });
  });
}

test("wheel over a long example scrolls the reference without a nested vertical scroll", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/console/docs/chat-completions");
  const example = page.getByRole("tabpanel", { name: "cURL", exact: true });
  await expect(example).toBeVisible();
  const box = await example.boundingBox();
  expect(box).not.toBeNull();
  await page.mouse.move(box!.x + 40, Math.max(box!.y + 20, 400));
  const offset = await pageOffset(page);
  await page.mouse.wheel(0, 300);
  await expect.poll(() => pageOffset(page), { message: "wheel over example must advance the reference" }).toBeGreaterThan(offset + 100);
  expect(await example.evaluate(element => element.scrollTop), "example must not consume vertical scrolling").toBe(0);
});

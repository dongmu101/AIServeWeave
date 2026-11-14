import { test, expect } from "./fixtures";

// The overview's error table, the client setup page and the per-page table of
// contents, against the compiled-in documents served by the fixtures.
// 总览页错误码表、客户端接入页与页内目录，使用 fixture 提供的编译内置文档。

test("overview lists every gateway status including 402 and links to client setup", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/console/docs");
  const table = page.getByRole("table");
  for (const status of ["400", "401", "402", "404", "409", "413", "429", "500", "501", "502", "503", "504"]) {
    await expect(table.getByRole("cell", { name: status, exact: true })).toBeVisible();
  }
  await expect(table).toContainText("insufficient_balance");
  await expect(table).toContainText("account_subscription_expired");
  await page.getByRole("tab", { name: "402", exact: true }).click();
  await expect(page.getByRole("tabpanel", { name: "402", exact: true })).toContainText("insufficient_quota");
  await page.getByRole("link", { name: /^客户端接入 Claude Code/ }).click();
  await expect(page).toHaveURL(/\/console\/docs\/clients$/);
  await expect(page.getByRole("heading", { level: 1, name: "客户端接入" })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "API 文档目录" }).getByRole("link", { name: "客户端接入" })).toHaveAttribute("aria-current", "page");
});

for (const width of [1440, 390, 320]) {
  test(`client setup fills the base URL and model into every example at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/console/docs/clients");
    await page.getByRole("textbox", { name: "示例模型" }).fill("guide-model");
    const claude = page.locator("#claude-code");
    await expect(claude.getByRole("tabpanel")).toContainText("ANTHROPIC_BASE_URL=http://127.0.0.1:1");
    await expect(claude.getByRole("tabpanel")).toContainText("ANTHROPIC_MODEL=guide-model");
    await expect(page.locator("#codex").getByRole("tabpanel")).toContainText('base_url = "http://127.0.0.1:1/v1"');
    await expect(page.getByRole("navigation", { name: "本页内容" }).getByRole("link")).toHaveCount(6);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);
  });
}

test("an endpoint page offers a table of contents whose links reach their sections", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/console/docs/chat-completions");
  const toc = page.getByRole("navigation", { name: "本页内容" });
  await expect(toc.getByRole("link", { name: "认证" })).toHaveAttribute("href", "#doc-auth");
  const last = toc.getByRole("link").last();
  const target = (await last.getAttribute("href"))!;
  await last.click();
  await expect(page).toHaveURL(new RegExp(`${target}$`));
  await expect(page.locator(target)).toBeInViewport();
});

test("placeholders in highlights, notices and parameter descriptions are filled in", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  for (const slug of ["messages", "ollama-chat", "images-generations"]) {
    await page.goto(`/console/docs/${slug}`);
    const article = page.locator("article");
    await expect(article.locator("#doc-auth")).toBeVisible();
    await expect(article).not.toContainText("{{");
  }
  await page.goto("/console/docs/messages");
  await expect(page.locator("article header")).toContainText("http://127.0.0.1:1");
});

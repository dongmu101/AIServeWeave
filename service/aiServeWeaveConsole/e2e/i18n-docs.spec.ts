import { test, expect, API_DOCS } from "./fixtures";

const headings = {
  "zh-CN": "下载产物", "zh-TW": "下載產物", "en-US": "Download artifact", "ja-JP": "成果物をダウンロード",
  "ko-KR": "결과물 다운로드", "de-DE": "Artefakt herunterladen", "fr-FR": "Télécharger un artefact", "es-ES": "Descargar artefacto",
};
for (const [locale, heading] of Object.entries(headings)) {
  test(`${locale} built-in documentation displays translated content and unchanged examples`, async ({ page }) => {
    await page.context().addCookies([{ name: "aisw_console_locale", value: locale, url: "http://127.0.0.1:3100" }]);
    await page.goto("/console/docs/artifact-download");
    await expect(page.locator("h1")).toHaveText(heading);
    await expect(page.getByRole("tabpanel").first()).toContainText("/v1/artifacts/");
    await expect(page.locator("article")).toContainText("artifact_id");
  });
}

test("switching documentation retains example inputs, the selected tab and the exact copied code", async ({ page }) => {
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.goto("/console/docs/chat-completions");
  await page.getByRole("textbox", { name: "示例模型" }).fill("author-model-原文");
  await page.getByRole("tab", { name: "Python", exact: true }).click();
  const selected = page.locator('[role="tabpanel"]').first();
  const code = await selected.textContent();
  await selected.locator("..").getByRole("button", { name: "复制", exact: true }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(code);
  await page.locator("[data-language-switcher]").selectOption("en-US");
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");
  await expect(page.locator('input[maxlength="128"]')).toHaveValue("author-model-原文");
  await expect(page.getByRole("tab", { name: "Python", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(selected).toHaveText(code!);
  await selected.locator("..").getByRole("button", { name: "Copy", exact: true }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(code);
});

test("edited server content is rendered intact despite a matching built-in slug", async ({ page }) => {
  const original = API_DOCS.find(doc => doc.slug === "artifact-download");
  await page.route("**/api/admin/admin/v1/api-docs/artifact-download", route => route.fulfill({ json: { ...original, title: "作者自定义标题", content: { ...original.content, notice: "作者自定义正文" } } }));
  await page.goto("/console/docs/artifact-download");
  await page.locator("[data-language-switcher]").selectOption("de-DE");
  await expect(page.locator("html")).toHaveAttribute("lang", "de-DE");
  await expect(page.locator("h1")).toHaveText("作者自定义标题");
  await expect(page.locator("article")).toContainText("作者自定义正文");
});

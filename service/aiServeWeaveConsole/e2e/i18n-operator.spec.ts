import { test, expect } from "./fixtures";
import { operatorEntries } from "./i18n-expectations";
import { LOCALES } from "../lib/i18n/locale.ts";

for (const locale of LOCALES) for (const entry of operatorEntries) {
  test(`${locale} operator ${entry.path} renders its own heading`, async ({ page }) => {
    await page.context().addCookies([{ name: "aisw_console_locale", value: locale, url: "http://127.0.0.1:3100" }]);
    await page.goto(entry.path);
    await expect(page.locator("html")).toHaveAttribute("lang", locale);
    await expect(page.locator("h1").first()).toHaveText(entry.headings[locale]);
  });
}

test("route drafts keep machine model values and the revision while the UI changes", async ({ page }) => {
  const writes: unknown[] = [];
  await page.route("**/api/operator/operator/v1/routes/validate", async route => {
    writes.push(route.request().postDataJSON());
    await route.fulfill({ json: { valid: true, digest: "e".repeat(64) } });
  });
  await page.goto("/operator/routes");
  const model = page.getByRole("textbox").first();
  await model.fill("synthetic-unsaved-route");
  await page.locator("[data-language-switcher]").selectOption("fr-FR");
  await expect(page.locator("html")).toHaveAttribute("lang", "fr-FR");
  await expect(model).toHaveValue("synthetic-unsaved-route");
  expect(writes).toEqual([]);
  await expect(page.locator("main h2").first()).toContainText("1");
});

for (const locale of ["de-DE", "fr-FR"] as const) {
  test(`${locale} operator navigation and long chart headings fit 375px`, async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 820 });
    await page.context().addCookies([{ name: "aisw_console_locale", value: locale, url: "http://127.0.0.1:3100" }]);
    for (const path of ["/operator/metrics", "/operator/usage"]) {
      await page.goto(path);
      await expect(page.locator("h1")).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(376);
    }
  });
}

test("template graph drafts and a replacement confirmation survive a locale change", async ({ page }) => {
  await page.goto("/operator/workflow-templates");
  await page.getByRole("button", { name: /P10_.* · v1/ }).click();
  const graph = page.locator("textarea").first();
  await expect(graph).toHaveValue(/synthetic input/);
  const raw = '{"1":{"class_type":"Text","inputs":{"text":"untranslated draft 用户内容"}}}';
  await graph.fill(raw);
  await page.getByRole("button", { name: "用当前版本替换草稿" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.locator("[data-language-switcher]").selectOption("de-DE");
  await expect(page.locator("html")).toHaveAttribute("lang", "de-DE");
  await expect(graph).toHaveValue(raw);
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByRole("dialog")).not.toContainText("替换本地草稿");
});

test("API document JSON remains author content when the editor UI changes", async ({ page }) => {
  await page.goto("/operator/api-docs");
  await page.getByRole("button", { name: /新建文档/ }).click();
  const editor = page.locator("textarea");
  const source = '{"title":"自定义 API 作者内容","example":"{{BASE_URL}}/v1/chat/completions"}';
  await editor.fill(source);
  await page.locator("[data-language-switcher]").selectOption("ja-JP");
  await expect(page.locator("html")).toHaveAttribute("lang", "ja-JP");
  await expect(editor).toHaveValue(source);
});

test("an open alert rule keeps draft inputs and selected machine enums", async ({ page }) => {
  await page.goto("/operator/alert-rules");
  await page.getByRole("button", { name: "创建规则" }).click();
  await page.locator("#rule-name").fill("synthetic-unsaved-rule");
  await page.locator("#rule-threshold").fill("17");
  await page.locator("[data-language-switcher]").selectOption("fr-FR");
  await expect(page.locator("html")).toHaveAttribute("lang", "fr-FR");
  await expect(page.locator("#rule-name")).toHaveValue("synthetic-unsaved-rule");
  await expect(page.locator("#rule-threshold")).toHaveValue("17");
  await expect(page.getByRole("dialog")).toBeVisible();
});

test("model pull drafts survive switching without triggering a pull or another read", async ({ page }) => {
  let reads = 0;
  await page.route("**/api/operator/operator/v1/nodes/synthetic-node/model-pulls", async route => {
    expect(route.request().method()).toBe("GET");
    reads++;
    await route.fulfill({ json: { pulls: [], replicas: [] } });
  });
  await page.goto("/operator/model-pulls");
  await page.locator("#node-id").fill("synthetic-node");
  await page.locator("#node-id").press("Enter");
  await page.locator("#pull-names").fill("local-allowlisted-model");
  await page.locator("[data-language-switcher]").selectOption("ko-KR");
  await expect(page.locator("html")).toHaveAttribute("lang", "ko-KR");
  await expect(page.locator("#pull-names")).toHaveValue("local-allowlisted-model");
  expect(reads).toBe(1);
});

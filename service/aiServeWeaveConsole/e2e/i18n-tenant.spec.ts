import { test, expect } from "./fixtures";
import { tenantEntries, saveText } from "./i18n-expectations";
import { LOCALES } from "../lib/i18n/locale.ts";

for (const locale of LOCALES) for (const entry of tenantEntries) {
  test(`${locale} tenant ${entry.path} renders its own heading`, async ({ page }) => {
    await page.context().addCookies([{ name: "aisw_console_locale", value: locale, url: "http://127.0.0.1:3100" }]);
    await page.goto(entry.path);
    await expect(page.locator("html")).toHaveAttribute("lang", locale);
    if (entry.headings) await expect(page.locator("h1")).toHaveText(entry.headings[locale]);
    else await expect(page).toHaveURL(/\/operator\/fleet$/);
    if (entry.path === "/console/settings") await expect(page.getByRole("button", { name: saveText[locale], exact: true })).toBeVisible();
  });
}

test("quota validation and gateway-key drafts follow language without changing input", async ({ page }) => {
  await page.goto("/console/quota");
  const input = page.locator('input[id="quota-requestsPerMinute"]');
  await input.fill("-1");
  await page.getByRole("button", { name: "保存全部三项", exact: true }).click();
  await expect(page.locator("main").getByRole("alert")).toContainText("只能填写非负整数");
  await page.locator("[data-language-switcher]").selectOption("de-DE");
  await expect(page.locator("html")).toHaveAttribute("lang", "de-DE");
  await expect(input).toHaveValue("-1");
  await expect(page.locator("main").getByRole("alert")).not.toContainText("只能填写非负整数");
  await page.goto("/console/settings");
  await page.locator("#gateway-key-input").fill("aisw_synthetic_unsaved_draft");
  await page.locator("[data-language-switcher]").selectOption("fr-FR");
  await expect(page.locator("html")).toHaveAttribute("lang", "fr-FR");
  await expect(page.locator("#gateway-key-input")).toHaveValue("aisw_synthetic_unsaved_draft");
});

test("a creation error and all user draft fields remain while the language changes", async ({ page }) => {
  await page.goto("/console/users");
  await page.getByRole("button", { name: "创建用户", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "创建", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("请填写邮箱");
  await dialog.locator('input[type="email"]').fill("draft@example.invalid");
  await page.evaluate(() => {
    const select = document.querySelector<HTMLSelectElement>("[data-language-switcher]")!;
    select.value = "en-US"; select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await expect(dialog.getByRole("alert")).toHaveText("Enter an email address.");
  await expect(dialog.locator('input[type="email"]')).toHaveValue("draft@example.invalid");
});

test("creating a key sends stable machine values and language changes preserve its one-time secret", async ({ page }) => {
  const writes: unknown[] = [];
  await page.route("**/api/admin/admin/v1/apikeys", async route => {
    if (route.request().method() !== "POST") return route.fallback();
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 201, json: {
      api_key: { id: "key_once", tenant_id: "tnt_p10", name: "synthetic key", display: "aisw_display_once", status: "active", created_by: "usr_p10", created_at: "2026-10-20T00:00:00Z" },
      key: "aisw_synthetic_once_only_secret",
    } });
  });
  await page.goto("/console/keys");
  await page.getByRole("button", { name: "创建 Key", exact: true }).click();
  await page.getByRole("dialog").getByRole("textbox", { name: "名称", exact: true }).fill("synthetic key");
  await page.getByRole("dialog").getByRole("button", { name: "创建", exact: true }).click();
  const secret = page.getByRole("dialog").getByText("aisw_synthetic_once_only_secret", { exact: true });
  await expect(secret).toBeVisible();
  await page.evaluate(() => {
    const select = document.querySelector<HTMLSelectElement>("[data-language-switcher]")!;
    select.value = "ja-JP"; select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await expect(page.locator("html")).toHaveAttribute("lang", "ja-JP");
  await expect(secret).toBeVisible();
  expect(writes).toEqual([{ name: "synthetic key", ttl_seconds: 90 * 24 * 60 * 60 }]);
});

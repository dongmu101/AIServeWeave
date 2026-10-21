import { test, expect } from "@playwright/test";
import { test as signedIn } from "./fixtures";

test("SSR isolates concurrent language requests and honours Chinese script", async ({ request }) => {
  const responses = await Promise.all([
    request.get("/login", { headers: { "Accept-Language": "en-US" } }),
    request.get("/login", { headers: { "Accept-Language": "ja-JP" } }),
    request.get("/login", { headers: { "Accept-Language": "zh-Hant-HK" } }),
  ]);
  for (const [index, locale] of ["en-US", "ja-JP", "zh-TW"].entries()) {
    expect(await responses[index].text()).toContain(`lang="${locale}"`);
  }
});

test("Japanese entry and switching preserve credentials and the URL", async ({ browser }) => {
  const context = await browser.newContext({ locale: "ja-JP" });
  const page = await context.newPage();
  await page.goto("/login?next=%2Fconsole%2Fkeys");
  await expect(page.locator("html")).toHaveAttribute("lang", "ja-JP");
  await expect(page.getByRole("button", { name: "ログイン", exact: true })).toBeVisible();
  await page.locator("#email").fill("draft@example.invalid");
  await page.locator("#password").fill("synthetic-draft-password");
  await page.locator("[data-language-switcher]").selectOption("de-DE");
  await expect(page.locator("html")).toHaveAttribute("lang", "de-DE");
  await expect(page.getByRole("button", { name: "Anmelden", exact: true })).toBeVisible();
  await expect(page.locator("#email")).toHaveValue("draft@example.invalid");
  await expect(page.locator("#password")).toHaveValue("synthetic-draft-password");
  expect(new URL(page.url()).searchParams.get("next")).toBe("/console/keys");
  const cookie = (await context.cookies()).find(item => item.name === "aisw_console_locale");
  expect(cookie).toMatchObject({ value: "de-DE", path: "/", sameSite: "Lax", httpOnly: false });
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("lang", "de-DE");
  await context.close();
});

for (const path of ["/login", "/signup", "/operator/login"]) {
  test(`${path} exposes a keyboard-accessible preference on a narrow viewport`, async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 760 });
    await page.goto(path);
    await page.locator("[data-language-switcher]").selectOption("fr-FR");
    await expect(page.locator("html")).toHaveAttribute("lang", "fr-FR");
    await expect(page.locator("[data-language-switcher]")).toHaveAccessibleName("Langue");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  });
}

signedIn("tenant and operator shells share display preference without changing identities", async ({ page }) => {
  await page.goto("/console/users");
  const identities = (await page.context().cookies()).filter(cookie => /session/.test(cookie.name));
  await page.locator("[data-language-switcher]").selectOption("ko-KR");
  await expect(page.locator("html")).toHaveAttribute("lang", "ko-KR");
  await page.goto("/operator/fleet");
  await expect(page.locator("html")).toHaveAttribute("lang", "ko-KR");
  await expect(page.locator("[data-language-switcher]")).toHaveValue("ko-KR");
  expect((await page.context().cookies()).filter(cookie => /session/.test(cookie.name))).toEqual(identities);
});

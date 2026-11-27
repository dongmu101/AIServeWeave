import { test, expect, LONG_TEXT } from "./fixtures.ts";

for (const width of [320, 1024]) {
  test(`compact navigation supports keyboard and page switching at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 600 });
    await page.goto("/console/keys");
    const trigger = page.getByRole("button", { name: "打开控制台导航", exact: true });
    await expect(trigger).toContainText("API Key");
    await trigger.focus();
    await page.keyboard.press("ArrowDown");
    const menu = page.getByRole("menu", { name: "打开控制台导航", exact: true });
    await expect(menu).toBeVisible();
    await expect(menu.locator('[aria-current="page"]')).toHaveText("API Key");
    await page.keyboard.press("Escape");
    await expect(menu).toHaveCount(0);
    await expect(trigger).toBeFocused();
    await trigger.click();
    await page.getByRole("menuitem", { name: "账户安全", exact: true }).click();
    await expect(page).toHaveURL(/\/console\/account$/);
    await expect(trigger).toContainText("账户安全");
    await expect(page.getByRole("menu")).toHaveCount(0);
    expect((await page.getByRole("banner").boundingBox())?.height).toBe(56);
  });
}

test("account details stay in the menu with keyboard dismissal and platform access", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 600 });
  await page.goto("/console/keys");
  const trigger = page.getByRole("button", { name: "账户菜单", exact: true });
  await expect(page.getByRole("banner").getByText(LONG_TEXT, { exact: true })).toHaveCount(0);
  await trigger.focus();
  await page.keyboard.press("Enter");
  const menu = page.getByRole("menu", { name: "账户菜单", exact: true });
  await expect(menu.getByText(LONG_TEXT, { exact: true })).toBeVisible();
  await expect(menu.getByText("tnt_p10", { exact: true })).toBeVisible();
  await expect(menu.getByRole("menuitem", { name: "平台运维", exact: true })).toHaveAttribute("href", "/operator");
  const bounds = await menu.boundingBox();
  expect(bounds?.x).toBeGreaterThanOrEqual(0);
  expect((bounds?.x ?? 0) + (bounds?.width ?? 0)).toBeLessThanOrEqual(320);
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
  await expect(trigger).toBeFocused();
});

test("account logout preserves its failure handling and never retries automatically", async ({ page }) => {
  let writes = 0;
  await page.route("**/api/session", route => {
    writes++;
    return route.fulfill({ status: 503, json: { error: "upstream_error" } });
  });
  await page.goto("/console/keys");
  await page.getByRole("button", { name: "账户菜单", exact: true }).click();
  const exit = page.getByRole("menuitem", { name: "退出", exact: true });
  await exit.click();
  await expect(exit).toBeEnabled();
  await expect(page.locator("[data-sonner-toast]")).toBeVisible();
  expect(writes, "failed logout must issue exactly one request").toBe(1);
  await expect(page).toHaveURL(/\/console\/keys$/);
});

test("desktop top navigation never scrolls vertically or clips labels", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/console");
  const nav = page.getByRole("navigation", { name: "控制台导航", exact: true });
  const link = nav.getByRole("link", { name: "概览", exact: true });
  await expect(link).toBeVisible();
  const scrollBefore = await nav.evaluate((el) => {
    const row = el.firstElementChild as HTMLElement;
    return {
      clientHeight: row.clientHeight,
      scrollHeight: row.scrollHeight,
      clientWidth: row.clientWidth,
      scrollWidth: row.scrollWidth,
      scrollTop: row.scrollTop,
      scrollLeft: row.scrollLeft,
      overflowY: getComputedStyle(row).overflowY,
    };
  });
  expect(scrollBefore.scrollHeight).toBeLessThanOrEqual(scrollBefore.clientHeight);
  expect(scrollBefore.scrollWidth).toBeLessThanOrEqual(scrollBefore.clientWidth);
  expect(scrollBefore.overflowY).toBe("visible");

  await link.hover();
  await page.mouse.wheel(0, 120);
  const afterWheel = await nav.evaluate((el) => {
    const row = el.firstElementChild as HTMLElement;
    return { scrollTop: row.scrollTop, scrollLeft: row.scrollLeft };
  });
  expect(afterWheel).toEqual({ scrollTop: 0, scrollLeft: 0 });

  const headerBox = await page.getByRole("banner").boundingBox();
  const linkBox = await link.boundingBox();
  expect(headerBox?.height).toBe(56);
  expect(linkBox!.y).toBeGreaterThanOrEqual(headerBox!.y);
  expect(linkBox!.y + linkBox!.height).toBeLessThanOrEqual(headerBox!.y + headerBox!.height);
});

test("desktop top navigation collapses instead of scrolling horizontally when labels overflow", async ({ page }) => {
  await page.context().addCookies([{ name: "aisw_console_locale", value: "de-DE", url: "http://127.0.0.1:3100" }]);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/console");
  const banner = page.getByRole("banner");
  const compact = banner.getByRole("button", { name: "Konsolennavigation öffnen", exact: true });
  await expect(compact).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(1281);
});

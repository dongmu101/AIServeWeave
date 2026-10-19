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

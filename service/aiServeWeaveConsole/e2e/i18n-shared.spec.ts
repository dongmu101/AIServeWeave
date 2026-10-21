import { test, expect } from "./fixtures";

test("an existing read error changes language without another business request", async ({ page }) => {
  let reads = 0;
  await page.route("**/api/admin/admin/v1/users*", async route => { reads++; await route.fulfill({ status: 403, json: { error: "private-upstream-text" } }); });
  await page.goto("/console/users");
  await expect(page.locator("main").getByRole("alert")).toContainText("当前角色没有执行该操作的权限");
  const before = reads;
  await page.locator("[data-language-switcher]").selectOption("de-DE");
  await expect(page.locator("html")).toHaveAttribute("lang", "de-DE");
  await expect(page.locator("main").getByRole("alert")).toContainText("Die aktuelle Rolle darf diese Aktion nicht ausführen.");
  await expect(page.locator("main").getByRole("alert")).not.toContainText("private-upstream-text");
  expect(reads).toBe(before);
});

test("an open confirmation retains its target and pending failure across a language change", async ({ page }) => {
  let writes = 0;
  await page.route("**/api/admin/admin/v1/apikeys/key_p10", async route => {
    writes++; await route.fulfill({ status: 403, json: { error: "private-upstream-text" } });
  });
  await page.goto("/console/keys");
  await page.getByRole("button", { name: "吊销", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "吊销", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("当前角色没有执行该操作的权限");
  await page.evaluate(() => {
    const select = document.querySelector<HTMLSelectElement>("[data-language-switcher]")!;
    select.value = "de-DE"; select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await expect(page.locator("html")).toHaveAttribute("lang", "de-DE");
  await expect(dialog.getByRole("button", { name: "Abbrechen", exact: true })).toBeVisible();
  await expect(dialog).toContainText("aisw_test12345");
  await expect(dialog.getByRole("alert")).toContainText("Die aktuelle Rolle darf diese Aktion nicht ausführen.");
  expect(writes).toBe(1);
});

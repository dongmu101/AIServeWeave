import { test, expect, LONG_TEXT } from "./fixtures.ts";
import type { Locator, Page } from "@playwright/test";

/** tabTo reaches a control through the real tab order, with a finite bound.
 * tabTo 按真实 Tab 顺序抵达控件，并限制尝试次数。 */
async function tabTo(page: Page, target: Locator) {
  for (let i = 0; i < 80; i++) {
    await page.keyboard.press("Tab");
    if (await target.evaluate(element => element === document.activeElement)) return;
  }
  await expect(target, "control must be reachable with Tab").toBeFocused();
}

const pages = [
  "/console", "/console/users", "/console/keys", "/console/audit", "/console/requests",
  "/console/quota", "/console/workflows", "/console/jobs", "/console/jobs/history",
  "/console/jobs/history/job_p10", "/console/settings", "/console/account",
  "/operator/fleet", "/operator/models", "/operator/routes", "/operator/workflow-templates",
  "/operator/workflows", "/operator/audit", "/operator/requests", "/operator/metrics",
  "/operator/operators", "/operator/account",
];

for (const width of [1440, 390, 320]) {
  for (const path of pages) {
    test(`${width}px ${path}: bounded layout and labelled controls`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(path);
      await expect(page.locator("main")).toBeVisible();
      await expect(page.getByRole("heading", { level: 1 }).first()).toBeVisible();
      if (path === "/operator/workflow-templates") {
        await page.getByRole("button", { name: `${LONG_TEXT} · v1`, exact: true }).click();
        await expect(page.getByRole("heading", { name: "当前发布：版本 1" })).toBeVisible();
      }
      if (path === "/operator/routes") {
        await expect(page.getByRole("heading", { name: "当前发布：版本 1" })).toBeVisible();
        await expect(page.getByLabel("模型别名", { exact: true })).toHaveValue(LONG_TEXT);
        await expect(page.getByLabel("运行时模型", { exact: true })).toHaveValue(LONG_TEXT);
      }
      await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
      await expect(page.locator("main").getByRole("alert")).toHaveCount(0);
      if (path === "/operator/fleet") {
        const clipped = await page.getByText(`region=${LONG_TEXT}`, { exact: true }).evaluate(element => {
          const bounds = element.getBoundingClientRect();
          const card = element.closest('[data-slot="card"]')!.getBoundingClientRect();
          return bounds.right > card.right || bounds.left < card.left || element.scrollHeight > element.clientHeight + 1 || element.scrollWidth > element.clientWidth + 1;
        });
        expect(clipped, "long node labels must stay readable inside the card").toBe(false);
      }
      const bounds = await page.evaluate(() => ({ width: innerWidth, scroll: document.documentElement.scrollWidth }));
      await info.attach("layout.json", { body: JSON.stringify(bounds), contentType: "application/json" });
      expect(bounds.scroll, "page width must remain inside the viewport; tables scroll locally").toBeLessThanOrEqual(width + 1);
      const unnamed = await page.locator('input:not([type="hidden"]), textarea, select, [role="combobox"]').evaluateAll(elements =>
        elements.filter(element => {
          if (!(element as HTMLElement).getClientRects().length || element.closest('[aria-hidden="true"]')) return false;
          const ids = element.getAttribute("aria-labelledby")?.split(/\s+/) ?? [];
          const labels = "labels" in element ? (element as HTMLInputElement).labels : null;
          return !element.getAttribute("aria-label") && !ids.some(id => document.getElementById(id)?.textContent?.trim()) && !labels?.length && !element.closest("label") && !document.querySelector(`label[for="${CSS.escape(element.id)}"]`);
        }).map(element => element.outerHTML.slice(0, 220)));
      expect(unnamed, "visible inputs need a programmatic label").toEqual([]);
      if (width === 390 && ["/console/users", "/operator/workflow-templates", "/operator/metrics"].includes(path)) {
        const screenshot = info.outputPath("narrow-screen.png");
        await page.screenshot({ path: screenshot, fullPage: true });
        await info.attach("narrow-screen", { path: screenshot, contentType: "image/png" });
      }
    });
  }
}

for (const path of ["/login", "/operator/login"]) {
  test(`${path}: narrow login form supports keyboard and readable authentication failure`, async ({ page }) => {
    await page.context().clearCookies();
    await page.setViewportSize({ width: 320, height: 600 });
    await page.route(/\/api\/(operator-session|session)$/, route => route.fulfill({ status: 401, json: { error: "unauthorized" } }));
    await page.goto(path);
    const form = page.locator("form");
    const fields = form.locator('input:not([type="hidden"])');
    for (const field of await fields.all()) await expect(field).toHaveAccessibleName(/.+/);
    await fields.first().fill("p10@example.invalid");
    await fields.last().fill("synthetic-password");
    const submit = form.locator('[type="submit"]');
    await tabTo(page, submit);
    await page.keyboard.press("Enter");
    await expect(form.getByRole("alert")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(321);
  });
}

test("read failure shows a retry action and recovers without an empty-success state", async ({ page }) => {
  let unavailable = true;
  await page.route("**/api/admin/admin/v1/audit?**", async route => {
    if (unavailable) await route.fulfill({ status: 503, json: { error: "upstream_error" } });
    else await route.fallback();
  });
  await page.goto("/console/audit");
  const error = page.locator("main").getByRole("alert");
  await expect(error).toContainText(/稍后|不可用|失败/);
  await expect(page.getByRole("table")).toHaveCount(0);
  unavailable = false;
  await tabTo(page, error.getByRole("button", { name: "重试", exact: true }));
  await page.keyboard.press("Enter");
  await expect(page.getByText("第 1 页 · 本页 100 条", { exact: false })).toBeVisible();
  await expect(error).toHaveCount(0);
});

test("keyboard closes create and revoke dialogs and restores their opener", async ({ page }) => {
  await page.goto("/console/keys");
  const create = page.getByRole("button", { name: "创建 Key", exact: true });
  await tabTo(page, create);
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog.getByLabel("名称", { exact: true })).toBeFocused();
  for (let i = 0; i < 10; i++) {
    await page.keyboard.press("Tab");
    await expect.poll(() => dialog.evaluate(element => element.contains(document.activeElement)), { message: "Tab stays inside the modal" }).toBe(true);
  }
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(create).toBeFocused();
  const revoke = page.getByRole("button", { name: "吊销", exact: true }).first();
  await tabTo(page, revoke);
  await page.keyboard.press("Enter");
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(revoke).toBeFocused();
});

test("short viewport keeps a create-user dialog reachable by keyboard", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 480 });
  await page.goto("/console/users");
  const create = page.getByRole("button", { name: "创建用户", exact: true });
  await tabTo(page, create);
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  const bounds = await dialog.boundingBox();
  expect(bounds?.y).toBeGreaterThanOrEqual(0);
  expect((bounds?.y ?? 0) + (bounds?.height ?? 0)).toBeLessThanOrEqual(480);
  await page.keyboard.press("Escape");
  await expect(create).toBeFocused();
});

test("navigation marks only the current tenant page", async ({ page }) => {
  await page.goto("/console/jobs/history");
  const current = page.getByRole("navigation", { name: "控制台导航" }).locator('[aria-current="page"]');
  await expect(current).toHaveCount(1);
  await expect(current).toHaveText("运行历史");
});

test("creation error is readable, retry succeeds, and the one-time dialog restores focus", async ({ page }) => {
  let writes = 0;
  const plaintext = "synthetic-browser-test-key-never-a-real-credential";
  await page.route("**/api/admin/admin/v1/apikeys", async route => {
    if (route.request().method() !== "POST") return route.fallback();
    writes++;
    if (writes === 1) return route.fulfill({ status: 503, json: { error: "upstream_error" } });
    return route.fulfill({ status: 201, json: { key: plaintext, api_key: { id: "key_new", tenant_id: "tnt_p10", name: "验收", display: "aisw_test12345", status: "active", created_by: "usr_p10", created_at: "2026-09-12T00:00:00Z" } } });
  });
  await page.setViewportSize({ width: 390, height: 600 });
  await page.goto("/console/keys");
  const create = page.getByRole("button", { name: "创建 Key", exact: true });
  await tabTo(page, create);
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("名称", { exact: true }).fill("验收");
  await tabTo(page, dialog.getByRole("button", { name: "创建", exact: true }));
  await page.keyboard.press("Enter");
  await expect(dialog.getByRole("alert")).toContainText(/稍后|不可用|失败/);
  expect(writes, "a failed write is sent exactly once").toBe(1);
  await tabTo(page, dialog.getByRole("button", { name: "创建", exact: true }));
  await page.keyboard.press("Enter");
  await expect(dialog.getByRole("button", { name: "我已保存，关闭" })).toBeVisible();
  await tabTo(page, dialog.getByRole("button", { name: "我已保存，关闭" }));
  await page.keyboard.press("Enter");
  await expect(dialog).toHaveCount(0);
  await expect(create).toBeFocused();
  await expect(page.getByText(plaintext, { exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => [localStorage.length, sessionStorage.length])).toEqual([0, 0]);
  expect(writes).toBe(2);
});

for (const path of ["/console/audit", "/operator/requests"]) {
  test(`${path}: 10000 records stay paged, virtualized and bounded in memory`, async ({ page }, info) => {
    test.setTimeout(90_000);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`${path}?size=200`);
    await expect(page.getByText("第 1 页 · 本页 200 条", { exact: false })).toBeVisible();
    const cdp = await page.context().newCDPSession(page);
    const samples: { page: number; heap: number; rows: number }[] = [];
    try {
      for (let number = 1; number <= 50; number++) {
        await expect(page.getByText(`第 ${number} 页 · 本页 200 条`, { exact: false })).toBeVisible();
        const count = await page.locator('[role="row"][data-index]').count();
        expect(count, "DOM contains visible rows plus bounded overscan").toBeGreaterThan(0);
        expect(count).toBeLessThan(55);
        if ([5, 25, 50].includes(number)) {
          await cdp.send("HeapProfiler.collectGarbage");
          const usage = await cdp.send("Runtime.getHeapUsage");
          samples.push({ page: number, heap: usage.usedSize, rows: count });
        }
        if (number < 50) await page.getByRole("button", { name: "下一页", exact: true }).click();
      }
      await expect(page.getByRole("button", { name: "下一页", exact: true })).toBeDisabled();
      const scroll = page.locator('[tabindex="0"][aria-label$="可滚动"]');
      await scroll.focus();
      await page.keyboard.press("End");
      await expect(page.locator('[role="row"][data-index="199"]')).toBeVisible();
      expect(samples.at(-1)!.heap - samples[0].heap, "retained heap growth after 45 additional pages stays below 8 MiB").toBeLessThan(8 * 1024 * 1024);
      await info.attach("memory.json", { body: JSON.stringify({ records: 10_000, pageSize: 200, samples }), contentType: "application/json" });
      if (path.endsWith("audit")) {
        await page.getByLabel("操作者 ID", { exact: true }).fill("actor_9999");
        await expect(page.getByText("第 1 页 · 本页 1 条", { exact: false })).toBeVisible();
        await expect(page.getByRole("cell", { name: "actor_9999", exact: true })).toBeVisible();
      }
    } finally { await cdp.detach(); }
  });
}

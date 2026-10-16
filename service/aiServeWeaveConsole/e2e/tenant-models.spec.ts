import { test, expect } from "./fixtures.ts";
import { OPERATOR_COOKIE } from "../lib/console/operator-session.ts";

const catalog = { collected_at: "2026-10-15T00:00:00Z", partial: false, services: [
  { agent_id: "agent-01", service_id: "ollama-11434", kind: "ollama", node_online: true, state: "healthy", version: "1.0", models: [
    { id: "tenant-agent-model", capabilities: { chat: "supported" } },
    { id: "second-model", capabilities: { chat: "supported" } },
    { id: "third-model", capabilities: { chat: "supported" } },
    { id: "fourth-model-with-a-complete-long-model-id", capabilities: { chat: "supported" } },
  ] },
] };

for (const width of [1440, 390, 320]) {
  test(`tenant sees Agent-reported models without platform login at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const cookies = await page.context().cookies();
    await page.context().clearCookies();
    await page.context().addCookies(cookies.filter(cookie => cookie.name !== OPERATOR_COOKIE));
    await page.route("**/api/admin/admin/v1/models", route => route.fulfill({ json: catalog }));
    await page.goto("/console");
    await page.getByRole("navigation", { name: "控制台导航" }).getByRole("link", { name: "AI 服务与模型", exact: true }).click();
    await expect(page).toHaveURL(/\/console\/models$/);
    await expect(page.getByRole("heading", { name: "Agent AI 服务与模型", exact: true })).toBeVisible();
    await expect(page.getByText("4 个模型", { exact: true })).toBeVisible();
    await expect(page.getByText("tenant-agent-model", { exact: true })).toHaveCount(0);
    await expect(page.getByText("Agent 在线", { exact: true })).toBeVisible();
    await expect(page.getByRole("table", { name: "Agent AI 服务列表" })).toBeVisible();
    await expect(page.getByText("agent-01", { exact: true })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Gateway 本机 AI 服务", exact: true })).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);
    await page.screenshot({ path: test.info().outputPath(`tenant-models-${width}.png`), fullPage: true });
    await page.getByRole("button", { name: "查看模型", exact: true }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(page.getByRole("dialog").getByText("chat · 支持", { exact: true })).toHaveCount(4);
    await page.keyboard.press("Escape");
    await page.getByRole("textbox", { name: "搜索 AI 服务或模型" }).fill("missing-model");
    await expect(page.getByText("没有符合搜索条件的服务或模型", { exact: true })).toBeVisible();
  });
}

test("tenant model refresh reports failure and retry restores models", async ({ page }) => {
  let partial = false;
  await page.route("**/api/admin/admin/v1/models", route => route.fulfill({ json: partial ? { ...catalog, partial: true, services: [] } : catalog }));
  await page.goto("/console/models");
  await expect(page.getByText("4 个模型", { exact: true })).toBeVisible();
  partial = true;
  await page.getByRole("button", { name: "刷新服务与模型", exact: true }).click();
  await expect(page.locator("main").getByRole("alert")).toContainText("暂时无法读取 Agent AI 服务与模型");
  await expect(page.getByText("4 个模型", { exact: true })).toHaveCount(0);
  partial = false;
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await expect(page.getByText("4 个模型", { exact: true })).toBeVisible();
});

test("offline Agent reports remain distinguishable from live services", async ({ page }) => {
  await page.route("**/api/admin/admin/v1/models", route => route.fulfill({ json: {
    ...catalog, services: [{ ...catalog.services[0], node_online: false }],
  } }));
  await page.goto("/console/models");
  await expect(page.getByText("Agent 离线", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "查看模型", exact: true }).click();
  await expect(page.getByText("Agent 已离线，以下为最近一次上报的服务与模型信息。", { exact: true })).toBeVisible();
  await expect(page.getByRole("dialog").getByText("tenant-agent-model", { exact: true })).toBeVisible();
});

test("many Agents use bounded service and model pages with full-snapshot search", async ({ page }) => {
  await page.route("**/api/admin/admin/v1/models", route => route.fulfill({ json: {
    ...catalog, services: Array.from({ length: 1000 }, (_, index) => ({
      ...catalog.services[0], agent_id: `agent-${String(index).padStart(4, "0")}`, node_online: index % 2 === 0,
      models: Array.from({ length: 60 }, (_, model) => ({ id: `model-${model}`, capabilities: { chat: "supported" } })),
    })),
  } }));
  await page.goto("/console/models");
  const table = page.getByRole("table", { name: "Agent AI 服务列表" });
  await expect(table.locator(":scope > tbody > tr")).toHaveCount(25);
  await page.getByRole("navigation", { name: "Agent 分页" }).getByRole("button", { name: "下一页" }).click();
  await expect(table.getByText("agent-0025", { exact: true })).toBeVisible();
  const search = page.getByRole("textbox", { name: "搜索 AI 服务或模型" });
  await search.fill("agent-0999");
  await expect(table.locator(":scope > tbody > tr")).toHaveCount(1);
  await page.getByRole("button", { name: "查看模型", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("listitem")).toHaveCount(25);
  await dialog.getByRole("navigation", { name: "模型分页" }).getByRole("button", { name: "下一页" }).click();
  await expect(dialog.getByText("model-25", { exact: true })).toBeVisible();
  await dialog.getByRole("textbox", { name: "搜索服务内模型" }).fill("model-59");
  await expect(dialog.getByRole("listitem")).toHaveCount(1);
  await expect(dialog.getByText("model-59", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await search.fill("model-59");
  await expect(table.locator(":scope > tbody > tr")).toHaveCount(25);
  await expect(table.getByText("model-59", { exact: true })).toHaveCount(0);
  await search.fill("agent-0999");
  await page.getByLabel("仅显示在线 Agent").check();
  await expect(page.getByText("没有符合搜索条件的服务或模型", { exact: true })).toBeVisible();
});

for (const width of [1440, 390, 320]) {
  test(`one parent row per Agent with service subtables at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route("**/api/admin/admin/v1/models", route => route.fulfill({ json: {
      ...catalog, services: [catalog.services[0],
        { ...catalog.services[0], service_id: "vllm-8000", kind: "vllm", models: [{ id: "second-service-model", capabilities: {} }] },
        { ...catalog.services[0], agent_id: "agent-02", service_id: "ollama-remote", models: [] },
      ],
    } }));
    await page.goto("/console/models");
    const parent = page.getByRole("table", { name: "Agent AI 服务列表", exact: true });
    await expect(parent.locator(":scope > tbody > tr")).toHaveCount(2);
    await expect(parent.getByText("agent-01", { exact: true })).toHaveCount(1);
    const child = page.getByRole("table", { name: "agent-01 的 AI 服务", exact: true });
    await expect(child.locator(":scope > tbody > tr")).toHaveCount(2);
    await expect(child.getByText("ollama-11434", { exact: true })).toBeVisible();
    await expect(child.getByText("vllm-8000", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);
    await page.screenshot({ path: test.info().outputPath(`agent-subtables-${width}.png`), fullPage: true });
    await page.getByRole("textbox", { name: "搜索 AI 服务或模型" }).fill("second-service-model");
    await expect(parent.locator(":scope > tbody > tr")).toHaveCount(1);
    await expect(child.locator(":scope > tbody > tr")).toHaveCount(1);
    await expect(child.getByText("vllm-8000", { exact: true })).toBeVisible();
  });
}

test("one Agent's service pagination never duplicates its parent row", async ({ page }) => {
  await page.route("**/api/admin/admin/v1/models", route => route.fulfill({ json: {
    ...catalog, services: Array.from({ length: 30 }, (_, index) => ({ ...catalog.services[0], service_id: `service-${index}` })),
  } }));
  await page.goto("/console/models");
  const parent = page.getByRole("table", { name: "Agent AI 服务列表", exact: true });
  const child = page.getByRole("table", { name: "agent-01 的 AI 服务", exact: true });
  await expect(parent.locator(":scope > tbody > tr")).toHaveCount(1);
  await expect(child.locator(":scope > tbody > tr")).toHaveCount(25);
  await page.getByRole("navigation", { name: "agent-01 的服务分页" }).getByRole("button", { name: "下一页" }).click();
  await expect(parent.locator(":scope > tbody > tr")).toHaveCount(1);
  await expect(child.locator(":scope > tbody > tr")).toHaveCount(5);
  await expect(child.getByText("service-25", { exact: true })).toBeVisible();
});

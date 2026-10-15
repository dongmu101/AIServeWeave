import { test, expect, LONG_TEXT } from "./fixtures.ts";
import { OPERATOR_COOKIE } from "../lib/console/operator-session.ts";

const at = "2026-10-15T00:00:00Z";
const runtime = {
  id: "ollama-local", kind: "ollama", base_url: `http://127.0.0.1:11434/${LONG_TEXT}`,
  state: "healthy", version: "test-version", identity_verified: true,
  checked_at: at, discovered_at: at, latency_millis: 12,
  models: [{ id: "gemma-local", capabilities: { chat: "supported", tools: "unknown" } }],
};
const snapshot = {
  collected_at: at, partial: false, replicas: [{ endpoint: "http://gateway-local:8091", replica_id: "gateway-local", generated_at: at, node_count: 3 }],
  nodes: [
    { node_id: "local-mac", agent_version: "test-agent", live: true, maintenance: false, last_heartbeat: at, observed_at: at, replicas: ["gateway-local"], runtimes: [runtime] },
    { node_id: "offline-node", live: false, maintenance: false, observed_at: at, runtimes: [{ ...runtime, id: "cached-backend", models: [{ id: "cached-model", capabilities: {} }] }] },
    { node_id: "pending-node", live: true, maintenance: false, observed_at: at, runtimes: [], declared_runtime_ids: ["pending-ollama"] },
  ],
};

for (const width of [1440, 390, 320]) {
  test(`AI connections ${width}px: node health, backend metadata, models and filtering`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route("**/api/operator/operator/v1/nodes?**", route => {
      expect(new URL(route.request().url()).searchParams.get("gateway")).toBe("default");
      return route.fulfill({ json: snapshot });
    });
    await page.goto("/operator/models");
    await expect(page.getByRole("heading", { name: "当前 Gateway 的本地 AI 模型" })).toBeVisible();
    await expect(page.getByText("gemma-local", { exact: true })).toBeVisible();
    await expect(page.getByText("chat · 支持", { exact: true })).toBeVisible();
    await expect(page.getByText("节点已离线，下方后端与模型信息为最近一次上报。")).toBeVisible();
    await expect(page.getByText("test-agent", { exact: true })).toBeVisible();
    await expect(page.getByText("test-version", { exact: true })).toHaveCount(2);
    await expect(page.getByRole("navigation", { name: "平台运维导航" }).getByRole("link", { name: "模型", exact: true })).toHaveAttribute("aria-current", "page");
    await expect(page.getByText("已声明：pending-ollama", { exact: false })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);
    await page.screenshot({ path: test.info().outputPath(`connections-${width}.png`), fullPage: true });

    await page.getByLabel("仅显示在线节点").check();
    await expect(page.getByText("cached-model", { exact: true })).toHaveCount(0);
    const search = page.getByRole("textbox", { name: "筛选节点、后端或模型" });
    await search.fill("gemma-local");
    await expect(page.getByText("显示 1 / 已采集 3 个节点", { exact: true })).toBeVisible();
    await search.fill("absent-model");
    await expect(page.getByText("没有符合筛选条件的节点", { exact: true })).toBeVisible();
  });
}

test("AI connections: refresh changes health, preserves filters and reports partial collection", async ({ page }) => {
  let changed = false;
  await page.route("**/api/operator/operator/v1/nodes?**", route => route.fulfill({ json: changed ? {
    ...snapshot, partial: true,
    replicas: [{ endpoint: "http://missing-gateway.invalid", error: "timeout" }],
    nodes: [{ ...snapshot.nodes[0], runtimes: [{ ...runtime, state: "unhealthy", models: [] }] }],
  } : snapshot }));
  await page.goto("/operator/models");
  await expect(page.getByText("gemma-local", { exact: true })).toBeVisible();
  const search = page.getByRole("textbox", { name: "筛选节点、后端或模型" });
  await search.fill("local-mac");
  changed = true;
  await page.getByRole("button", { name: "刷新连接与模型", exact: true }).click();
  await expect(page.getByText("gemma-local", { exact: true })).toHaveCount(0);
  await expect(page.locator("main").getByRole("alert")).toContainText("无法读取当前 Gateway");
  await expect(page.getByText("异常", { exact: true })).toHaveCount(0);
  await expect(page.getByText("local-mac", { exact: true })).toHaveCount(0);
  changed = false;
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await expect(search).toHaveValue("local-mac");
  await expect(page.getByText("显示 1 / 已采集 3 个节点", { exact: true })).toBeVisible();
});

test("AI connections: failed collection clears old results and retry recovers", async ({ page }) => {
  let failed = false;
  await page.route("**/api/operator/operator/v1/nodes?**", route => route.fulfill(failed ? { status: 503, json: { error: "upstream_error" } } : { json: snapshot }));
  await page.goto("/operator/models");
  await expect(page.getByText("gemma-local", { exact: true })).toBeVisible();
  failed = true;
  await page.getByRole("button", { name: "刷新连接与模型", exact: true }).click();
  await expect(page.locator("main").getByRole("alert")).toBeVisible();
  await expect(page.getByText("gemma-local", { exact: true })).toHaveCount(0);
  failed = false;
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await expect(page.getByText("gemma-local", { exact: true })).toBeVisible();
});

test("AI connections: existing model page requires a separate platform session", async ({ page }) => {
  const cookies = await page.context().cookies();
  await page.context().clearCookies();
  await page.context().addCookies(cookies.filter(cookie => cookie.name !== OPERATOR_COOKIE));
  await page.goto("/operator/models");
  await expect(page).toHaveURL(/\/operator\/login\?next=%2Foperator%2Fmodels/);
});

test("Gateway local services are visible without any Agent connections", async ({ page }) => {
  await page.route("**/api/operator/operator/v1/nodes?**", route => route.fulfill({ json: {
    ...snapshot, nodes: [], replicas: [{ ...snapshot.replicas[0], node_count: 0, local_runtimes: [
      { ...runtime, id: "gateway-ollama", models: [{ id: "gateway-only-model", capabilities: { chat: "supported" } }] },
    ] }],
  } }));
  await page.goto("/operator/models");
  await expect(page.getByText("Gateway 本机 AI 服务", { exact: true })).toBeVisible();
  await expect(page.getByText("gateway-only-model", { exact: true })).toBeVisible();
  await expect(page.getByText("test-version", { exact: true })).toBeVisible();
  await expect(page.getByText("当前 Gateway 尚无 AI 节点连接", { exact: true })).toBeVisible();
  await page.getByRole("textbox", { name: "筛选节点、后端或模型" }).fill("missing-local-model");
  await expect(page.getByText("gateway-only-model", { exact: true })).toHaveCount(0);
  await expect(page.getByText("没有符合搜索条件的本机 AI 服务。", { exact: true })).toBeVisible();
});

test("AI connections: automatic refresh updates the snapshot without waiting for wall time", async ({ page }) => {
  let reads = 0;
  await page.clock.install({ time: new Date("2026-10-15T00:00:00Z") });
  await page.clock.pauseAt(new Date("2026-10-15T00:00:01Z"));
  await page.route("**/api/operator/operator/v1/nodes?**", route => {
    reads++;
    return route.fulfill({ json: reads === 1 ? snapshot : { ...snapshot, nodes: [] } });
  });
  await page.goto("/operator/models");
  await expect(page.getByText("gemma-local", { exact: true })).toBeVisible();
  await page.clock.runFor(15_000);
  await expect(page.getByText("当前 Gateway 尚无 AI 节点连接", { exact: true })).toBeVisible();
  expect(reads).toBe(2);
});

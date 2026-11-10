import { test, expect } from "./fixtures.ts";

const at = "2026-11-09T00:00:00Z";

test("operator enrollment explicitly opts into sharing", async ({ page }) => {
  const writes: unknown[] = [];
  await page.route("**/api/operator/operator/v1/agent-install-codes", async route => {
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 503, json: { error: "unavailable" } });
  });
  await page.route("**/api/operator/operator/v1/agent-enrollment", route => route.fulfill({ json: { ca_pem: "-----BEGIN CERTIFICATE-----\nY2VydA==\n-----END CERTIFICATE-----\n", ca_sha256: "a".repeat(64) } }));
  await page.goto("/operator/agent-enrollment");
  const shared = page.getByRole("checkbox", { name: "共享给所有租户", exact: true });
  await expect(shared).not.toBeChecked();
  await page.getByLabel("Registry 注册地址").fill("registry.example.com:9443");
  await page.getByLabel("Gateway 隧道地址").fill("agent.example.com:8443");
  await shared.check();
  await page.locator('form').filter({ has: page.locator('#install-os') }).getByRole("button", { name: "生成命令", exact: true }).click();
  await expect.poll(() => writes.length).toBe(1);
  expect(writes[0]).toMatchObject({ shared: true, registry_addr: "registry.example.com:9443", gateway_addr: "agent.example.com:8443" });
});

for (const sharedChoice of [false, true]) {
test(`operator approval records explicit sharing choice: ${sharedChoice}`, async ({ page }) => {
  const writes: unknown[] = [];
  await page.route("**/api/operator/operator/v1/nodes/states", route => route.fulfill({ json: { items: [
    { node_id: "agent-private", pending_approval: true, disabled: false, maintenance: false, first_seen_at: at, last_seen_at: at },
  ] } }));
  await page.route("**/api/operator/operator/v1/nodes/agent-private/approve", async route => {
    writes.push(route.request().postDataJSON());
    await route.fulfill({ status: 204 });
  });
  await page.goto("/operator/fleet");
  await page.getByRole("button", { name: "审批通过", exact: true }).click();
  const dialog = page.getByRole("dialog");
  const shared = dialog.getByRole("checkbox", { name: "共享给所有租户", exact: true });
  await expect(shared).not.toBeChecked();
  if (sharedChoice) await shared.check();
  await dialog.getByRole("button", { name: "确认执行", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(writes).toEqual([{ shared: sharedChoice }]);
});
}

test("operator changes an Agent from private to shared and back", async ({ page }) => {
  let mode = "private";
  const writes: unknown[] = [];
  await page.route("**/api/operator/operator/v1/node-access", route => route.fulfill({ json: { items: [{ node_id: "agent-private", mode, tenants: [], updated_at: at, updated_by: "operator" }] } }));
  await page.route("**/api/operator/operator/v1/node-access/agent-private", async route => {
    const body = route.request().postDataJSON();
    writes.push(body); mode = body.mode;
    await route.fulfill({ json: { node_id: "agent-private", mode, tenants: [], updated_at: at, updated_by: "operator" } });
  });
  await page.route("**/api/operator/operator/v1/tenants?*", route => route.fulfill({ json: { items: [], next_cursor: "" } }));
  page.on("dialog", dialog => dialog.accept());
  await page.goto("/operator/node-access");
  await page.getByRole("button").filter({ hasText: "agent-private" }).click();
  const modes = page.getByRole("combobox", { name: "访问方式", exact: true });
  await expect(modes).toHaveValue("private");
  await modes.selectOption("shared");
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect.poll(() => writes.length).toBe(1);
  expect(writes[0]).toEqual({ mode: "shared", tenants: [] });
  await modes.selectOption("private");
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect.poll(() => writes.length).toBe(2);
  expect(writes[1]).toEqual({ mode: "private", tenants: [] });
});

test("fleet node buttons enable and stop sharing after confirmation", async ({ page }) => {
  let mode = "private";
  const writes: unknown[] = [];
  const ownedId = `tnode.tenant.${"a".repeat(32)}`;
  await page.route("**/api/operator/operator/v1/nodes/states", route => route.fulfill({ json: { items: ["agent-inline", ownedId].map(node_id => ({ node_id, pending_approval: false, disabled: false, maintenance: false, first_seen_at: at, last_seen_at: at })) } }));
  await page.route("**/api/operator/operator/v1/node-access", route => route.fulfill({ json: { items: [{ node_id: "agent-inline", mode, tenants: [] }] } }));
  await page.route("**/api/operator/operator/v1/node-access/agent-inline", async route => {
    const body = route.request().postDataJSON();
    writes.push(body); mode = body.mode;
    await route.fulfill({ json: { node_id: "agent-inline", mode, tenants: [] } });
  });
  await page.goto("/operator/fleet");
  await expect(page.getByRole("button", { name: "开启共享", exact: true })).toHaveCount(1);
  await page.getByRole("button", { name: "开启共享", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("agent-inline");
  expect(writes).toHaveLength(0);
  await dialog.getByRole("button", { name: "确认执行", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await page.getByRole("button", { name: "停止共享", exact: true }).click();
  await expect(dialog).toContainText("有效的独享账号权益仍保留");
  await dialog.getByRole("button", { name: "确认执行", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("button", { name: "开启共享", exact: true })).toBeEnabled();
  expect(writes).toEqual([{ mode: "shared", tenants: [] }, { mode: "private", tenants: [] }]);
  await expect(page.getByText("共享状态已保存，正在同步至各 Gateway。", { exact: true })).toBeVisible();
});

test("fleet sharing warns before replacing dedicated bindings and never retries a write automatically", async ({ page }) => {
  let writes = 0;
  await page.route("**/api/operator/operator/v1/nodes/states", route => route.fulfill({ json: { items: [
    { node_id: "agent-dedicated", pending_approval: false, disabled: false, maintenance: false, first_seen_at: at, last_seen_at: at },
  ] } }));
  await page.route("**/api/operator/operator/v1/node-access", route => route.fulfill({ json: { items: [{ node_id: "agent-dedicated", mode: "dedicated", tenants: ["tenant"] }] } }));
  await page.route("**/api/operator/operator/v1/node-access/agent-dedicated", async route => {
    writes++;
    await route.fulfill({ status: 503, json: { error: "private diagnostic" } });
  });
  await page.goto("/operator/fleet");
  await page.getByRole("button", { name: "开启共享", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("开启共享会替换当前的指定租户绑定。");
  await dialog.getByRole("button", { name: "确认执行", exact: true }).click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  expect(writes).toBe(1);
  await expect(dialog).not.toContainText("private diagnostic");
});

test("fleet sharing stays disabled when the current sharing policy cannot load", async ({ page }) => {
  await page.route("**/api/operator/operator/v1/node-access", route => route.fulfill({ status: 404, json: { error: "not_found" } }));
  await page.goto("/operator/fleet");
  await expect(page.getByRole("button", { name: "开启共享", exact: true })).toBeDisabled();
});

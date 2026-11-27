import { test, expect } from "./fixtures.ts";

const at = "2026-11-27T00:00:00Z";
const savedS3 = {
  kind: "s3", s3: { endpoint: "https://s3.example.com", region: "us-east-1", bucket: "b", prefix: "aisw", path_style: true, access_key_id_last4: "ABCD" },
  webdav: null, retention_days: 30, updated_by: "usr_p10", updated_at: at,
};
const STORAGE = "**/api/admin/admin/v1/storage";

test("a new S3 configuration shows the probe failure, then saves", async ({ page }) => {
  let state: unknown = { enabled: true, allow_private_endpoints: false, storage: null };
  const bodies: unknown[] = [];
  let attempts = 0;
  await page.route(STORAGE, async route => {
    const method = route.request().method();
    if (method === "GET") { await route.fulfill({ json: state }); return; }
    bodies.push(route.request().postDataJSON());
    attempts += 1;
    if (attempts === 1) { await route.fulfill({ status: 400, json: { error: "upstream_error", code: "storage_unreachable", reason: "auth" } }); return; }
    state = { enabled: true, allow_private_endpoints: false, storage: savedS3 };
    await new Promise(resolve => setTimeout(resolve, 300));
    await route.fulfill({ json: savedS3 });
  });
  await page.goto("/console/settings");
  await expect(page.getByText("未配置（使用平台存储）").first()).toBeVisible();
  await page.locator("#storage-kind").selectOption("s3");
  await expect(page.getByText("只接受公网 https 地址，内网地址与 http 会被拒绝。")).toBeVisible();
  const save = page.getByRole("button", { name: "保存并测试连接" });
  await save.click();
  await expect(page.getByRole("alert").filter({ hasText: "请填写 Bucket" })).toBeVisible();
  await page.getByLabel("Bucket", { exact: true }).fill("b");
  await page.getByLabel("Endpoint").fill("http://s3.example.com");
  await save.click();
  await expect(page.getByRole("alert").filter({ hasText: "只接受公网 https" })).toBeVisible();
  await page.getByLabel("Endpoint").fill("");
  await save.click();
  await expect(page.getByRole("alert").filter({ hasText: "需要填写密钥" })).toBeVisible();
  await page.getByLabel("Access Key ID").fill("AKIAEXAMPLE");
  await page.getByLabel("Secret Access Key").fill("secret-value");
  await save.click();
  await expect(page.getByRole("alert").filter({ hasText: "鉴权未通过" })).toBeVisible();
  // The secrets never outlive the attempt.
  await expect(page.getByLabel("Secret Access Key")).toHaveValue("");
  await page.getByLabel("Access Key ID").fill("AKIAEXAMPLE");
  await page.getByLabel("Secret Access Key").fill("secret-value");
  await save.click();
  await expect(page.getByRole("button", { name: "正在测试连接并保存…" })).toBeDisabled();
  await expect(page.getByText("已保存，连接测试通过。")).toBeVisible();
  await expect(page.getByLabel("Access Key ID")).toHaveAttribute("placeholder", "已保存（末四位 ABCD），留空保持不变");
  expect(bodies[1]).toEqual({
    kind: "s3",
    s3: { endpoint: "", region: "", bucket: "b", prefix: "", path_style: false, access_key_id: "AKIAEXAMPLE", secret_access_key: "secret-value" },
    retention_days: 0,
  });
});

test("a saved configuration keeps its secrets unless the location moves, and can be deleted", async ({ page }) => {
  const bodies: unknown[] = [];
  let deleted = false;
  await page.route(STORAGE, async route => {
    const method = route.request().method();
    if (method === "GET") { await route.fulfill({ json: { enabled: true, allow_private_endpoints: false, storage: deleted ? null : savedS3 } }); return; }
    if (method === "DELETE") { deleted = true; await route.fulfill({ status: 204 }); return; }
    bodies.push(route.request().postDataJSON());
    await route.fulfill({ status: 400, json: { error: "upstream_error", code: "something_new", reason: "dial tcp 10.0.0.1" } });
  });
  await page.goto("/console/settings");
  await expect(page.getByLabel("Access Key ID")).toHaveAttribute("placeholder", "已保存（末四位 ABCD），留空保持不变");
  await page.getByLabel("保留天数").fill("7");
  await page.getByRole("button", { name: "保存并测试连接" }).click();
  // Unknown codes use the generic copy, never the upstream text.
  await expect(page.getByRole("alert").filter({ hasText: "提交的内容不符合要求" })).toBeVisible();
  await expect(page.getByText("dial tcp")).toHaveCount(0);
  expect((bodies[0] as { s3: object }).s3).not.toHaveProperty("access_key_id");
  expect((bodies[0] as { s3: object }).s3).not.toHaveProperty("secret_access_key");

  await page.getByLabel("Bucket", { exact: true }).fill("other");
  await expect(page.getByText("你更改了存储位置，必须重新填写密钥。")).toBeVisible();
  await page.getByRole("button", { name: "保存并测试连接" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "需要填写密钥" })).toBeVisible();
  await page.getByLabel("Access Key ID").fill("AK");
  await page.getByLabel("Secret Access Key").fill("SK");
  await page.getByRole("button", { name: "保存并测试连接" }).click();
  const replace = page.getByRole("dialog");
  await expect(replace).toContainText("旧文件留在原来的存储里");
  await replace.getByRole("button", { name: "保存并测试连接" }).click();
  await expect(replace.getByRole("alert")).toContainText("提交的内容不符合要求");
  await replace.getByRole("button", { name: "取消" }).click();

  await page.getByRole("button", { name: "删除配置" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("已写入的文件会留在你的存储里");
  await dialog.getByRole("button", { name: "删除配置" }).click();
  await expect(page.getByText("已删除对象存储配置。")).toBeVisible();
  await expect(page.getByText("未配置（使用平台存储）").first()).toBeVisible();
});

test("a deployment without tenant storage says so", async ({ page }) => {
  await page.route(STORAGE, route => route.fulfill({ json: { enabled: false } }));
  await page.goto("/console/settings");
  await expect(page.getByText("本部署未开启租户自有对象存储。")).toBeVisible();
  await expect(page.locator("#storage-kind")).toHaveCount(0);
});

test("a 404 from an older control plane reads as not enabled, and a 403 as an error", async ({ page }) => {
  await page.route(STORAGE, route => route.fulfill({ status: 404, json: { error: "not_found" } }));
  await page.goto("/console/settings");
  await expect(page.getByText("本部署未开启租户自有对象存储。")).toBeVisible();
  await page.unroute(STORAGE);
  await page.route(STORAGE, route => route.fulfill({ status: 403, json: { error: "forbidden" } }));
  await page.reload();
  await expect(page.getByRole("alert").filter({ hasText: "没有执行该操作的权限" })).toBeVisible();
});

import fs from "node:fs/promises";
import { test, expect } from "./fixtures";

const node = "node_" + "a".repeat(32);
const filename = `aiserveweave-enrollment-${node}.zip`;
const archive = "UEsDBBQAAAAIANiCXl0sQQbtEwAAABMAAAAGAAAAY2EucGVtK67MK8lILclMVigoTcoBUs6OAFBLAwQUAAAACADYgl5dZbLXgh0AAAAdAAAADwAAAGJvb3RzdHJhcC10b2tlbiuuzCvJSC3JTNbNz0vVLcnMBRKpxSW6JfnZqXkAUEsDBBQAAAAIANiCXl1Dv6ajBAAAAAIAAAAPAAAAY29ubmVjdGlvbi5qc29uq64FAFBLAwQUAAAACADYgl5dzpv1FR8AAAAjAAAACgAAAFJFQURNRS50eHQrrswryUgtyUxWyMwrLknMyUksyczPA3OKSpNB7GIAUEsBAhQDFAAAAAgA2IJeXSxBBu0TAAAAEwAAAAYAAAAAAAAAAAAAAIABAAAAAGNhLnBlbVBLAQIUAxQAAAAIANiCXl1lsteCHQAAAB0AAAAPAAAAAAAAAAAAAACAATcAAABib290c3RyYXAtdG9rZW5QSwECFAMUAAAACADYgl5dQ7+mowQAAAACAAAADwAAAAAAAAAAAAAAgAGBAAAAY29ubmVjdGlvbi5qc29uUEsBAhQDFAAAAAgA2IJeXc6b9RUfAAAAIwAAAAoAAAAAAAAAAAAAAIABsgAAAFJFQURNRS50eHRQSwUGAAAAAAQABADmAAAA+QAAAAAA";
const ca = "-----BEGIN CERTIFICATE-----\nY2VydA==\n-----END CERTIFICATE-----\n";

for (const width of [1440, 375]) {
  test(`agent enrollment downloads CA and ZIP without reminting at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    const writes: unknown[] = [];
    await page.route("**/api/operator/operator/v1/agent-enrollment", async route => {
      if (route.request().method() === "GET") {
        await route.fulfill({ json: { ca_pem: ca, ca_sha256: "a".repeat(64) } });
      } else {
        writes.push(route.request().postDataJSON());
        await route.fulfill({ status: 201, json: { node_id: node, filename, expires_at: "2040-10-30T01:00:00Z", archive_base64: archive } });
      }
    });
    await page.goto("/operator/agent-enrollment");
    await expect(page.getByRole("heading", { name: "Agent 接入", exact: true })).toBeVisible();
    const caDownload = page.waitForEvent("download");
    await page.getByRole("button", { name: "下载 CA 文件", exact: true }).click();
    const downloadedCA = await caDownload;
    expect(downloadedCA.suggestedFilename()).toBe("ca.pem");
    expect(await fs.readFile((await downloadedCA.path())!, "utf8")).toBe(ca);
    await page.getByLabel("Registry 注册地址").fill("registry.example.com:9443");
    await page.getByLabel("Gateway 隧道地址").fill("agent.example.com:8443");
    await page.getByRole("button", { name: "生成新节点接入包", exact: true }).click();
    await expect(page.getByText("接入包已生成", { exact: true })).toBeVisible();
    expect(writes).toEqual([{ registry_addr: "registry.example.com:9443", gateway_addr: "agent.example.com:8443", ttl_minutes: 60 }]);
    for (let i = 0; i < 2; i++) {
      const download = page.waitForEvent("download");
      await page.getByRole("button", { name: "下载 ZIP 接入包", exact: true }).click();
      const saved = await download;
      expect(saved.suggestedFilename()).toBe(filename);
      expect(await fs.readFile((await saved.path())!)).toEqual(Buffer.from(archive, "base64"));
    }
    expect(writes).toHaveLength(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);
    expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain(archive);
    await page.screenshot({ path: `/private/tmp/aisw-agent-enrollment-${width}.png`, fullPage: true });
    await page.reload();
    await expect(page.getByRole("button", { name: "生成新节点接入包", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "下载 ZIP 接入包", exact: true })).toHaveCount(0);
  });
}

test("enrollment generation failures never retry automatically", async ({ page }) => {
  let writes = 0;
  await page.route("**/api/operator/operator/v1/agent-enrollment", async route => {
    if (route.request().method() === "GET") await route.fulfill({ json: { ca_pem: ca, ca_sha256: "a".repeat(64) } });
    else { writes++; await route.fulfill({ status: 503, json: { error: "upstream_error" } }); }
  });
  await page.goto("/operator/agent-enrollment");
  await page.getByLabel("Registry 注册地址").fill("registry.example.com:9443");
  await page.getByLabel("Gateway 隧道地址").fill("agent.example.com:8443");
  await page.getByRole("button", { name: "生成新节点接入包", exact: true }).click();
  await expect(page.getByText(/结果未确认时不要自动重试/)).toBeVisible();
  expect(writes).toBe(1);
  await expect(page.getByRole("button", { name: "下载 ZIP 接入包", exact: true })).toHaveCount(0);
});

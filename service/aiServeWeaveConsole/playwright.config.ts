import { defineConfig } from "@playwright/test";

// Browser acceptance uses an isolated loopback server and synthetic sessions.
// 浏览器验收使用隔离的回环服务与合成会话。
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 30_000,
  reporter: [["list"], ["json", { outputFile: "test-results/results.json" }]],
  use: {
    baseURL: "http://127.0.0.1:3100",
    browserName: "chromium",
    ...(process.env.AISW_BROWSER_CHANNEL ? { channel: process.env.AISW_BROWSER_CHANNEL } : {}),
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "pnpm exec next start --hostname 127.0.0.1 --port 3100",
    url: "http://127.0.0.1:3100/login",
    reuseExistingServer: false,
    env: {
      AISW_CONSOLE_SESSION_SECRET: "p10-browser-acceptance-only-not-a-deployment-secret",
      AISW_CONSOLE_COOKIE_SECURE: "false",
      AISW_CONSOLE_CONTROL_PLANE_URL: "http://127.0.0.1:1",
      AISW_CONSOLE_GATEWAY_URL: "http://127.0.0.1:1",
    },
  },
});

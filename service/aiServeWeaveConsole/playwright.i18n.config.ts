import path from "node:path";
import {defineConfig} from "@playwright/test";
import common from "./playwright.config";

const consoleServer = Array.isArray(common.webServer) ? common.webServer[0] : common.webServer!;
export default defineConfig({
  ...common,
  testIgnore: [],
  testMatch: ["**/i18n-editor.spec.ts", "**/i18n-services.spec.ts"],
  timeout: 120_000,
  webServer: [
    {
      command: "go test -tags=browser ./service/aiServeWeaveControlPlane/internal/comfyeditor -run '^TestEditorBrowser$' -count=1 -v",
      cwd: "../..",
      url: "http://127.0.0.1:3101/operator/v1/comfy-editor/index.html",
      timeout: 120_000,
      reuseExistingServer: false,
      env: {
        GOCACHE: process.env.GOCACHE ?? "/private/tmp/aisw-i18n-go-cache",
        AISW_EDITOR_BROWSER_ADDR: "127.0.0.1:3101",
        AISW_COMFY_BROWSER_DIST: process.env.AISW_COMFY_BROWSER_DIST ?? path.resolve(__dirname, "../../dist/comfy-editor-i18n"),
      },
    },
    {...consoleServer, env: {...consoleServer.env, AISW_CONSOLE_CONTROL_PLANE_URL: "http://127.0.0.1:3101"}},
  ],
});

import assert from "node:assert/strict";
import test from "node:test";

import { ccSwitchImportLink } from "./cc-switch.ts";

test("ccSwitchImportLink targets the base URL for Claude Code and /v1 for Codex", () => {
  const common = { name: "My Gateway", baseUrl: "https://api.example.com/", apiKey: "sk-a+b", model: "m 1" };
  assert.equal(
    ccSwitchImportLink({ ...common, app: "claude" }),
    "ccswitch://v1/import?resource=provider&app=claude&name=My%20Gateway&endpoint=https%3A%2F%2Fapi.example.com&apiKey=sk-a%2Bb&model=m%201"
  );
  const codex = new URL(ccSwitchImportLink({ ...common, app: "codex" }).replace("ccswitch://", "https://x/"));
  assert.equal(codex.searchParams.get("app"), "codex");
  assert.equal(codex.searchParams.get("endpoint"), "https://api.example.com/v1");
});

import assert from "node:assert/strict";
import test from "node:test";
import { parseTenantModels } from "./fleet.ts";

const source = {
  collected_at: "2026-10-15T00:00:00Z", partial: false,
  services: [{ agent_id: "agent-01", service_id: "ollama-11434", kind: "ollama", node_online: true, state: "healthy", version: "1.0", models: [{ id: "agent-model", capabilities: { chat: "supported" } }] }],
};
for (const entry of [
  { name: "Agent models", value: source, accepted: true },
  { name: "offline Agent", value: { ...source, services: [{ ...source.services[0], node_online: false }] }, accepted: true },
  { name: "missing Agent status", value: { ...source, services: [{ ...source.services[0], node_online: undefined }] }, accepted: false },
  { name: "missing Agent identifier", value: { ...source, services: [{ ...source.services[0], agent_id: undefined }] }, accepted: false },
  { name: "missing service identifier", value: { ...source, services: [{ ...source.services[0], service_id: undefined }] }, accepted: false },
  { name: "no services", value: { ...source, services: [] }, accepted: true },
  { name: "failed collection", value: { ...source, services: [], partial: true }, accepted: true },
  { name: "missing service list", value: { ...source, services: undefined }, accepted: false },
  { name: "missing timestamp", value: { ...source, collected_at: undefined }, accepted: false },
  { name: "invalid models", value: { ...source, services: [{ ...source.services[0], models: [{}] }] }, accepted: false },
]) {
  test(`tenant model parser: ${entry.name}`, () => {
    if (!entry.accepted) { assert.throws(() => parseTenantModels(entry.value)); return; }
    assert.equal(parseTenantModels(entry.value).services.length, entry.value.services?.length);
  });
}
test("tenant model parser retains only public service and model fields", () => {
  const parsed = parseTenantModels({ ...source, replicas: [{ endpoint: "private" }], services: [{ ...source.services[0], base_url: "private", node_id: "private", error_summary: "private" }] });
  assert.equal(JSON.stringify(parsed).includes("private"), false);
  assert.equal(parsed.services[0].models[0].capabilities.chat, "supported");
});

const quota = {
  status: "ready", checked_at: "2026-10-25T07:59:11Z",
  windows: [{ name: "five_hour", used_percent: 6, resets_at: "2026-10-25T11:50:00Z" }, { name: "primary", bucket: "codex", used_percent: 0, duration_minutes: 10080 }],
};
test("tenant model parser keeps a CLI quota, a real zero and absent timestamps", () => {
  const parsed = parseTenantModels({ ...source, services: [{ ...source.services[0], cli_usage: quota }] });
  const usage = parsed.services[0].cliUsage;
  assert.equal(usage?.status, "ready");
  assert.equal(usage?.windows[0].usedPercent, 6);
  assert.equal(usage?.windows[1].usedPercent, 0);
  assert.equal(usage?.windows[1].resetsAt, null);
  assert.equal(usage?.windows[1].durationMinutes, 10080);
});
test("tenant model parser treats a missing CLI quota as absent, not as zero", () => {
  assert.equal(parseTenantModels(source).services[0].cliUsage, null);
});
for (const entry of [
  { name: "missing status", value: { ...quota, status: undefined } },
  { name: "window without a name", value: { ...quota, windows: [{ used_percent: 1 }] } },
  { name: "non-numeric percent", value: { ...quota, windows: [{ name: "w", used_percent: "6" }] } },
  { name: "bad reset time", value: { ...quota, windows: [{ name: "w", used_percent: 1, resets_at: "soon" }] } },
]) {
  test(`tenant model parser rejects a malformed CLI quota: ${entry.name}`, () => {
    assert.throws(() => parseTenantModels({ ...source, services: [{ ...source.services[0], cli_usage: entry.value }] }));
  });
}

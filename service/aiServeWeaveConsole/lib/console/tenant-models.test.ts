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

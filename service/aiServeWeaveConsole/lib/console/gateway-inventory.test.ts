import assert from "node:assert/strict";
import test from "node:test";
import { parseGatewaySnapshot } from "./fleet.ts";

const replica = { endpoint: "http://gateway-local:8091", replica_id: "gateway-local" };
for (const entry of [
  { name: "one Gateway", replicas: [replica], partial: false, accepted: true },
  { name: "failed Gateway", replicas: [{ ...replica, error: "timeout" }], partial: true, accepted: true },
  { name: "merged fleet response", replicas: [replica, { ...replica, replica_id: "gateway-other" }], partial: false, accepted: false },
  { name: "no configured Gateway", replicas: [], partial: false, accepted: false },
]) {
  test(`Gateway inventory parser: ${entry.name}`, () => {
    const value = { collected_at: "2026-10-15T00:00:00Z", replicas: entry.replicas, partial: entry.partial, nodes: [] };
    if (entry.accepted) {
      const snapshot = parseGatewaySnapshot(value);
      assert.equal(snapshot.replicas.length, 1);
      assert.equal(snapshot.partial, entry.partial);
    } else assert.throws(() => parseGatewaySnapshot(value));
  });
}

for (const entry of [
  { name: "legacy Gateway", local_runtimes: undefined, accepted: true, count: 0 },
  { name: "local Ollama without Agents", local_runtimes: [{ id: "ollama-11434", kind: "ollama", state: "healthy", models: [{ id: "gateway-model" }] }], accepted: true, count: 1 },
  { name: "invalid local runtime", local_runtimes: [{ state: "healthy" }], accepted: false, count: 0 },
]) {
  test(`Gateway local services: ${entry.name}`, () => {
    const value = { collected_at: "2026-10-15T00:00:00Z", replicas: [{ ...replica, local_runtimes: entry.local_runtimes }], partial: false, nodes: [] };
    if (!entry.accepted) { assert.throws(() => parseGatewaySnapshot(value)); return; }
    const snapshot = parseGatewaySnapshot(value);
    assert.equal(snapshot.nodes.length, 0);
    assert.equal(snapshot.replicas[0].localRuntimes.length, entry.count);
    if (entry.count) assert.equal(snapshot.replicas[0].localRuntimes[0].models[0].id, "gateway-model");
  });
}

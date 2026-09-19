import { test } from "node:test";
import assert from "node:assert/strict";
import {
  comfyUIManagedCustomNodeInstallRequest,
  comfyUIManagedStatusRequest,
  comfyUIManagedTriggerRequest,
  parseComfyUIManagedStatus,
  parseComfyUIManagedTriggerOutcome,
} from "./comfyui-managed.ts";
import { ApiError } from "./errors.ts";

test("parses a status response with one instance and its custom nodes", () => {
  const status = parseComfyUIManagedStatus({
    instances: [
      {
        container_name: "aiserveweave-comfyui",
        state: "running",
        updated_at: "2026-01-01T00:00:00.000Z",
        custom_nodes: [{ name: "my-node", version: "v1" }],
      },
    ],
    replicas: [{ endpoint: "http://gateway-1:8093", connected: true }],
  });
  assert.equal(status.instances.length, 1);
  assert.equal(status.instances[0]?.containerName, "aiserveweave-comfyui");
  assert.equal(status.instances[0]?.state, "running");
  assert.deepEqual(status.instances[0]?.customNodes, [{ name: "my-node", version: "v1" }]);
  assert.equal(status.replicas[0]?.connected, true);
  assert.equal(status.replicas[0]?.error, "");
});

test("an instance with no custom_nodes field parses as an empty list", () => {
  const status = parseComfyUIManagedStatus({
    instances: [{ container_name: "c", state: "pending", updated_at: "2026-01-01T00:00:00.000Z" }],
  });
  assert.deepEqual(status.instances[0]?.customNodes, []);
});

test("a response with no instances or replicas fields parses as empty lists", () => {
  const status = parseComfyUIManagedStatus({});
  assert.deepEqual(status.instances, []);
  assert.deepEqual(status.replicas, []);
});

test("rejects an instance missing container_name", () => {
  assert.throws(
    () => parseComfyUIManagedStatus({ instances: [{ state: "running", updated_at: "2026-01-01T00:00:00.000Z" }] }),
    ApiError
  );
});

test("a replica carries its error code when disconnected", () => {
  const status = parseComfyUIManagedStatus({
    replicas: [{ endpoint: "http://gateway-2:8093", connected: false, error: "unreachable" }],
  });
  assert.equal(status.replicas[0]?.error, "unreachable");
});

test("parses a trigger outcome with no instances field expected", () => {
  const outcome = parseComfyUIManagedTriggerOutcome({
    replicas: [{ endpoint: "http://gateway-1:8093", connected: true }],
  });
  assert.equal(outcome.replicas.length, 1);
});

test("comfyUIManagedTriggerRequest encodes the node id and action", () => {
  const spec = comfyUIManagedTriggerRequest("mac mini/01", "restart");
  assert.equal(spec.surface, "operator");
  assert.equal(spec.method, "POST");
  assert.equal(spec.path, "/operator/v1/nodes/mac%20mini%2F01/comfyui-managed");
  assert.deepEqual(spec.body, { action: "restart" });
});

test("comfyUIManagedCustomNodeInstallRequest encodes the node id and name, never a URL", () => {
  const spec = comfyUIManagedCustomNodeInstallRequest("node-a", "my-node");
  assert.equal(spec.method, "POST");
  assert.equal(spec.path, "/operator/v1/nodes/node-a/comfyui-managed/custom-nodes");
  assert.deepEqual(spec.body, { name: "my-node" });
});

test("comfyUIManagedStatusRequest builds a GET against the node's status path", () => {
  const spec = comfyUIManagedStatusRequest("node-a");
  assert.equal(spec.method, "GET");
  assert.equal(spec.path, "/operator/v1/nodes/node-a/comfyui-managed");
});

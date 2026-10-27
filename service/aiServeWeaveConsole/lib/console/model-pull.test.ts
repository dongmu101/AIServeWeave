import { test } from "node:test";
import assert from "node:assert/strict";
import {
  modelPullStatusRequest,
  modelPullTriggerRequest,
  parseModelPullStatus,
  parseModelPullTriggerOutcome,
} from "./model-pull.ts";
import { ApiError } from "./errors.ts";

test("parses a status response with one pull and one replica", () => {
  const status = parseModelPullStatus({
    pulls: [
      {
        name: "qwen3-coder:30b",
        state: "downloading",
        bytes_downloaded: 1024,
        bytes_total: 4096,
        updated_at: "2026-01-01T00:00:00.000Z",
      },
    ],
    replicas: [{ endpoint: "http://gateway-1:8092", connected: true }],
  });
  assert.equal(status.pulls.length, 1);
  assert.equal(status.pulls[0]?.name, "qwen3-coder:30b");
  assert.equal(status.pulls[0]?.state, "downloading");
  assert.equal(status.pulls[0]?.bytesDownloaded, 1024);
  assert.equal(status.pulls[0]?.bytesTotal, 4096);
  assert.equal(status.pulls[0]?.reason, "");
  assert.equal(status.replicas[0]?.connected, true);
  assert.equal(status.replicas[0]?.error, "");
});

test("a failed pull carries its reason", () => {
  const status = parseModelPullStatus({
    pulls: [
      {
        name: "ghost:latest",
        state: "failed",
        bytes_downloaded: 0,
        bytes_total: 0,
        reason: "checksum_mismatch",
        updated_at: "2026-01-01T00:00:00.000Z",
      },
    ],
  });
  assert.equal(status.pulls[0]?.reason, "checksum_mismatch");
});

test("a response with no pulls or replicas fields parses as empty lists", () => {
  const status = parseModelPullStatus({});
  assert.deepEqual(status.pulls, []);
  assert.deepEqual(status.replicas, []);
});

test("rejects a pull missing a required field", () => {
  assert.throws(
    () => parseModelPullStatus({ pulls: [{ state: "done", bytes_downloaded: 0, bytes_total: 0, updated_at: "2026-01-01T00:00:00.000Z" }] }),
    ApiError
  );
});

test("a replica carries its error code when disconnected", () => {
  const status = parseModelPullStatus({
    replicas: [{ endpoint: "http://gateway-2:8092", connected: false, error: "unreachable" }],
  });
  assert.equal(status.replicas[0]?.error, "unreachable");
});

test("parses a trigger outcome with no pulls field expected", () => {
  const outcome = parseModelPullTriggerOutcome({
    replicas: [{ endpoint: "http://gateway-1:8092", connected: true }],
  });
  assert.equal(outcome.replicas.length, 1);
});

test("modelPullTriggerRequest encodes the node id and names", () => {
  const spec = modelPullTriggerRequest("mac mini/01", ["m1", "m2"]);
  assert.equal(spec.surface, "operator");
  assert.equal(spec.method, "POST");
  assert.equal(spec.path, "/operator/v1/nodes/mac%20mini%2F01/model-pulls");
  assert.deepEqual(spec.body, { names: ["m1", "m2"] });
});

test("modelPullStatusRequest builds a GET against the node's status path", () => {
  const spec = modelPullStatusRequest("node-a");
  assert.equal(spec.method, "GET");
  assert.equal(spec.path, "/operator/v1/nodes/node-a/model-pulls");
});

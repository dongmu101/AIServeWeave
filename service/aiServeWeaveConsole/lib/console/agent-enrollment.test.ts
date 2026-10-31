import assert from "node:assert/strict";
import test from "node:test";
import { parseEnrollmentCA, parseEnrollmentBundle } from "./agent-enrollment.ts";
import { request } from "./api-client.ts";
import { resolveOperatorUpstream, resolveUpstream } from "./upstream-routes.ts";

const node = "node_" + "a".repeat(32);
const response = { node_id: node, expires_at: "2026-10-30T01:00:00Z", filename: `aiserveweave-enrollment-${node}.zip`, archive_base64: "UEsDBA==" };

test("enrollment parsing bounds binary data and rejects unsafe filenames", () => {
  assert.deepEqual([...parseEnrollmentBundle(response).bytes], [80, 75, 3, 4]);
  for (const patch of [{ filename: "../../bad.zip" }, { node_id: "existing-node" }, { expires_at: "bad" }, { archive_base64: "x".repeat(128 * 1024 + 1) }, { archive_base64: "not base64" }, { archive_base64: "c2VjcmV0" }]) {
    assert.throws(() => parseEnrollmentBundle({ ...response, ...patch }));
  }
  const pem = "-----BEGIN CERTIFICATE-----\nY2VydA==\n-----END CERTIFICATE-----\n";
  assert.equal(parseEnrollmentCA({ ca_pem: pem, ca_sha256: "a".repeat(64) }).pem, pem);
  assert.throws(() => parseEnrollmentCA({ ca_pem: pem + "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----", ca_sha256: "a".repeat(64) }));
});

test("enrollment routes exist only on the operator surface and drop query parameters", () => {
  const path = ["operator", "v1", "agent-enrollment"];
  for (const method of ["GET", "POST"]) {
    assert.deepEqual(resolveOperatorUpstream(method, path, new URLSearchParams("ca_file=/secret&node_id=existing")), { path: "/operator/v1/agent-enrollment", search: "" });
    assert.equal(resolveUpstream(method, path, new URLSearchParams()), null);
  }
  assert.equal(resolveOperatorUpstream("DELETE", path, new URLSearchParams()), null);
});

test("a lost enrollment creation response never automatically mints a second token", async () => {
  let calls = 0;
  await assert.rejects(request({ method: "POST", surface: "operator", path: "/operator/v1/agent-enrollment", body: { registry_addr: "registry.example.com:9443", gateway_addr: "agent.example.com:8443", ttl_minutes: 60 }, parse: parseEnrollmentBundle }, {
    fetchImpl: async () => { calls++; throw new TypeError("lost response"); },
    sleep: async () => { assert.fail("a creation must never retry"); },
  }));
  assert.equal(calls, 1);
});

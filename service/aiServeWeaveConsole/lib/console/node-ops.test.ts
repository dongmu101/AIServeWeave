import assert from "node:assert/strict";
import test from "node:test";
import { request } from "./api-client.ts";
import { nodeActionRequest, parseNodeStates } from "./node-ops.ts";

const entry = { node_id: "node-1", pending_approval: true, disabled: false, maintenance: false, first_seen_at: "2026-01-01T00:00:00Z", last_seen_at: "2026-01-02T00:00:00Z" };

test("Registry states preserve pending nodes independently of live fleet", () => {
  assert.deepEqual(parseNodeStates({ items: [entry] }), [{ nodeId: "node-1", pendingApproval: true, disabled: false, maintenance: false, firstSeenAt: entry.first_seen_at, lastSeenAt: entry.last_seen_at }]);
  assert.deepEqual(parseNodeStates({ items: [] }), []);
});
for (const [name, value] of Object.entries({ envelope: [], missingFlag: { items: [{ ...entry, maintenance: undefined }] }, invalidTime: { items: [{ ...entry, last_seen_at: "bad" }] }, invalidId: { items: [{ ...entry, node_id: "" }] } })) {
  test(`Registry parser rejects ${name}`, () => assert.throws(() => parseNodeStates(value)));
}
const ACTION_TARGETS = {
  approve: ["POST", "/operator/v1/nodes/node%2F1/approve"],
  disable: ["POST", "/operator/v1/nodes/node%2F1/disable"],
  enable: ["POST", "/operator/v1/nodes/node%2F1/enable"],
  maintenance: ["POST", "/operator/v1/nodes/node%2F1/maintenance"],
  resume: ["DELETE", "/operator/v1/nodes/node%2F1/maintenance"],
  delete: ["DELETE", "/operator/v1/nodes/node%2F1"],
} as const;
for (const [action, [method, path]] of Object.entries(ACTION_TARGETS) as [keyof typeof ACTION_TARGETS, (typeof ACTION_TARGETS)[keyof typeof ACTION_TARGETS]][]) {
  test(`${action} sends one operator write with an empty JSON body`, async () => {
    let calls = 0;
    const spec = nodeActionRequest("node/1", action);
    assert.equal(spec.path, path);
    assert.equal(spec.method, method);
    await assert.rejects(request(spec, { fetchImpl: async (_url, init) => {
      calls++;
      assert.equal(init?.body, "{}");
      return new Response(null, { status: 503 });
    }, sleep: async () => assert.fail("writes must not retry") }));
    assert.equal(calls, 1);
    assert.equal(await request(spec, { fetchImpl: async () => new Response(null, { status: 204 }) }), null);
  });
}

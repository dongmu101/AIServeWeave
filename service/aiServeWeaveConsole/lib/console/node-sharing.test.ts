import { test } from "node:test";
import assert from "node:assert/strict";
import { parseNodeAccess, parseNodeAccessList, mergeNodeRows } from "./node-access.ts";
import { nodeActionRequest } from "./node-ops.ts";

test("node sharing defaults to private and requires a valid explicit mode", () => {
  assert.equal(mergeNodeRows([{ nodeId: "new-agent", live: true }], [])[0].mode, "private");
  for (const mode of ["private", "shared"] as const) {
    const entry = parseNodeAccess({ node_id: "agent", mode, tenants: [] });
    assert.equal(entry.mode, mode);
    assert.equal(parseNodeAccessList({ items: [{ node_id: "agent", mode, tenants: [] }] })[0].mode, mode);
    assert.equal(mergeNodeRows([], [entry])[0].mode, mode);
  }
  assert.throws(() => parseNodeAccess({ node_id: "agent", mode: "shared", tenants: ["tenant"] }));
  assert.throws(() => parseNodeAccess({ node_id: "agent", mode: "dedicated", tenants: [] }));
  assert.throws(() => parseNodeAccess({ node_id: "agent", mode: "unknown", tenants: [] }));
});

test("approving a node defaults to private and only approval sends sharing", () => {
  assert.deepEqual(nodeActionRequest("agent", "approve").body, { shared: false });
  assert.deepEqual(nodeActionRequest("agent", "approve", true).body, { shared: true });
  assert.deepEqual(nodeActionRequest("agent", "enable", true).body, {});
});

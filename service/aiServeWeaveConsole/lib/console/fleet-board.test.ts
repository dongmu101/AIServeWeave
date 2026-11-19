import { test } from "node:test";
import assert from "node:assert/strict";
import { countByStatus, matchesFleetQuery, matchesStatus, mergeFleetBoard } from "./fleet-board.ts";
import { parseFleetSnapshot } from "./fleet.ts";
import { parseNodeAccessList } from "./node-access.ts";
import { parseNodeStates } from "./node-ops.ts";

const at = "2026-11-18T00:00:00Z";
const state = (node_id: string, extra: Record<string, boolean> = {}) => ({ node_id, pending_approval: false, disabled: false, maintenance: false, first_seen_at: at, last_seen_at: at, ...extra });
const observed = (node_id: string, live: boolean, extra: Record<string, unknown> = {}) => ({ node_id, live, maintenance: false, observed_at: at, ...extra });

const desired = parseNodeStates({ items: [state("b-online"), state("a-pending", { pending_approval: true }), state("c-gone", { disabled: true, maintenance: true })] });
const fleet = parseFleetSnapshot({ collected_at: at, nodes: [
  observed("b-online", true, { labels: { region: "tokyo" }, runtimes: [{ id: "ollama-main", kind: "ollama", state: "healthy" }] }),
  observed("d-unregistered", false, { maintenance: true }),
] }).nodes;
const access = parseNodeAccessList({ items: [{ node_id: "b-online", mode: "shared", tenants: [] }] });

test("the board joins sources per node and orders pending, online, then the rest", () => {
  const board = mergeFleetBoard(desired, fleet, access);
  assert.deepEqual(board.map((node) => [node.nodeId, node.liveness, node.inRegistry, node.access]), [
    ["a-pending", "unobserved", true, "private"],
    ["b-online", "online", true, "shared"],
    ["c-gone", "unobserved", true, "private"],
    ["d-unregistered", "offline", false, "private"],
  ]);
  assert.equal(board[1].desired?.nodeId, "b-online");
  assert.equal(board[1].observed?.nodeId, "b-online");
});

test("a source that was not read is unknown rather than absent", () => {
  const withoutFleet = mergeFleetBoard(desired, null, null);
  assert.ok(withoutFleet.every((node) => node.liveness === "unknown" && node.access === null));
  assert.equal(countByStatus(withoutFleet).online, 0);
  assert.equal(countByStatus(withoutFleet).offline, 0, "unknown liveness is never counted as offline");
  const withoutRegistry = mergeFleetBoard(null, fleet, access);
  assert.ok(withoutRegistry.every((node) => node.inRegistry === null && node.desired === null));
});

test("status tiles count intent and observation each from its own source", () => {
  const board = mergeFleetBoard(desired, fleet, access);
  assert.deepEqual(countByStatus(board), { all: 4, pending: 1, online: 1, offline: 3, maintenance: 2, disabled: 1 });
  const maintenance = board.filter((node) => matchesStatus(node, "maintenance")).map((node) => node.nodeId);
  assert.deepEqual(maintenance, ["c-gone", "d-unregistered"], "desired and observed maintenance both count");
});

test("search covers node IDs and what the Agent reported about itself", () => {
  const [, online, gone] = mergeFleetBoard(desired, fleet, access);
  assert.ok(matchesFleetQuery(online, "REGION=tok"));
  assert.ok(matchesFleetQuery(online, "ollama"));
  assert.ok(matchesFleetQuery(gone, "c-g"));
  assert.ok(!matchesFleetQuery(gone, "ollama"));
  assert.ok(matchesFleetQuery(gone, "  "));
});

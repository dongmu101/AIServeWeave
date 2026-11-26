import assert from "node:assert/strict";
import { test } from "node:test";

import {
  addNode,
  connect,
  isIdentifier,
  moveNode,
  newDefinition,
  nextNodeId,
  referencesFor,
  removeEdges,
  removeNodes,
  serialize,
  setConfig,
  toCanvasEdges,
  unavailableModels,
  upstreamOf,
} from "./flow-graph.ts";
import type { FlowDefinition } from "./flows.ts";

function branching(): FlowDefinition {
  let def = newDefinition();
  def = { ...def, edges: [] };
  let added = addNode(def, "llm", { x: 200, y: 0 }, "qwen3");
  def = added.def;
  added = addNode(def, "condition", { x: 400, y: 0 });
  def = added.def;
  added = addNode(def, "template", { x: 600, y: 0 });
  def = added.def;
  for (const [from, to, handle] of [
    ["start", "llm_1", null],
    ["llm_1", "condition_1", null],
    ["condition_1", "template_1", "true"],
    ["condition_1", "end", "false"],
    ["template_1", "end", null],
  ] as const) {
    const result = connect(def, from, to, handle);
    assert.ok(result.ok, `${from} → ${to}`);
    def = result.def;
  }
  return def;
}

test("a new flow is an input wired to an output", () => {
  const def = newDefinition();
  assert.deepEqual(def.nodes.map((n) => n.type), ["input", "output"]);
  assert.deepEqual(def.edges, [{ from: "start", to: "end" }]);
});

test("added nodes get the first free id and a default config", () => {
  const def = branching();
  assert.equal(nextNodeId(def, "llm"), "llm_2");
  assert.deepEqual(def.nodes.find((n) => n.id === "llm_1")?.config, { model: "qwen3", prompt: "" });
  assert.deepEqual(def.nodes.find((n) => n.id === "condition_1")?.config, { left: "", op: "contains", right: "" });
});

test("connect refuses what the flow could never run", () => {
  const def = branching();
  const refusals: [string, string, string | null][] = [
    ["llm_1", "llm_1", null],
    ["llm_1", "start", null],
    ["end", "llm_1", null],
    ["condition_1", "llm_1", null],
    ["start", "llm_1", null],
    ["template_1", "llm_1", null],
    ["ghost", "end", null],
  ];
  for (const [from, to, handle] of refusals) {
    assert.equal(connect(def, from, to, handle).ok, false, `${from} → ${to}`);
  }
});

test("canvas edges carry the branch as handle and label", () => {
  const edges = toCanvasEdges(branching());
  const yes = edges.find((e) => e.source === "condition_1" && e.target === "template_1");
  assert.deepEqual(yes, { id: "condition_1->template_1:true", source: "condition_1", target: "template_1", sourceHandle: "true", label: "是" });
  assert.equal(edges.find((e) => e.source === "start")?.sourceHandle, null);
  const without = removeEdges(branching(), ["condition_1->template_1:true"]);
  assert.equal(without.edges.some((e) => e.to === "template_1"), false);
});

test("removing nodes drops their edges but never the input or output", () => {
  const def = removeNodes(branching(), ["condition_1", "start", "end"]);
  assert.deepEqual(def.nodes.map((n) => n.id), ["start", "end", "llm_1", "template_1"]);
  assert.equal(def.edges.some((e) => e.from === "condition_1" || e.to === "condition_1"), false);
});

test("references are the inputs and the upstream nodes' fields", () => {
  let def = branching();
  def = setConfig(def, "llm_1", { model: "qwen3", prompt: "", json_fields: [{ name: "score", type: "number" }] });
  assert.deepEqual([...upstreamOf(def, "template_1")].sort(), ["condition_1", "llm_1", "start"]);
  assert.deepEqual(referencesFor(def, "template_1").map((r) => r.ref), [
    "{{inputs.topic}}",
    "{{llm_1.text}}",
    "{{llm_1.score}}",
    "{{condition_1.result}}",
  ]);
  assert.deepEqual(referencesFor(def, "llm_1").map((r) => r.ref), ["{{inputs.topic}}"]);
});

test("models the user cannot call are flagged on their nodes", () => {
  const def = branching();
  assert.deepEqual(unavailableModels(def, ["qwen3"]), []);
  assert.deepEqual(unavailableModels(def, ["llama3"]), [{ node: "llm_1", field: "config.model", code: "model_unavailable" }]);
});

test("serialize drops empty optional fields and unary operands", () => {
  let def = branching();
  def = setConfig(def, "condition_1", { left: "{{llm_1.text}}", op: "empty", right: "ignored" });
  def = setConfig(def, "llm_1", { model: "qwen3", prompt: "hi", system: "", temperature: 0.5 });
  def = moveNode(def, "llm_1", { x: 10.6, y: 20.2 });
  const out = serialize(def);
  assert.deepEqual(out.nodes.find((n) => n.id === "condition_1")?.config, { left: "{{llm_1.text}}", op: "empty" });
  assert.deepEqual(out.nodes.find((n) => n.id === "llm_1")?.config, { model: "qwen3", prompt: "hi", temperature: 0.5 });
  assert.deepEqual(out.nodes.find((n) => n.id === "llm_1")?.position, { x: 11, y: 20 });
  assert.equal(out.nodes.find((n) => n.id === "start")?.config, undefined);
});

test("identifiers follow flowdef's rule", () => {
  for (const ok of ["topic", "_x", "a1"]) assert.ok(isIdentifier(ok), ok);
  for (const bad of ["", "1a", "a-b", "主题", "inputs", "a".repeat(65)]) assert.equal(isIdentifier(bad), false, bad);
});

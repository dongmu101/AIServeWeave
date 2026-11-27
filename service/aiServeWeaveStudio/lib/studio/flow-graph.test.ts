import assert from "node:assert/strict";
import { test } from "node:test";

import {
  addNode,
  artifactReferenceIssues,
  catalogWarnings,
  comfyConfig,
  connect,
  EDITABLE_TYPES,
  imageConfig,
  isIdentifier,
  isImageSize,
  mergeIssues,
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
import type { FlowCatalog, WorkflowTemplate } from "./flow-catalog.ts";
import { parseDefinition, type FlowDefinition } from "./flows.ts";

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

// ---- second-phase nodes: image generation and ComfyUI templates ----
// ---- 第二期节点：图像生成与 ComfyUI 模板 ----

const portrait: WorkflowTemplate = {
  id: "portrait",
  description: "",
  inputs: [
    { name: "prompt", type: "string", required: true },
    { name: "steps", type: "integer", required: false, default: 20 },
    { name: "seed", type: "integer", required: true, default: 1 },
    { name: "ref", type: "file", required: false },
  ],
  outputs: [],
};
const catalog = (over: Partial<FlowCatalog> = {}): FlowCatalog => ({
  imageModels: { models: [{ id: "flux", name: "Flux" }], defaultAvailable: true },
  templates: [portrait],
  ...over,
});

/** start → img → flux → end, with a result field on each artifact */
function media(): FlowDefinition {
  let def: FlowDefinition = { ...newDefinition(), edges: [] };
  def = addNode(def, "image_generation", { x: 200, y: 0 }, "qwen3", "flux").def;
  def = addNode(def, "comfyui_template", { x: 400, y: 0 }).def;
  for (const [from, to] of [["start", "image_generation_1"], ["image_generation_1", "comfyui_template_1"], ["comfyui_template_1", "end"]] as const) {
    const result = connect(def, from, to, null);
    assert.ok(result.ok, `${from} → ${to}`);
    def = result.def;
  }
  return def;
}

test("the palette offers the image and ComfyUI nodes", () => {
  assert.deepEqual(EDITABLE_TYPES, ["llm", "template", "condition", "image_generation", "comfyui_template"]);
});

test("new image and ComfyUI nodes get their own defaults", () => {
  const def = media();
  const image = def.nodes.find((n) => n.id === "image_generation_1")!;
  assert.deepEqual(image.config, { model: "flux", prompt: "" });
  assert.deepEqual(def.nodes.find((n) => n.id === "comfyui_template_1")!.config, { template: "", inputs: {} });
  assert.deepEqual(addNode(newDefinition(), "image_generation", { x: 0, y: 0 }).def.nodes.at(-1)?.config, { model: "", prompt: "" });
});

test("image and ComfyUI nodes connect like llm nodes: one in, one out", () => {
  let def = media();
  assert.ok(!connect(def, "end", "image_generation_1", null).ok);
  assert.ok(!connect(def, "image_generation_1", "start", null).ok);
  assert.ok(!connect(def, "comfyui_template_1", "image_generation_1", null).ok, "a cycle");
  const extra = connect(def, "start", "comfyui_template_1", null);
  assert.ok(extra.ok);
  def = extra.def;
  assert.deepEqual(toCanvasEdges(def).find((e) => e.id === "start->comfyui_template_1")?.sourceHandle, null);
});

test("configs are read with defaults for what is missing", () => {
  const bare = { id: "n", type: "image_generation" as const, position: { x: 0, y: 0 } };
  assert.deepEqual(imageConfig(bare), { model: "", prompt: "" });
  assert.deepEqual(imageConfig({ ...bare, config: { model: "flux", prompt: "p", size: "512x512" } }), { model: "flux", prompt: "p", size: "512x512" });
  assert.deepEqual(comfyConfig({ ...bare, type: "comfyui_template" }), { template: "", inputs: {} });
  assert.deepEqual(comfyConfig({ ...bare, type: "comfyui_template", config: { template: "t", inputs: { a: "1", b: 2, c: null } } }), { template: "t", inputs: { a: "1" } });
  assert.deepEqual(comfyConfig({ ...bare, type: "comfyui_template", config: { inputs: ["x"] } }).inputs, {});
});

test("artifact fields are offered only to an output's result fields", () => {
  const def = media();
  assert.deepEqual(referencesFor(def, "end").map((r) => r.ref), ["{{inputs.topic}}"]);
  assert.deepEqual(referencesFor(def, "end", true).map((r) => r.ref), [
    "{{inputs.topic}}",
    "{{image_generation_1.image}}",
    "{{comfyui_template_1.image}}",
    "{{comfyui_template_1.artifacts}}",
  ]);
  // an image node's own prompt and a downstream template never see them
  assert.deepEqual(referencesFor(def, "comfyui_template_1").map((r) => r.ref), ["{{inputs.topic}}"]);
});

test("an llm after an image node still lists its text fields only", () => {
  let def = media();
  def = addNode(def, "llm", { x: 0, y: 0 }, "qwen3").def;
  const result = connect(def, "image_generation_1", "llm_1", null);
  assert.ok(result.ok);
  assert.deepEqual(referencesFor(result.def, "llm_1").map((r) => r.ref), ["{{inputs.topic}}"]);
});

test("image sizes follow flowdef's rule", () => {
  for (const ok of ["1x1", "1024x1024", "16384x16384", "512x768"]) assert.ok(isImageSize(ok), ok);
  for (const bad of ["", "1024", "0x10", "10x0", "01x10", "10x010", "16385x10", "10x16385", "100000x1", "-1x5", "1.5x2", "10X10", " 10x10", "10x10 ", "10x10x10"]) {
    assert.equal(isImageSize(bad), false, bad);
  }
});

test("an unavailable image model, or no model without a deployment default, is flagged", () => {
  const def = media();
  assert.deepEqual(catalogWarnings(def, catalog()).filter((w) => w.node === "image_generation_1"), []);
  const gone = catalog({ imageModels: { models: [{ id: "sd", name: "SD" }], defaultAvailable: true } });
  assert.deepEqual(catalogWarnings(def, gone).filter((w) => w.node === "image_generation_1"), [{ node: "image_generation_1", field: "config.model", code: "model_unavailable" }]);
  const empty = setConfig(def, "image_generation_1", { model: "", prompt: "" });
  assert.deepEqual(catalogWarnings(empty, catalog()).filter((w) => w.node === "image_generation_1"), []);
  const noDefault = catalog({ imageModels: { models: [{ id: "flux", name: "Flux" }], defaultAvailable: false } });
  assert.deepEqual(catalogWarnings(empty, noDefault).filter((w) => w.node === "image_generation_1"), [{ node: "image_generation_1", field: "config.model", code: "image_generation_unavailable" }]);
});

test("a catalog that failed to load flags nothing", () => {
  const def = setConfig(media(), "comfyui_template_1", { template: "ghost", inputs: {} });
  assert.deepEqual(catalogWarnings(def, { imageModels: null, templates: null }), []);
});

test("a template that is not offered is flagged, and says nothing more about its inputs", () => {
  const def = setConfig(media(), "comfyui_template_1", { template: "ghost", inputs: { prompt: "x" } });
  assert.deepEqual(catalogWarnings(def, catalog()), [{ node: "comfyui_template_1", field: "config.template", code: "template_unavailable" }]);
  assert.equal(catalogWarnings(def, catalog({ templates: [] })).length, 1);
  const unset = setConfig(media(), "comfyui_template_1", { template: "", inputs: {} });
  assert.equal(catalogWarnings(unset, catalog())[0].code, "template_unavailable");
});

test("template inputs: unknown name, unset required, required file, optional file", () => {
  const ok = setConfig(media(), "comfyui_template_1", { template: "portrait", inputs: { prompt: "a cat" } });
  assert.deepEqual(catalogWarnings(ok, catalog()), []);

  const unset = setConfig(media(), "comfyui_template_1", { template: "portrait", inputs: { prompt: "   " } });
  assert.deepEqual(catalogWarnings(unset, catalog()), [{ node: "comfyui_template_1", field: "config.inputs.prompt", code: "required" }]);

  const stale = setConfig(media(), "comfyui_template_1", { template: "portrait", inputs: { prompt: "p", old_name: "x" } });
  assert.deepEqual(catalogWarnings(stale, catalog()), [{ node: "comfyui_template_1", field: "config.inputs.old_name", code: "invalid_input" }]);

  const needsFile: WorkflowTemplate = { ...portrait, inputs: [...portrait.inputs.slice(0, 1), { name: "ref", type: "file", required: true }] };
  const blocked = setConfig(media(), "comfyui_template_1", { template: "portrait", inputs: { prompt: "p" } });
  assert.deepEqual(catalogWarnings(blocked, catalog({ templates: [needsFile] })), [{ node: "comfyui_template_1", field: "config.inputs.ref", code: "unsupported_input" }]);
  // an optional file input does not stop the node
  assert.deepEqual(catalogWarnings(blocked, catalog()), []);
});

test("a required input with a default may be left unset", () => {
  const def = setConfig(media(), "comfyui_template_1", { template: "portrait", inputs: { prompt: "p" } });
  assert.deepEqual(catalogWarnings(def, catalog()), []); // "seed" is required but defaulted
});

test("artifact fields are caught wherever they are not a whole result field", () => {
  let def = media();
  def = addNode(def, "llm", { x: 0, y: 0 }, "qwen3").def;
  def = addNode(def, "template", { x: 0, y: 0 }).def;
  def = addNode(def, "condition", { x: 0, y: 0 }).def;
  def = setConfig(def, "llm_1", { model: "qwen3", system: "{{image_generation_1.image}}", prompt: "see {{comfyui_template_1.artifacts}}" });
  def = setConfig(def, "template_1", { template: "{{comfyui_template_1.image}}" });
  def = setConfig(def, "condition_1", { left: " {{image_generation_1.image}} ", op: "equals", right: "{{comfyui_template_1.image}}" });
  def = setConfig(def, "image_generation_1", { model: "", prompt: "again {{comfyui_template_1.image}}" });
  def = setConfig(def, "comfyui_template_1", { template: "portrait", inputs: { prompt: "{{image_generation_1.image}}", fine: "{{inputs.topic}}" } });
  def = { ...def, outputs: [{ name: "ok", ref: "{{image_generation_1.image}}" }, { name: "bad", ref: "x {{image_generation_1.image}}" }, { name: "txt", ref: "{{llm_1.text}} and {{inputs.topic}}" }] };
  assert.deepEqual(artifactReferenceIssues(def), [
    { node: "image_generation_1", field: "config.prompt", code: "artifact_reference" },
    { node: "comfyui_template_1", field: "config.inputs.prompt", code: "artifact_reference" },
    { node: "llm_1", field: "config.system", code: "artifact_reference" },
    { node: "llm_1", field: "config.prompt", code: "artifact_reference" },
    { node: "template_1", field: "config.template", code: "artifact_reference" },
    { node: "condition_1", field: "config.left", code: "artifact_reference" },
    { node: "condition_1", field: "config.right", code: "artifact_reference" },
    { node: "end", field: "outputs.bad.ref", code: "artifact_reference" },
  ]);
});

test("a flow that references artifacts only as whole result fields has no artifact issues", () => {
  const def: FlowDefinition = { ...media(), outputs: [{ name: "a", ref: "{{image_generation_1.image}}" }, { name: "b", ref: "{{ comfyui_template_1.artifacts }}" }, { name: "c", ref: "{{inputs.topic}}" }] };
  assert.deepEqual(artifactReferenceIssues(def), []);
  assert.deepEqual(artifactReferenceIssues(newDefinition()), []);
});

test("text fields of an image node are not artifacts, so llm text may be referenced anywhere", () => {
  let def = media();
  def = addNode(def, "llm", { x: 0, y: 0 }, "qwen3").def;
  def = setConfig(def, "llm_1", { model: "qwen3", prompt: "{{llm_1.text}} {{inputs.topic}}" });
  assert.deepEqual(artifactReferenceIssues(def), []);
});

test("mergeIssues keeps the server's issues and adds only the local ones it did not report", () => {
  const def = media();
  const local = [
    { node: "llm_1", field: "config.prompt", code: "artifact_reference" },
    { node: "end", field: "outputs.bad.ref", code: "artifact_reference" },
  ];
  assert.deepEqual(mergeIssues(def, [], local), local);
  const server = [{ node: "llm_1", field: "config.prompt", code: "artifact_reference" }, { node: "end", field: "outputs[0].ref", code: "artifact_reference" }, { node: "x", field: "", code: "cycle" }];
  assert.deepEqual(mergeIssues(def, server, local), server);
  assert.deepEqual(mergeIssues(def, [{ node: "llm_1", field: "config.system", code: "artifact_reference" }], local).length, 3);
});

test("serialize writes image and ComfyUI configs without empty optional fields", () => {
  let def = media();
  def = setConfig(def, "image_generation_1", { model: "", prompt: "p", size: "" });
  def = setConfig(def, "comfyui_template_1", { template: "portrait", inputs: { prompt: "a cat", steps: "", seed: "{{inputs.topic}}" } });
  const out = serialize(def);
  assert.deepEqual(out.nodes.find((n) => n.id === "image_generation_1")?.config, { prompt: "p" });
  assert.deepEqual(out.nodes.find((n) => n.id === "comfyui_template_1")?.config, { template: "portrait", inputs: { prompt: "a cat", seed: "{{inputs.topic}}" } });

  def = setConfig(def, "image_generation_1", { model: "flux", prompt: "p", size: "1024x768" });
  def = setConfig(def, "comfyui_template_1", { template: "portrait", inputs: {} });
  const again = serialize(def);
  assert.deepEqual(again.nodes.find((n) => n.id === "image_generation_1")?.config, { model: "flux", prompt: "p", size: "1024x768" });
  assert.deepEqual(again.nodes.find((n) => n.id === "comfyui_template_1")?.config, { template: "portrait" });
});

test("a definition with the new node types survives parse and serialize", () => {
  const def = serialize(media());
  assert.deepEqual(serialize(parseDefinition(JSON.parse(JSON.stringify(def)))), def);
});

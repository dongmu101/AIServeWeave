import { test } from "node:test";
import assert from "node:assert/strict";
import {
  buildGraphPreview,
  compareIDs,
  MAX_PREVIEW_NODES,
  nodeHeight,
  parseGraphText,
  PREVIEW_LITERAL_CHARS,
  stableStringify,
  type GraphBindings,
  type GraphNode,
  type GraphPreview,
} from "./comfy-graph.ts";

// txt2img is the stock ComfyUI default workflow in API format.
// txt2img 是 ComfyUI 自带默认工作流的 API Format 版本。
const txt2img = {
  "3": { class_type: "KSampler", inputs: { seed: 1, steps: 20, model: ["4", 0], positive: ["6", 0], negative: ["7", 0], latent_image: ["5", 0] } },
  "4": { class_type: "CheckpointLoaderSimple", inputs: { ckpt_name: "v1-5.safetensors" } },
  "5": { class_type: "EmptyLatentImage", inputs: { width: 512, height: 512, batch_size: 1 } },
  "6": { class_type: "CLIPTextEncode", inputs: { text: "a cat", clip: ["4", 1] }, _meta: { title: "Positive" } },
  "7": { class_type: "CLIPTextEncode", inputs: { text: "", clip: ["4", 1] } },
  "8": { class_type: "VAEDecode", inputs: { samples: ["3", 0], vae: ["4", 2] } },
  "9": { class_type: "SaveImage", inputs: { images: ["8", 0], filename_prefix: "out" } },
};
const noBindings: GraphBindings = { inputs: [], outputs: [] };

function ok(preview: GraphPreview) {
  if (!preview.ok) throw new Error(`expected a preview, got reason ${preview.reason}`);
  return preview;
}
function node(preview: GraphPreview, id: string): GraphNode {
  const found = ok(preview).nodes.find((n) => n.id === id);
  if (!found) throw new Error(`node ${id} missing`);
  return found;
}
function column(preview: GraphPreview, id: string): number {
  const xs = [...new Set(ok(preview).nodes.map((n) => n.x))].sort((a, b) => a - b);
  return xs.indexOf(node(preview, id).x);
}

for (const tc of [
  { name: "null", graph: null, reason: "not_object" },
  { name: "array", graph: [], reason: "not_object" },
  { name: "string", graph: "x", reason: "not_object" },
  { name: "empty object", graph: {}, reason: "empty" },
  { name: "too many nodes", graph: Object.fromEntries(Array.from({ length: MAX_PREVIEW_NODES + 1 }, (_, i) => [String(i), { class_type: "N", inputs: {} }])), reason: "too_many_nodes" },
]) {
  test(`preview declines ${tc.name}`, () => {
    const got = buildGraphPreview(tc.graph, noBindings);
    assert.equal(got.ok, false, `want not ok, got ${JSON.stringify(got).slice(0, 80)}`);
    if (!got.ok) assert.equal(got.reason, tc.reason, `want ${tc.reason}, got ${got.reason}`);
  });
}

test("preview accepts exactly MAX_PREVIEW_NODES nodes", () => {
  const graph = Object.fromEntries(Array.from({ length: MAX_PREVIEW_NODES }, (_, i) => [String(i), { class_type: "N", inputs: {} }]));
  assert.equal(ok(buildGraphPreview(graph, noBindings)).nodes.length, MAX_PREVIEW_NODES);
});

test("default workflow yields every node, every link and a left-to-right layering", () => {
  const got = ok(buildGraphPreview(txt2img, noBindings));
  assert.deepEqual(got.nodes.map((n) => n.id), ["3", "4", "5", "6", "7", "8", "9"]);
  assert.equal(got.edges.length, 9);
  assert.deepEqual(got.issues, []);
  assert.deepEqual(node(got, "4").outputSlots, [0, 1, 2]);
  assert.deepEqual(node(got, "9").outputSlots, []);
  const ranks = Object.fromEntries(["4", "5", "6", "7", "3", "8", "9"].map((id) => [id, column(got, id)]));
  assert.deepEqual(ranks, { "4": 0, "5": 0, "6": 1, "7": 1, "3": 2, "8": 3, "9": 4 });
  assert.equal(node(got, "6").title, "Positive");
  assert.equal(node(got, "7").title, undefined);
  assert.equal(node(got, "7").fields.find((f) => f.name === "text")!.text, "\"\"");
  assert.equal(node(got, "4").height, nodeHeight(3, 1));
});

test("nodes in the same column do not overlap", () => {
  const got = ok(buildGraphPreview(txt2img, noBindings));
  const byColumn = new Map<number, GraphNode[]>();
  for (const n of got.nodes) byColumn.set(n.x, [...(byColumn.get(n.x) ?? []), n]);
  for (const nodes of byColumn.values()) {
    nodes.sort((a, b) => a.y - b.y);
    for (let i = 1; i < nodes.length; i++) assert.ok(nodes[i].y >= nodes[i - 1].y + nodes[i - 1].height, `node ${nodes[i].id} overlaps ${nodes[i - 1].id}`);
  }
});

test("bindings mark fields and output nodes, and misses become issues", () => {
  const got = ok(buildGraphPreview(txt2img, {
    inputs: [
      { name: "prompt", node: "6", field: "text" },
      { name: "seed", node: "3", field: "seed" },
      { name: "ghost", node: "99", field: "text" },
      { name: "typo", node: "6", field: "txt" },
    ],
    outputs: [{ name: "image", node: "9" }, { name: "lost", node: "42" }],
  }));
  assert.deepEqual(node(got, "6").fields.find((f) => f.name === "text")!.bound, ["prompt"]);
  assert.deepEqual(node(got, "3").fields.find((f) => f.name === "seed")!.bound, ["seed"]);
  assert.deepEqual(node(got, "9").outputs, ["image"]);
  assert.equal(got.issues.length, 3, `issues: ${got.issues.join(" | ")}`);
  for (const needle of ["ghost", "typo", "lost"]) assert.ok(got.issues.some((i) => i.includes(needle)), `missing issue for ${needle}`);
});

for (const tc of [
  { name: "non-object node", graph: { "1": 5, "2": { class_type: "N", inputs: {} } }, nodes: 1, issue: "不是对象" },
  { name: "missing class_type", graph: { "1": { inputs: {} } }, nodes: 1, issue: "class_type" },
  { name: "missing inputs", graph: { "1": { class_type: "N" } }, nodes: 1, issue: "inputs" },
  { name: "dangling link", graph: { "1": { class_type: "N", inputs: { a: ["7", 0] } } }, nodes: 1, issue: "不存在的节点 7" },
  { name: "cycle", graph: { "1": { class_type: "N", inputs: { a: ["2", 0] } }, "2": { class_type: "N", inputs: { a: ["1", 0] } } }, nodes: 2, issue: "环" },
  { name: "self loop", graph: { "1": { class_type: "N", inputs: { a: ["1", 0] } } }, nodes: 1, issue: "自身" },
]) {
  test(`malformed graph is reported, not thrown: ${tc.name}`, () => {
    const got = ok(buildGraphPreview(tc.graph, noBindings));
    assert.equal(got.nodes.length, tc.nodes, `want ${tc.nodes} nodes, got ${got.nodes.length}`);
    assert.ok(got.issues.some((i) => i.includes(tc.issue)), `want issue containing ${tc.issue}, got ${JSON.stringify(got.issues)}`);
  });
}

test("link detection only accepts [string id, non-negative integer slot]", () => {
  const got = ok(buildGraphPreview({
    "1": { class_type: "Src", inputs: {} },
    "2": { class_type: "N", inputs: { link: ["1", 0], num_id: [1, 0], neg: ["1", -1], frac: ["1", 0.5], triple: ["1", 0, 0], pair: ["a", "b"] } },
  }, noBindings));
  const kinds = Object.fromEntries(node(got, "2").fields.map((f) => [f.name, f.kind]));
  assert.deepEqual(kinds, { link: "link", num_id: "literal", neg: "literal", frac: "literal", triple: "literal", pair: "literal" });
  assert.equal(got.edges.length, 1);
});

test("long and multi-line literals are flattened and truncated on canvas but kept whole for the detail panel", () => {
  const long = "line one\n\n  " + "x".repeat(200);
  const field = node(buildGraphPreview({ "1": { class_type: "N", inputs: { text: long, obj: { a: 1 } } } }, noBindings), "1").fields;
  assert.equal(field[0].text.length, PREVIEW_LITERAL_CHARS + 1);
  assert.ok(field[0].text.startsWith("line one x"), field[0].text);
  assert.equal(field[0].full, long);
  assert.equal(field[1].text, "{\"a\":1}");
});

test("diff marks changed and one-sided nodes, ignoring key order", () => {
  const next = structuredClone(txt2img) as Record<string, unknown>;
  (next["3"] as { inputs: Record<string, unknown> }).inputs.steps = 30;
  next["5"] = { inputs: txt2img["5"].inputs, class_type: "EmptyLatentImage" }; // reordered keys only / 只调换键顺序
  delete next["9"];
  next["10"] = { class_type: "PreviewImage", inputs: { images: ["8", 0] } };
  const forward = ok(buildGraphPreview(next, noBindings, txt2img));
  assert.equal(node(forward, "3").diff, "changed");
  assert.equal(node(forward, "5").diff, undefined);
  assert.equal(node(forward, "10").diff, "only-here");
  assert.deepEqual(forward.missingHere, ["9"]);
  const backward = ok(buildGraphPreview(txt2img, noBindings, next));
  assert.equal(node(backward, "9").diff, "only-here");
  assert.deepEqual(backward.missingHere, ["10"]);
  assert.ok(ok(buildGraphPreview(txt2img, noBindings, "not a graph")).nodes.every((n) => n.diff === undefined));
});

for (const tc of [
  { name: "numeric", a: "10", b: "9", want: 1 },
  { name: "numeric before text", a: "2", b: "a", want: -1 },
  { name: "text", a: "b", b: "a", want: 1 },
  { name: "equal", a: "3", b: "3", want: 0 },
]) {
  test(`compareIDs orders ${tc.name}`, () => assert.equal(Math.sign(compareIDs(tc.a, tc.b)), tc.want));
}

test("stableStringify is key-order independent and handles scalars", () => {
  assert.equal(stableStringify({ b: [1, { d: 1, c: 2 }], a: null }), stableStringify({ a: null, b: [1, { c: 2, d: 1 }] }));
  assert.equal(stableStringify(undefined), "null");
});

for (const tc of [
  { name: "valid", text: "{\"1\":{}}", ok: true },
  { name: "half typed", text: "{\"1\":", ok: false },
  { name: "empty", text: "", ok: false },
]) {
  test(`parseGraphText ${tc.name}`, () => assert.equal(parseGraphText(tc.text).ok, tc.ok));
}

import { test } from "node:test";
import assert from "node:assert/strict";
import {
  COMFY_EDITOR_ENTRY,
  EDITOR_BOOTSTRAP_SCRIPT,
  EDITOR_FORWARDED_REQUEST_HEADERS,
  FAKE_WEBSOCKET_SCRIPT,
  isEditorRequestAllowed,
  parseEditorExport,
  resolveEditorUpstream,
} from "./comfy-editor.ts";
import { MAX_CONTENT_BYTES, MAX_GRAPH_BYTES, MAX_WORKFLOW_BYTES, parseTemplateContent, parseTemplateSnapshot, parseWorkflow } from "./workflow-templates.ts";

test("editor bootstrap aligns Response with the realm that owns restricted fetch", () => {
  class HostResponse {}
  const child: Record<string, unknown> = {parent: {Response: HostResponse}};
  new Function("window", "setTimeout", EDITOR_BOOTSTRAP_SCRIPT)(child, () => 0);
  assert.equal(child.Response, HostResponse);
  assert.equal(child.__AISW_EDITOR_ONLY__, true);
});

test("editor upstream resolution stays inside the editor prefix", () => {
  for (const [name, method, segments, search, want] of [
    ["index", "GET", ["index.html"], "", "/operator/v1/comfy-editor/index.html"],
    ["nested asset", "GET", ["assets", "index-abc.js"], "", "/operator/v1/comfy-editor/assets/index-abc.js"],
    ["head", "HEAD", ["index.html"], "", "/operator/v1/comfy-editor/index.html"],
    ["post", "POST", ["api", "settings"], "", "/operator/v1/comfy-editor/api/settings"],
    ["delete", "DELETE", ["api", "userdata", "workflows/a.json"], "", "/operator/v1/comfy-editor/api/userdata/workflows%2Fa.json"],
    ["query kept", "GET", ["api", "userdata"], "?dir=workflows", "/operator/v1/comfy-editor/api/userdata"],
    ["encoded chars", "GET", ["api", "a b?c#d"], "", "/operator/v1/comfy-editor/api/a%20b%3Fc%23d"],
    ["put refused", "PUT", ["api", "settings"], "", null],
    ["patch refused", "PATCH", ["index.html"], "", null],
    ["empty", "GET", [], "", null],
    ["dot dot", "GET", ["..", "admin"], "", null],
    ["dot", "GET", [".", "index.html"], "", null],
    ["embedded dot dot", "GET", ["api", "userdata", "../../x"], "", null],
    ["empty segment", "GET", ["assets", ""], "", null],
    ["backslash", "GET", ["..\\x"], "", null],
    ["control char", "GET", ["a\u0000b"], "", null],
    ["too many segments", "GET", Array(33).fill("a"), "", null],
    ["segment too long", "GET", ["a".repeat(257)], "", null],
    ["search too long", "GET", ["index.html"], "?" + "a".repeat(2048), null],
  ] as const) {
    const got = resolveEditorUpstream(method, [...segments], search);
    assert.equal(got?.path ?? null, want, name);
    if (got) assert.equal(got.search, search, name);
  }
});

test("editor sub-application allow list is same-origin and prefix-bound", () => {
  const origin = "https://console.example";
  for (const [url, want] of [
    ["https://console.example/api/operator/comfy-editor/api/object_info", true],
    ["/api/operator/comfy-editor/assets/x.js", true],
    ["api/settings", true],
    ["./assets/x.js", true],
    ["https://console.example/api/operator/v1/workflow-templates", false],
    ["/api/admin/v1/api-keys", false],
    ["/api/operator/comfy-editor/../operator/v1/routes", false],
    ["https://evil.example/api/operator/comfy-editor/x", false],
    ["https://registry.comfy.org/nodes", false],
    ["//evil.example/api/operator/comfy-editor/", false],
    ["http://console.example/api/operator/comfy-editor/x", false],
    ["http://[::1", false],
    ["data:application/wasm;base64,AGFzbQ==", true],
    ["blob:https://console.example/1234", true],
    ["javascript:alert(1)", false],
  ] as const) {
    assert.equal(isEditorRequestAllowed(url, origin), want, url);
  }
});

test("editor entry names index.html so the derived API base is the editor prefix", () => {
  assert.ok(COMFY_EDITOR_ENTRY.endsWith("/comfy-editor/index.html"));
  assert.ok(!(EDITOR_FORWARDED_REQUEST_HEADERS as readonly string[]).some((h) => ["authorization", "cookie", "host"].includes(h)));
});

test("editor export parsing", () => {
  const workflow = { nodes: [{ id: 6, pos: [0, 0] }], links: [] };
  const output = { "6": { class_type: "CLIPTextEncode", inputs: { text: "a" } } };
  const ok = parseEditorExport(JSON.stringify({ workflow, output, missing: [] }));
  assert.deepEqual(ok, { workflow, graph: output, missing: [] });
  assert.deepEqual(parseEditorExport(JSON.stringify({ workflow, output, missing: ["ImpactX"] })).missing, ["ImpactX"]);
  for (const [name, text] of [
    ["not a string", 5 as unknown as string],
    ["not json", "{"],
    ["array root", "[]"],
    ["null root", "null"],
    ["missing list absent", JSON.stringify({ workflow, output })],
    ["missing list not strings", JSON.stringify({ workflow, output, missing: [1] })],
    ["missing list not array", JSON.stringify({ workflow, output, missing: "x" })],
    ["missing workflow", JSON.stringify({ output, missing: [] })],
    ["workflow without nodes", JSON.stringify({ workflow: { links: [] }, output, missing: [] })],
    ["workflow array", JSON.stringify({ workflow: [], output, missing: [] })],
    ["missing output", JSON.stringify({ workflow, missing: [] })],
    ["empty output", JSON.stringify({ workflow, output: {}, missing: [] })],
    ["array output", JSON.stringify({ workflow, output: [], missing: [] })],
    ["oversized workflow", JSON.stringify({ workflow: { nodes: [], pad: "a".repeat(MAX_WORKFLOW_BYTES) }, output, missing: [] })],
    ["oversized graph", JSON.stringify({ workflow, output: { "1": { class_type: "X", inputs: { t: "a".repeat(MAX_GRAPH_BYTES) } } }, missing: [] })],
  ] as const) {
    assert.throws(() => parseEditorExport(text), name);
  }
});

test("editor export drops the viewport so panning never makes a new revision", () => {
  const output = { "6": { class_type: "CLIPTextEncode", inputs: { text: "a" } } };
  const base = { id: "w", nodes: [{ id: 6, pos: [0, 0] }], links: [] };
  const at = (ds: unknown) => parseEditorExport(JSON.stringify({ workflow: { ...base, extra: { ds, frontendVersion: "1.55.17" } }, output, missing: [] })).workflow;
  const a = at({ scale: 1, offset: [0, 0] }), b = at({ scale: 0.7, offset: [137, -20] });
  assert.deepEqual(a, b);
  assert.deepEqual(a.extra, { frontendVersion: "1.55.17" });
  for (const [name, workflow] of [
    ["no extra", base],
    ["extra without ds", { ...base, extra: { linearData: { inputs: [] } } }],
    ["extra not an object", { ...base, extra: "x" }],
  ] as const) {
    assert.deepEqual(parseEditorExport(JSON.stringify({ workflow, output, missing: [] })).workflow, workflow, name);
  }
});

test("template content carries an optional UI workflow", () => {
  const graph = { "6": { class_type: "CLIPTextEncode", inputs: { text: "a" } } };
  const workflow = { nodes: [{ id: 6 }], extra: { linearData: {} } };
  assert.equal(parseTemplateContent({ inputs: [], graph }).workflow, undefined);
  assert.deepEqual(parseTemplateContent({ inputs: [], graph, workflow }).workflow, workflow);
  const snap = parseTemplateSnapshot({ template_id: "a", revision: 1, digest: "d", created_at: "t", actor_id: "o", inputs: [], graph, workflow });
  assert.deepEqual(snap.workflow, workflow);
  for (const [name, value] of [["no nodes", { links: [] }], ["array", []], ["null", null], ["string", "x"], ["oversized", { nodes: [], p: "a".repeat(MAX_WORKFLOW_BYTES) }]] as const) {
    assert.throws(() => parseTemplateContent({ inputs: [], graph, workflow: value }), name);
    assert.throws(() => parseWorkflow(value), name);
  }
  assert.equal(MAX_CONTENT_BYTES, MAX_GRAPH_BYTES + MAX_WORKFLOW_BYTES + 256 * 1024);
});

test("fake websocket script reports an idle queue and never opens a socket", () => {
  const timers: Array<() => void> = [];
  const win: Record<string, unknown> = {};
  new Function("window", "setTimeout", FAKE_WEBSOCKET_SCRIPT)(win, (fn: () => void) => { timers.push(fn); return 0; });
  const WS = win.WebSocket as new (url: string) => { readyState: number; onopen?: () => void; addEventListener(t: string, f: (e: { data: string }) => void): void; send(d: string): void; close(): void };
  const ws = new WS("ws://x/api/operator/comfy-editor/ws");
  const messages: string[] = [];
  let opened = false;
  ws.onopen = () => { opened = true; };
  ws.addEventListener("message", (e) => messages.push(JSON.parse(e.data).type));
  assert.equal(ws.readyState, 0);
  timers.shift()!();
  assert.equal(ws.readyState, 1);
  assert.ok(opened);
  ws.send(JSON.stringify({ type: "feature_flags", data: {} }));
  ws.send("not json");
  timers.shift()!();
  assert.deepEqual(messages, ["status", "feature_flags"]);
  ws.close(); ws.close();
  assert.equal(ws.readyState, 3);
});

import assert from "node:assert/strict";
import { test } from "node:test";

import { newDefinition } from "./flow-graph.ts";
import type { FlowInput } from "./flows.ts";
import { buildRunInputs, flowFile, formFile, formText, initialFormValues, readFlowFile, runStartBody, serverInputErrors, type BuiltRun } from "./run-inputs.ts";

const inputs: FlowInput[] = [
  { name: "topic", type: "text", required: true },
  { name: "tone", type: "text", required: false, default: "plain" },
  { name: "count", type: "number", required: true },
  { name: "strict", type: "boolean", required: false },
];

test("the form starts from defaults, or from a previous run", () => {
  assert.deepEqual(initialFormValues(inputs), { topic: "", tone: "plain", count: "", strict: false });
  assert.deepEqual(initialFormValues(inputs, { topic: "cats", count: 3, strict: true }), { topic: "cats", tone: "plain", count: "3", strict: true });
});

test("buildRunInputs checks required fields and numbers", () => {
  assert.deepEqual(buildRunInputs(inputs, { topic: " ", tone: "", count: "x", strict: false }), {
    ok: false,
    errors: { topic: "必填。", count: "请输入数字。" },
  });
  assert.deepEqual(buildRunInputs(inputs, { topic: "cats", tone: "", count: " 2.5 ", strict: true }), {
    ok: true,
    inputs: { topic: "cats", count: 2.5, strict: true },
    files: {},
  });
});

// ---- image and file inputs ----
// ---- image 与 file 输入 ----

const fileInputs: FlowInput[] = [
  { name: "topic", type: "text", required: false },
  { name: "photo", type: "image", required: true },
  { name: "doc", type: "file", required: false },
];
const png = () => new File(["png"], "cat.png", { type: "image/png" });
const pdf = () => new File(["%PDF"], "notes.pdf", { type: "application/pdf" });
const earlier = { run_id: "frn_old", artifact_id: "sart_old" };
const reuse = { kind: "reuse" as const, runId: "frn_old", artifactId: "sart_old", filename: "cat.png" };

test("a file input starts empty, and a rerun starts it from the earlier run's file", () => {
  assert.deepEqual(initialFormValues(fileInputs), { topic: "", photo: null, doc: null });
  const previous = { topic: "cats", photo: { artifact_id: "sart_old", filename: "cat.png" }, doc: null };
  assert.deepEqual(initialFormValues(fileInputs, previous, "frn_old"), { topic: "cats", photo: reuse, doc: null });
  // without the earlier run's id, or without a FileValue, there is nothing to keep
  assert.deepEqual(initialFormValues(fileInputs, previous), { topic: "cats", photo: null, doc: null });
  assert.deepEqual(initialFormValues(fileInputs, previous, "../x").photo, null);
  assert.deepEqual(initialFormValues(fileInputs, { photo: "sart_old", doc: { artifact_id: "../x" } }, "frn_old"), { topic: "", photo: null, doc: null });
});

test("a default on a file input is never used to fill the form", () => {
  const odd: FlowInput[] = [{ name: "photo", type: "image", required: false, default: "stale" }];
  assert.deepEqual(initialFormValues(odd), { photo: null });
});

test("an uploaded file goes in files, not in inputs; a kept one goes in inputs as a reference", () => {
  const upload = png();
  const built = buildRunInputs(fileInputs, { topic: "cats", photo: { kind: "upload", file: upload }, doc: reuse });
  assert.ok(built.ok);
  assert.deepEqual(built.inputs, { topic: "cats", doc: earlier });
  assert.deepEqual(Object.keys(built.files), ["photo"]);
  assert.equal(built.files.photo, upload);
});

test("a required file input needs a file; an optional one may be empty and is left out", () => {
  assert.deepEqual(buildRunInputs(fileInputs, { topic: "", photo: null, doc: null }), { ok: false, errors: { photo: "请选择文件。" } });
  const built = buildRunInputs(fileInputs, { topic: "", photo: reuse, doc: null });
  assert.ok(built.ok);
  assert.deepEqual(built.inputs, { photo: earlier });
  assert.deepEqual(built.files, {});
});

test("a kept file whose ids are not ones the BFF and Gateway would take is refused here", () => {
  for (const bad of [{ ...reuse, runId: "flw_1" }, { ...reuse, runId: "" }, { ...reuse, artifactId: "../x" }, { ...reuse, artifactId: "" }]) {
    const built = buildRunInputs(fileInputs, { topic: "", photo: bad, doc: null });
    assert.deepEqual(built, { ok: false, errors: { photo: "上次的文件无法沿用，请重新选择文件。" } });
  }
});

test("text values and file values read back from the form by their own helpers", () => {
  assert.equal(formText("abc"), "abc");
  assert.equal(formText(true), "");
  assert.equal(formText(reuse), "");
  assert.equal(formText(undefined), "");
  assert.equal(formFile(reuse), reuse);
  assert.equal(formFile("abc"), null);
  assert.equal(formFile(null), null);
  assert.equal(formFile(undefined), null);
});

test("a run without files is the JSON body it always was", () => {
  const built: BuiltRun = { inputs: { topic: "cats", doc: earlier }, files: {} };
  assert.deepEqual(runStartBody(built), { inputs: { topic: "cats", doc: earlier } });
  assert.deepEqual(runStartBody(built, 3), { inputs: { topic: "cats", doc: earlier }, version: 3 });
});

test("a run with files is multipart: the JSON in `request`, one part per file named after its input", async () => {
  const upload = png();
  const other = pdf();
  const body = runStartBody({ inputs: { topic: "cats", extra: earlier }, files: { photo: upload, doc: other } }, 2);
  assert.ok(body instanceof FormData);
  assert.deepEqual([...body.keys()].sort(), ["doc", "photo", "request"]);
  assert.deepEqual(JSON.parse(String(body.get("request"))), { inputs: { topic: "cats", extra: earlier }, version: 2 });
  const photo = body.get("photo");
  assert.ok(photo instanceof File);
  assert.equal(photo.name, "cat.png");
  assert.equal(await photo.text(), "png");
  const doc = body.get("doc");
  assert.ok(doc instanceof File);
  assert.equal(doc.name, "notes.pdf");
  // a file is never also a JSON value
  assert.equal("photo" in JSON.parse(String(body.get("request"))).inputs, false);
});

test("a file part keeps the file's own name, even a name that looks like a field", async () => {
  const body = runStartBody({ inputs: {}, files: { request: new File(["x"], "request") } });
  assert.ok(body instanceof FormData);
  assert.equal(body.getAll("request").length, 2);
  assert.equal(String(body.getAll("request")[0]), "{\"inputs\":{}}");
  assert.ok(body.getAll("request")[1] instanceof File);
});

test("a refused start puts each issue under its input, the first one only", () => {
  assert.deepEqual(
    serverInputErrors([
      { node: "", field: "inputs.photo", code: "unsupported_file_type" },
      { node: "", field: "inputs.photo", code: "not_found" },
      { node: "", field: "inputs.doc", code: "not_found" },
      { node: "", field: "inputs.topic", code: "required" },
      { node: "", field: "inputs.a-weird name", code: "unknown_input" },
      { node: "", field: "", code: "artifact_storage_unavailable" },
    ]),
    {
      photo: "文件类型不受支持，或文件内容与扩展名不符；图片输入只接受 png、jpg、jpeg、webp、gif、bmp。",
      doc: "上次运行的这个文件已不存在或已过保留期，请重新选择文件。",
      topic: "这一项必填。",
      "": "提交了未声明的输入，或多出了没有对应输入的文件。",
    }
  );
  assert.deepEqual(serverInputErrors([]), {});
});

test("a flow survives export and import; other JSON does not import", () => {
  const def = newDefinition();
  assert.deepEqual(readFlowFile(flowFile("摘要", def)), { name: "摘要", definition: def });
  assert.deepEqual(readFlowFile(JSON.stringify(def))?.definition, def);
  assert.equal(readFlowFile('{"hello":"world"}'), null);
  assert.equal(readFlowFile("not json"), null);
  assert.equal(readFlowFile('{"format":"aiserveweave-flow","definition":{"nodes":[{"id":"x","type":"loop"}]}}'), null);
});

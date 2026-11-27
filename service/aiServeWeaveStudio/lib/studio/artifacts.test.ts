import assert from "node:assert/strict";
import { test } from "node:test";

import {
  artifactFieldShape,
  artifactOutputs,
  artifactResponseHeaders,
  artifactURL,
  downloadFailureText,
  downloadName,
  kindOfFilename,
  parseArtifactList,
  parseFileValue,
  refArtifactShape,
  refFileShape,
  runInputFiles,
  stepFileInputs,
} from "./artifacts.ts";
import type { FlowDefinition } from "./flows.ts";

function def(outputs: FlowDefinition["outputs"]): FlowDefinition {
  return {
    inputs: [],
    nodes: [
      { id: "start", type: "input", position: { x: 0, y: 0 } },
      { id: "img", type: "image_generation", config: { prompt: "p" }, position: { x: 0, y: 0 } },
      { id: "flux", type: "comfyui_template", config: { template: "t" }, position: { x: 0, y: 0 } },
      { id: "txt", type: "llm", config: { model: "m", prompt: "p" }, position: { x: 0, y: 0 } },
      { id: "end", type: "output", position: { x: 0, y: 0 } },
    ],
    edges: [],
    outputs,
  };
}

test("only the image and ComfyUI nodes' image and artifacts fields are artifacts", () => {
  assert.equal(artifactFieldShape("image_generation", "image"), "image");
  assert.equal(artifactFieldShape("image_generation", "artifacts"), null);
  assert.equal(artifactFieldShape("comfyui_template", "image"), "image");
  assert.equal(artifactFieldShape("comfyui_template", "artifacts"), "list");
  assert.equal(artifactFieldShape("llm", "text"), null);
  assert.equal(artifactFieldShape("llm", "image"), null);
});

test("an output is an artifact only when its ref is the sole reference to an artifact field", () => {
  const out = artifactOutputs(
    def([
      { name: "cover", ref: "{{img.image}}" },
      { name: "padded", ref: "  {{ flux.image }}\n" },
      { name: "all", ref: "{{flux.artifacts}}" },
      { name: "mixed", ref: "image: {{img.image}}" },
      { name: "twice", ref: "{{img.image}}{{flux.image}}" },
      { name: "text", ref: "{{txt.text}}" },
      { name: "fake", ref: "{{txt.image}}" },
      { name: "missing", ref: "{{nope.image}}" },
      { name: "input", ref: "{{inputs.image}}" },
      { name: "empty", ref: "" },
    ])
  );
  assert.deepEqual(out, { cover: "image", padded: "image", all: "list" });
});

test("parseArtifactList reads the artifacts of a ComfyUI node", () => {
  const list = parseArtifactList(
    JSON.stringify([
      { artifact_id: "art_1", filename: "a.png", kind: "image" },
      { artifact_id: "art_2", filename: "b.mp4", kind: "video" },
      { artifact_id: "art_3", filename: "c.bin", kind: "file" },
      { artifact_id: "art_4", filename: "d.glb", kind: "model" },
      { artifact_id: "art_5", kind: "image" },
    ])
  );
  assert.deepEqual(list, [
    { id: "art_1", filename: "a.png", kind: "image" },
    { id: "art_2", filename: "b.mp4", kind: "video" },
    { id: "art_3", filename: "c.bin", kind: "file" },
    { id: "art_4", filename: "d.glb", kind: "file" },
    { id: "art_5", filename: "", kind: "image" },
  ]);
  assert.deepEqual(parseArtifactList("[]"), []);
});

test("parseArtifactList refuses anything that is not a list of artifacts", () => {
  for (const bad of ["", "not json", "{}", '"x"', "null", "[1]", "[null]", '[{"filename":"a"}]', '[{"artifact_id":""}]', '[{"artifact_id":"../x"}]', '[{"artifact_id":"a b"}]', '[{"artifact_id":5}]']) {
    assert.equal(parseArtifactList(bad), null, bad);
  }
  assert.equal(parseArtifactList(JSON.stringify([{ artifact_id: "a".repeat(129) }])), null);
});

test("a long artifact list is not truncated", () => {
  const many = Array.from({ length: 1000 }, (_, i) => ({ artifact_id: `art_${i}`, filename: `${i}.png`, kind: "image" }));
  assert.equal(parseArtifactList(JSON.stringify(many))?.length, 1000);
});

test("artifactURL points at the BFF route and refuses ids it would refuse", () => {
  assert.equal(artifactURL("frn_abc-1", "art_X_9"), "/api/studio/runs/frn_abc-1/artifacts/art_X_9");
  for (const [run, art] of [["flw_1", "a"], ["frn_1", ""], ["frn_1", "a/b"], ["frn_1", ".."], ["frn_1", "a?x=1"], ["", "a"], ["frn_", "a"]]) {
    assert.equal(artifactURL(run, art), "", `${run} ${art}`);
  }
});

test("a download is named after the file, else the artifact id", () => {
  assert.equal(downloadName({ id: "art_1", filename: "cat.png" }), "cat.png");
  assert.equal(downloadName({ id: "art_1", filename: "  " }), "art_1");
});

test("download headers pass type, length and disposition and add the inert-bytes headers", () => {
  const out = artifactResponseHeaders(
    new Headers({
      "Content-Type": "image/png",
      "Content-Length": "1234",
      "Content-Disposition": 'attachment; filename="cat.png"',
      "Set-Cookie": "x=1",
      "X-Internal": "secret",
      "Content-Security-Policy": "default-src *",
    })
  );
  assert.equal(out.get("content-type"), "image/png");
  assert.equal(out.get("content-length"), "1234");
  assert.equal(out.get("content-disposition"), 'attachment; filename="cat.png"');
  assert.equal(out.get("x-content-type-options"), "nosniff");
  assert.equal(out.get("content-security-policy"), "sandbox");
  assert.equal(out.get("cache-control"), "private, no-store");
  assert.equal(out.get("set-cookie"), null);
  assert.equal(out.get("x-internal"), null);
});

test("download headers fall back to an opaque type and an attachment", () => {
  const out = artifactResponseHeaders(new Headers());
  assert.equal(out.get("content-type"), "application/octet-stream");
  assert.equal(out.get("content-disposition"), "attachment");
  assert.equal(out.get("content-length"), null);
});

test("a length is not passed when the body was re-encoded", () => {
  const out = artifactResponseHeaders(new Headers({ "Content-Length": "10", "Content-Encoding": "gzip" }));
  assert.equal(out.get("content-length"), null);
  assert.equal(artifactResponseHeaders(new Headers({ "Content-Length": "10", "Content-Encoding": "identity" })).get("content-length"), "10");
});

// ---- files a run was started with ----
// ---- 运行启动时收到的文件 ----

function withFiles(): FlowDefinition {
  const base = def([]);
  return {
    ...base,
    inputs: [
      { name: "topic", type: "text", required: true },
      { name: "photo", label: "照片", type: "image", required: true },
      { name: "doc", type: "file", required: false },
    ],
    nodes: base.nodes.map((n) => (n.id === "flux" ? { ...n, config: { template: "t", inputs: { ref: "{{inputs.photo}}", sheet: " {{ inputs.doc }} ", prev: "{{img.image}}", list: "{{flux.artifacts}}", mixed: "a {{inputs.photo}}", txt: "{{inputs.topic}}" } } } : n)),
  };
}

test("a file input is an artifact: an image input an image, a file input a file", () => {
  const d = withFiles();
  assert.equal(refArtifactShape(d, { scope: "inputs", field: "photo" }), "image");
  assert.equal(refArtifactShape(d, { scope: "inputs", field: "doc" }), "file");
  assert.equal(refArtifactShape(d, { scope: "inputs", field: "topic" }), null);
  assert.equal(refArtifactShape(d, { scope: "inputs", field: "nope" }), null);
  assert.equal(refArtifactShape(d, { scope: "flux", field: "artifacts" }), "list");
  assert.equal(refArtifactShape(d, { scope: "txt", field: "text" }), null);
});

test("exactly one file: a file input or a node's image, never a list", () => {
  const d = withFiles();
  assert.equal(refFileShape(d, { scope: "inputs", field: "photo" }), "image");
  assert.equal(refFileShape(d, { scope: "inputs", field: "doc" }), "file");
  assert.equal(refFileShape(d, { scope: "img", field: "image" }), "image");
  assert.equal(refFileShape(d, { scope: "flux", field: "image" }), "image");
  assert.equal(refFileShape(d, { scope: "flux", field: "artifacts" }), null);
  assert.equal(refFileShape(d, { scope: "txt", field: "image" }), null);
});

test("an output that is a sole reference to a file input is shown as that file", () => {
  const d: FlowDefinition = { ...withFiles(), outputs: [
    { name: "pic", ref: "{{inputs.photo}}" },
    { name: "file", ref: "  {{ inputs.doc }}\n" },
    { name: "mixed", ref: "see {{inputs.photo}}" },
    { name: "text", ref: "{{inputs.topic}}" },
  ] };
  assert.deepEqual(artifactOutputs(d), { pic: "image", file: "file" });
});

test("a ComfyUI node's inputs that are one file are found, whatever else it holds", () => {
  assert.deepEqual(stepFileInputs(withFiles(), "flux"), { ref: "image", sheet: "file", prev: "image" });
  assert.deepEqual(stepFileInputs(withFiles(), "img"), {});
  assert.deepEqual(stepFileInputs(withFiles(), "nope"), {});
  assert.deepEqual(stepFileInputs(def([]), "flux"), {});
});

test("a run's input files are read from its recorded FileValues, in declaration order", () => {
  const inputs = {
    topic: "cats",
    doc: { artifact_id: "sart_2", filename: "notes.pdf" },
    photo: { artifact_id: "sart_1", filename: "cat.png" },
  };
  assert.deepEqual(runInputFiles(withFiles(), inputs), [
    { name: "photo", label: "照片", type: "image", file: { artifactId: "sart_1", filename: "cat.png" } },
    { name: "doc", label: "doc", type: "file", file: { artifactId: "sart_2", filename: "notes.pdf" } },
  ]);
  // an input never given, or a value that is not a FileValue, shows nothing
  assert.deepEqual(runInputFiles(withFiles(), { photo: "sart_1", doc: null }), []);
  assert.deepEqual(runInputFiles(withFiles(), {}), []);
  // a text input holding something file-shaped is still text
  assert.deepEqual(runInputFiles(withFiles(), { topic: { artifact_id: "sart_1", filename: "x" } }), []);
});

test("parseFileValue accepts only an object with a safe artifact id", () => {
  assert.deepEqual(parseFileValue({ artifact_id: "sart_1", filename: "a.png" }), { artifactId: "sart_1", filename: "a.png" });
  assert.deepEqual(parseFileValue({ artifact_id: "sart_1" }), { artifactId: "sart_1", filename: "" });
  for (const bad of [null, undefined, "sart_1", 5, [], {}, { artifact_id: "" }, { artifact_id: "../x" }, { artifact_id: 7 }, { artifact_id: "a".repeat(129) }]) {
    assert.equal(parseFileValue(bad), null, JSON.stringify(bad));
  }
});

test("a file is shown by its extension", () => {
  assert.equal(kindOfFilename("cat.PNG"), "image");
  assert.equal(kindOfFilename("a.b.jpeg"), "image");
  assert.equal(kindOfFilename("clip.mov"), "video");
  assert.equal(kindOfFilename("song.mp3"), "file");
  assert.equal(kindOfFilename("README"), "file");
  assert.equal(kindOfFilename(""), "file");
});

test("a failed download explains an expired file, a dead session, and anything else", () => {
  assert.match(downloadFailureText(404), /已不存在，可能超过保留期被清理/);
  assert.match(downloadFailureText(401), /重新登录/);
  assert.equal(downloadFailureText(500), downloadFailureText(0));
  assert.match(downloadFailureText(500), /下载失败/);
});

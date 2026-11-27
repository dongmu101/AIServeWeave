import assert from "node:assert/strict";
import { test } from "node:test";

import {
  artifactFieldShape,
  artifactOutputs,
  artifactResponseHeaders,
  artifactURL,
  downloadName,
  parseArtifactList,
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

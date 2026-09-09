import { test } from "node:test";
import assert from "node:assert/strict";
import {
  templateBodyLimit,
  MAX_CONTENT_BYTES,
  parseTemplateContent,
  parseTemplateSnapshot,
  parseTemplateSummaries,
  parseTemplateStatus,
  parseVisibleTenantIDs,
  importTemplateManifestFile,
  templateWriteMessage,
} from "./workflow-templates.ts";
import { resolveOperatorUpstream, resolveUpstream } from "./upstream-routes.ts";
import { ApiError } from "./errors.ts";
import { request } from "./api-client.ts";

const graph = { "6": { class_type: "CLIPTextEncode", inputs: { text: "a cat" } } };
const content = { description: "d", inputs: [{ name: "prompt", node: "6", field: "text", type: "string", required: true }], graph };

test("content copy preserves inputs and graph without aliasing", () => {
  const copy = parseTemplateContent(content);
  (copy.inputs[0] as { name: string }).name = "changed";
  assert.equal(content.inputs[0].name, "prompt");
  assert.deepEqual(copy.graph, graph);
});

for (const [name, value] of Object.entries({
  "missing graph": { inputs: [] },
  "unknown field": { ...content, credential: "hidden" },
  "empty input name": { ...content, inputs: [{ ...content.inputs[0], name: " " }] },
  "unknown input type": { ...content, inputs: [{ ...content.inputs[0], type: "weird" }] },
  "duplicate custom node": { ...content, dependencies: { custom_nodes: [{ name: "a" }, { name: "a" }] } },
  "empty output type": { ...content, outputs: [{ name: "o", node: "6" }] },
})) {
  test(`content validation rejects ${name}`, () => assert.throws(() => parseTemplateContent(value)));
}

test("valid outputs and dependencies survive a round trip", () => {
  const withMeta = {
    ...content,
    outputs: [{ name: "image", node: "6", type: "image" }],
    dependencies: { custom_nodes: [{ name: "ComfyUI-Impact-Pack", version: "1.0" }], models: [{ name: "sdxl-base" }] },
  };
  const parsed = parseTemplateContent(withMeta);
  assert.deepEqual(parsed.outputs, withMeta.outputs);
  assert.deepEqual(parsed.dependencies, withMeta.dependencies);
});

test("visible tenant ids default to an empty (all-visible) list and reject blanks", () => {
  assert.deepEqual(parseVisibleTenantIDs(undefined), []);
  assert.deepEqual(parseVisibleTenantIDs(["tenant-a"]), ["tenant-a"]);
  assert.throws(() => parseVisibleTenantIDs([" "]));
});

test("snapshot and catalogue parsing carry the same content shape", () => {
  const snapshot = parseTemplateSnapshot({ template_id: "alpha", revision: 1, digest: "d", created_at: "2026-09-08T00:00:00Z", actor_id: "operator", ...content });
  assert.equal(snapshot.template_id, "alpha");
  assert.equal(snapshot.description, "d");
  assert.deepEqual(snapshot.visible_tenant_ids, []);

  const list = parseTemplateSummaries({
    items: [{ template_id: "alpha", revision: 1, digest: "d", created_at: "2026-09-08T00:00:00Z", actor_id: "operator", description: "d", inputs: content.inputs, visible_tenant_ids: ["tenant-a"] }],
  });
  assert.equal(list.length, 1);
  assert.deepEqual(list[0].visible_tenant_ids, ["tenant-a"]);
});

test("incomplete bundle status stays explicit", () => {
  for (const replicas of [[], [{ endpoint: "gw", error: "unavailable" }], [{ endpoint: "gw", mode: "file", template_count: 1, bundle_digest: "d" }]]) {
    assert.equal(
      parseTemplateStatus({ desired_template_count: 1, desired_bundle_digest: "d", checked_at: "2026-09-08T00:00:00Z", complete: true, replicas }).complete,
      false
    );
  }
  const row = { endpoint: "gw", mode: "controlplane", template_count: 1, bundle_digest: "d" };
  assert.equal(parseTemplateStatus({ desired_template_count: 1, desired_bundle_digest: "d", checked_at: "now", complete: true, replicas: [row] }).complete, true);
});

test("import checks size before reading and supports an existing file-mode manifest", async () => {
  let read = false;
  await assert.rejects(importTemplateManifestFile({ size: MAX_CONTENT_BYTES + 1, stream() { read = true; throw Error(); } }));
  assert.equal(read, false);
  const imported = await importTemplateManifestFile(new Blob([JSON.stringify({ id: "ignored-here", ...content })]));
  assert.deepEqual(imported.graph, graph);
});

test("dishonest import size cannot bypass the stream bound", async () => {
  let canceled = false;
  await assert.rejects(
    importTemplateManifestFile({ size: 1, stream: () => new ReadableStream({ start(controller) { controller.enqueue(new Uint8Array(MAX_CONTENT_BYTES + 1)); }, cancel() { canceled = true; } }) })
  );
  assert.equal(canceled, true);
});

test("workflow-template allowlist is operator only with exact methods", () => {
  for (const [method, suffix] of [
    ["GET", ""],
    ["GET", "/status"],
    ["GET", "/alpha"],
    ["GET", "/alpha/history"],
    ["GET", "/alpha/revisions/1"],
    ["POST", "/alpha/validate"],
    ["POST", "/alpha/publish"],
    ["POST", "/alpha/rollback"],
  ] as const) {
    const path = `operator/v1/workflow-templates${suffix}`.split("/");
    assert.ok(resolveOperatorUpstream(method, path, new URLSearchParams()), `${method} ${suffix} should resolve`);
    assert.equal(resolveUpstream(method, path, new URLSearchParams()), null);
  }
  assert.equal(resolveOperatorUpstream("DELETE", ["operator", "v1", "workflow-templates", "alpha"], new URLSearchParams()), null);
  assert.equal(resolveOperatorUpstream("GET", ["operator", "v1", "workflow-templates", "alpha", "revisions", "bad"], new URLSearchParams()), null);
  assert.equal(
    resolveOperatorUpstream("GET", ["operator", "v1", "workflow-templates", "alpha", "history"], new URLSearchParams("limit=10&before=5&token=secret"))?.search,
    "?limit=10&before=5"
  );
});

test("large request allowance is scoped to exact template publish and validate writes", () => {
  for (const path of ["/operator/v1/workflow-templates/alpha/publish", "/operator/v1/workflow-templates/alpha/validate"]) assert.equal(templateBodyLimit(path), MAX_CONTENT_BYTES);
  for (const path of ["/operator/v1/workflow-templates/alpha/rollback", "/admin/v1/workflow-templates/alpha/publish", "/operator/v1/workflow-templates/alpha/publish/extra"]) {
    assert.equal(templateBodyLimit(path), undefined);
  }
});

test("publish conflict preserves caller draft and never retries", async () => {
  const draft = parseTemplateContent(content);
  let calls = 0;
  await assert.rejects(
    request(
      { method: "POST", surface: "operator", path: "/operator/v1/workflow-templates/alpha/publish", body: { expected_revision: 1, content: draft }, parse: parseTemplateSnapshot },
      { fetchImpl: async () => { calls++; return new Response("{}", { status: 409 }); } }
    ),
    (error: unknown) => {
      assert.match(templateWriteMessage(error), /草稿.*保留/);
      return error instanceof ApiError;
    }
  );
  assert.equal(calls, 1);
  assert.deepEqual(draft.graph, graph);
});

for (const operation of ["publish", "rollback"]) {
  test(`${operation} transport failure never retries`, async () => {
    let calls = 0;
    await assert.rejects(
      request(
        { method: "POST", surface: "operator", path: `/operator/v1/workflow-templates/alpha/${operation}`, body: { expected_revision: 1, revision: 1 }, parse: parseTemplateSnapshot },
        { fetchImpl: async () => { calls++; throw new TypeError("transport"); } }
      )
    );
    assert.equal(calls, 1);
  });
}

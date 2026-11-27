import assert from "node:assert/strict";
import { test } from "node:test";

import { FLOW_BODY_BYTES, resolveStudioRoute } from "./upstream-routes.ts";

const none = new URLSearchParams();

test("allowed routes map onto the Gateway Studio API", () => {
  assert.deepEqual(resolveStudioRoute("GET", ["models"], none), { method: "GET", path: "/studio/v1/models", search: "" });
  assert.deepEqual(resolveStudioRoute("GET", ["me"], none), { method: "GET", path: "/studio/v1/me", search: "" });
});

test("unlisted methods and paths are refused", () => {
  assert.equal(resolveStudioRoute("POST", ["models"], none), null);
  assert.equal(resolveStudioRoute("GET", ["models", "x"], none), null);
  assert.equal(resolveStudioRoute("GET", [], none), null);
});

test("traversal and odd segments are refused before matching", () => {
  assert.equal(resolveStudioRoute("GET", ["..", "models"], none), null);
  assert.equal(resolveStudioRoute("GET", ["models%2F.."], none), null);
  assert.equal(resolveStudioRoute("GET", ["mo dels"], none), null);
});

test("query parameters not on the route's list are dropped", () => {
  const params = new URLSearchParams("tenant_id=other&limit=5");
  assert.equal(resolveStudioRoute("GET", ["models"], params)?.search, "");
});

test("conversation routes are allowed with their own query parameters", () => {
  const params = new URLSearchParams("q=go&cursor=abc&tenant_id=other");
  assert.deepEqual(resolveStudioRoute("GET", ["conversations"], params), { method: "GET", path: "/studio/v1/conversations", search: "?q=go&cursor=abc" });
  assert.equal(resolveStudioRoute("PATCH", ["conversations", "cnv_1"], none)?.path, "/studio/v1/conversations/cnv_1");
  assert.equal(resolveStudioRoute("DELETE", ["conversations", "cnv_1"], none)?.path, "/studio/v1/conversations/cnv_1");
  assert.equal(resolveStudioRoute("GET", ["conversations", "cnv_1", "messages"], new URLSearchParams("after_seq=3"))?.search, "?after_seq=3");
});

test("sending is not on the forward list: it has its own streaming route", () => {
  assert.equal(resolveStudioRoute("POST", ["conversations", "cnv_1", "messages"], none), null);
});

test("flow and run routes are allowed, with room for a definition", () => {
  assert.deepEqual(resolveStudioRoute("POST", ["flows"], none), { method: "POST", path: "/studio/v1/flows", search: "", maxBody: FLOW_BODY_BYTES });
  assert.equal(resolveStudioRoute("PATCH", ["flows", "flw_1"], none)?.maxBody, FLOW_BODY_BYTES);
  assert.equal(resolveStudioRoute("GET", ["flows", "flw_1"], none)?.maxBody, undefined);
  assert.equal(resolveStudioRoute("GET", ["flows", "flw_1", "versions", "3"], none)?.path, "/studio/v1/flows/flw_1/versions/3");
  assert.equal(resolveStudioRoute("GET", ["flows", "flw_1", "runs"], new URLSearchParams("cursor=x&q=y"))?.search, "?cursor=x");
  assert.equal(resolveStudioRoute("GET", ["runs", "frn_1"], none)?.path, "/studio/v1/runs/frn_1");
  assert.equal(resolveStudioRoute("POST", ["runs", "frn_1", "cancel"], none)?.path, "/studio/v1/runs/frn_1/cancel");
});

test("run events are not on the forward list: they have their own streaming route", () => {
  assert.equal(resolveStudioRoute("GET", ["runs", "frn_1", "events"], none), null);
  assert.equal(resolveStudioRoute("PUT", ["flows", "flw_1"], none), null);
});

test("the image model and workflow template catalogs are allowed for GET only", () => {
  assert.deepEqual(resolveStudioRoute("GET", ["image-models"], none), { method: "GET", path: "/studio/v1/image-models", search: "" });
  assert.deepEqual(resolveStudioRoute("GET", ["workflow-templates"], new URLSearchParams("tenant_id=other")), { method: "GET", path: "/studio/v1/workflow-templates", search: "" });
  for (const name of ["image-models", "workflow-templates"]) {
    for (const method of ["POST", "PUT", "PATCH", "DELETE"]) assert.equal(resolveStudioRoute(method, [name], none), null, `${method} ${name}`);
    assert.equal(resolveStudioRoute("GET", [name, "x"], none), null);
  }
});

test("artifact downloads are not on the forward list: they have their own streaming route", () => {
  assert.equal(resolveStudioRoute("GET", ["runs", "frn_1", "artifacts", "art_1"], none), null);
});

test("starting a run is not on the forward list: it has its own streaming route for files", () => {
  assert.equal(resolveStudioRoute("POST", ["flows", "flw_1", "runs"], none), null);
  assert.equal(resolveStudioRoute("POST", ["flows", "flw_1", "runs", "start"], none), null);
  assert.equal(resolveStudioRoute("GET", ["flows", "flw_1", "runs"], none)?.path, "/studio/v1/flows/flw_1/runs");
});

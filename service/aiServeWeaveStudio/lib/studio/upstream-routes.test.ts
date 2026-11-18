import assert from "node:assert/strict";
import { test } from "node:test";

import { resolveStudioRoute } from "./upstream-routes.ts";

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

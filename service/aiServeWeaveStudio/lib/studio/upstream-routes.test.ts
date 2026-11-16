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

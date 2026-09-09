import { operatorTarget } from "./operator-session.ts";
import { test } from "node:test";
import assert from "node:assert/strict";
import { routeBodyLimit, MAX_ROUTE_DOCUMENT_BYTES, parseRoutes, parseRouteSnapshot, parseRouteStatus, importRouteFile, routeWriteMessage, MAX_ROUTES_BYTES } from "./model-routes.ts";
import { resolveOperatorUpstream, resolveUpstream } from "./upstream-routes.ts";
import { readBoundedText, MAX_BODY_BYTES } from "../server/responses.ts";
import { ApiError } from "./errors.ts";
import { request } from "./api-client.ts";
const routes = [{ model: "alias", targets: [{ runtime_model: "real", priority: -1, weight: 4, node_selector: { gpu: "yes" } }] }];
test("route copy preserves selectors and numeric semantics without aliasing", () => {
  const copy = parseRoutes(routes);
  copy[0].targets[0].node_selector!.gpu = "other";
  assert.equal(routes[0].targets[0].node_selector.gpu, "yes");
  assert.equal(copy[0].targets[0].priority, -1);
  assert.equal(copy[0].targets[0].weight, 4);
});
for (const [name, value] of Object.entries({ duplicate: [...routes, ...routes], negative: [{ model: "x", targets: [{ runtime_model: "x", weight: -1 }] }], unknown: [{ ...routes[0], credential: "hidden" }], empty: [{ model: " ", targets: [] }] })) {
  test(`route validation rejects ${name}`, () => assert.throws(() => parseRoutes(value)));
}
test("unpublished snapshot and incomplete status stay explicit", () => {
  assert.equal(parseRouteSnapshot({ revision: 0, digest: "", created_at: "0001-01-01T00:00:00Z", actor_id: "", routes: [] }).revision, 0);
  for (const replicas of [[], [{ endpoint: "gw", error: "unavailable" }], [{ endpoint: "gw", mode: "file", revision: 1, digest: "d" }]]) {
    assert.equal(parseRouteStatus({ desired_revision: 1, desired_digest: "d", checked_at: "2026-09-08T00:00:00Z", complete: true, replicas }).complete, false);
  }
});
test("import checks size before reading and supports existing route file", async () => {
  let read = false;
  await assert.rejects(importRouteFile({ size: MAX_ROUTES_BYTES + 1, stream() { read = true; throw Error(); } }));
  assert.equal(read, false);
  assert.deepEqual(await importRouteFile(new Blob([JSON.stringify(routes)])), routes);
});
test("route allowlist is operator only with exact methods and history filters", () => {
  for (const [method, suffix] of [["GET", ""], ["POST", "/validate"], ["POST", "/publish"], ["POST", "/rollback"], ["GET", "/history"], ["GET", "/status"], ["GET", "/revisions/12"]]) {
    const path = `operator/v1/routes${suffix}`.split("/");
    assert.ok(resolveOperatorUpstream(method, path, new URLSearchParams()));
    assert.equal(resolveUpstream(method, path, new URLSearchParams()), null);
  }
  assert.equal(resolveOperatorUpstream("DELETE", ["operator", "v1", "routes"], new URLSearchParams()), null);
  assert.equal(resolveOperatorUpstream("GET", ["operator", "v1", "routes", "revisions", "bad"], new URLSearchParams()), null);
  assert.equal(resolveOperatorUpstream("GET", ["operator", "v1", "routes", "history"], new URLSearchParams("limit=10&before=5&token=secret"))?.search, "?limit=10&before=5");
});
test("larger explicit body bound leaves default limit intact", async () => {
  const body = "x".repeat(MAX_BODY_BYTES + 1);
  assert.equal(await readBoundedText(new Request("http://local", { method: "POST", body })), null);
  assert.equal(await readBoundedText(new Request("http://local", { method: "POST", body }), MAX_ROUTES_BYTES), body);
});
test("publish conflict preserves caller draft and never retries", async () => {
  const draft = parseRoutes(routes);
  let calls = 0;
  await assert.rejects(request({ method: "POST", surface: "operator", path: "/operator/v1/routes/publish", body: { expected_revision: 1, routes: draft }, parse: parseRouteSnapshot }, { fetchImpl: async () => { calls++; return new Response("{}", { status: 409 }); } }), (error: unknown) => {
    assert.match(routeWriteMessage(error), /草稿.*保留/);
    return error instanceof ApiError;
  });
  assert.equal(calls, 1);
  assert.deepEqual(draft, routes);
});

test("platform login preserves the new route page only on its own surface", () => {
  assert.equal(operatorTarget("/operator/routes"), "/operator/routes");
  assert.equal(operatorTarget("/console/routes"), "/operator/fleet");
});
test("dishonest import size cannot bypass stream bound", async () => {
  let canceled = false;
  await assert.rejects(importRouteFile({ size: 1, stream: () => new ReadableStream({ start(controller) { controller.enqueue(new Uint8Array(MAX_ROUTES_BYTES + 1)); }, cancel() { canceled = true; } }) }));
  assert.equal(canceled, true);
});
test("matching managed observation requires actual desired publication", () => {
  const row = { endpoint: "gw", mode: "controlplane", revision: 2, digest: "digest" };
  assert.equal(parseRouteStatus({ desired_revision: 2, desired_digest: "digest", checked_at: "now", complete: true, replicas: [row] }).complete, true);
  assert.equal(parseRouteStatus({ desired_revision: 2, desired_digest: "other", checked_at: "now", complete: true, replicas: [row] }).complete, false);
});

test("large request allowance is scoped to exact platform bundle writes", () => {
  for (const path of ["/operator/v1/routes/publish", "/operator/v1/routes/validate"]) assert.equal(routeBodyLimit(path), MAX_ROUTE_DOCUMENT_BYTES);
  for (const path of ["/operator/v1/routes/rollback", "/admin/v1/routes/publish", "/operator/v1/nodes/x/disable", "/operator/v1/routes/publish/extra"]) assert.equal(routeBodyLimit(path), undefined);
});
for (const operation of ["publish", "rollback"]) test(`${operation} transport failure never retries`, async () => {
  let calls = 0;
  await assert.rejects(request({ method: "POST", surface: "operator", path: `/operator/v1/routes/${operation}`, body: { expected_revision: 1, revision: 1 }, parse: parseRouteSnapshot }, { fetchImpl: async () => { calls++; throw new TypeError("transport"); } }));
  assert.equal(calls, 1);
});

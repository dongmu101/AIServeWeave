import assert from "node:assert/strict";
import test from "node:test";

import { nodeActionRequest } from "./node-ops.ts";
import { resolveOperatorUpstream, resolveUpstream } from "./upstream-routes.ts";

/** resolve is the call under test, with the query string spelled inline.
 *
 * resolve 是被测调用，查询串就地写出。 */
function resolve(method: string, path: string, query = "") {
  return resolveUpstream(
    method,
    path.split("/").filter((segment) => segment !== ""),
    new URLSearchParams(query)
  );
}

test("the allowlist admits exactly the calls the Console makes", () => {
  const cases: {
    name: string;
    method: string;
    path: string;
    want: string | null;
  }[] = [
    { name: "list users", method: "GET", path: "/admin/v1/users", want: "/admin/v1/users" },
    { name: "create user", method: "POST", path: "/admin/v1/users", want: "/admin/v1/users" },
    { name: "change own password", method: "POST", path: "/admin/v1/auth/password", want: "/admin/v1/auth/password" },
    { name: "revoke own sessions", method: "POST", path: "/admin/v1/auth/sessions/revoke", want: "/admin/v1/auth/sessions/revoke" },
    { name: "reset user password", method: "PUT", path: "/admin/v1/users/usr_1/password", want: "/admin/v1/users/usr_1/password" },
    { name: "change user role", method: "PUT", path: "/admin/v1/users/usr_1/role", want: "/admin/v1/users/usr_1/role" },
    { name: "disable user", method: "POST", path: "/admin/v1/users/usr_1/disable", want: "/admin/v1/users/usr_1/disable" },
    { name: "enable user", method: "POST", path: "/admin/v1/users/usr_1/enable", want: "/admin/v1/users/usr_1/enable" },
    { name: "revoke user sessions", method: "POST", path: "/admin/v1/users/usr_1/sessions/revoke", want: "/admin/v1/users/usr_1/sessions/revoke" },
    { name: "list keys", method: "GET", path: "/admin/v1/apikeys", want: "/admin/v1/apikeys" },
    { name: "create key", method: "POST", path: "/admin/v1/apikeys", want: "/admin/v1/apikeys" },
    {
      name: "revoke key",
      method: "DELETE",
      path: "/admin/v1/apikeys/key-123",
      want: "/admin/v1/apikeys/key-123",
    },
    {
      name: "set limits",
      method: "PUT",
      path: "/admin/v1/tenants/limits",
      want: "/admin/v1/tenants/limits",
    },
    { name: "read audit", method: "GET", path: "/admin/v1/audit", want: "/admin/v1/audit" },
    {
      name: "read the current tenant and its quota",
      method: "GET",
      path: "/admin/v1/tenants/current",
      want: "/admin/v1/tenants/current",
    },
    { name: "wrong method on the tenant read", method: "POST", path: "/admin/v1/tenants/current", want: null },
    { name: "lowercase method", method: "get", path: "/admin/v1/users", want: "/admin/v1/users" },

    { name: "sign-in is not forwardable", method: "POST", path: "/admin/v1/auth/login", want: null },
    { name: "tenant bootstrap", method: "POST", path: "/admin/v1/tenants", want: null },
    { name: "gateway verification", method: "POST", path: "/internal/v1/apikeys/verify", want: null },
    { name: "wrong method on users", method: "DELETE", path: "/admin/v1/users", want: null },
    { name: "wrong method on audit", method: "POST", path: "/admin/v1/audit", want: null },
    { name: "unknown resource", method: "GET", path: "/admin/v1/nodes", want: null },
    { name: "extra segment", method: "GET", path: "/admin/v1/users/u-1", want: null },
    { name: "empty key id", method: "DELETE", path: "/admin/v1/apikeys/", want: null },
    { name: "traversal as key id", method: "DELETE", path: "/admin/v1/apikeys/..", want: null },
    {
      name: "encoded traversal as key id",
      method: "DELETE",
      path: "/admin/v1/apikeys/a%2F..%2Fusers",
      want: null,
    },
    { name: "root", method: "GET", path: "/", want: null },
  ];

  for (const item of cases) {
    const resolved = resolve(item.method, item.path);
    assert.equal(
      resolved?.path ?? null,
      item.want,
      `${item.name}: ${item.method} ${item.path}`
    );
  }
});

test("only the query parameters a route names are forwarded", () => {
  const cases: {
    name: string;
    method: string;
    path: string;
    query: string;
    want: string;
  }[] = [
    {
      name: "audit paging and filters pass",
      method: "GET",
      path: "/admin/v1/audit",
      query: "limit=50&cursor=abc&action=apikey.revoke&actor_id=usr_1&since=2026-09-01T00%3A00%3A00Z&until=2026-09-07T00%3A00%3A00Z",
      want: "?limit=50&cursor=abc&action=apikey.revoke&actor_id=usr_1&since=2026-09-01T00%3A00%3A00Z&until=2026-09-07T00%3A00%3A00Z",
    },
    {
      name: "audit drops unlisted parameters",
      method: "GET",
      path: "/admin/v1/audit",
      query: "limit=50&tenant_id=other&debug=1",
      want: "?limit=50",
    },
    {
      name: "users take paging, role and q",
      method: "GET",
      path: "/admin/v1/users",
      query: "limit=50&cursor=abc&role=admin&q=ada",
      want: "?limit=50&cursor=abc&role=admin&q=ada",
    },
    {
      name: "users drop an audit filter",
      method: "GET",
      path: "/admin/v1/users",
      query: "action=user.create&actor_id=usr_1",
      want: "",
    },
    {
      name: "keys take paging, status and q",
      method: "GET",
      path: "/admin/v1/apikeys",
      query: "limit=50&status=revoked&q=prod",
      want: "?limit=50&status=revoked&q=prod",
    },
    {
      name: "the tenant read takes no parameters at all",
      method: "GET",
      path: "/admin/v1/tenants/current",
      query: "tenant_id=other",
      want: "",
    },
    {
      name: "no parameters",
      method: "GET",
      path: "/admin/v1/audit",
      query: "",
      want: "",
    },
  ];

  for (const item of cases) {
    const resolved = resolve(item.method, item.path, item.query);
    assert.equal(resolved?.search, item.want, `${item.name}: ?${item.query}`);
  }
});

test("the operator surface is separate from the tenant one", () => {
  const operatorResolve = (method: string, path: string) =>
    resolveOperatorUpstream(
      method,
      path.split("/").filter((segment) => segment !== ""),
      new URLSearchParams()
    );

  const cases: {
    name: string;
    method: string;
    path: string;
    operator: string | null;
    tenant: string | null;
  }[] = [
    {
      name: "the fleet inventory",
      method: "GET",
      path: "/operator/v1/nodes",
      operator: "/operator/v1/nodes",
      tenant: null,
    },
    {
      name: "the model catalog",
      method: "GET",
      path: "/operator/v1/models",
      operator: "/operator/v1/models",
      tenant: null,
    },
    // The two tables must not overlap in either direction. A tenant session
    // reaching a fleet path, or the operator token paying for an Admin API
    // call, would each be one table entry away from being wrong.
    //
    // 两张表在任何方向上都不得重叠。租户会话够到机群路径，或者运维 token 为一次
    // Admin API 调用买单，两者都只差一个表项就会出错。
    {
      name: "the tenant user list is not on the operator surface",
      method: "GET",
      path: "/admin/v1/users",
      operator: null,
      tenant: "/admin/v1/users",
    },
    {
      name: "the tenant quota write is not on the operator surface",
      method: "PUT",
      path: "/admin/v1/tenants/limits",
      operator: null,
      tenant: "/admin/v1/tenants/limits",
    },
    {
      name: "writing to the fleet is not forwardable at all",
      method: "POST",
      path: "/operator/v1/nodes",
      operator: null,
      tenant: null,
    },
    {
      name: "the workflow rollout is an operator question",
      method: "GET",
      path: "/operator/v1/workflows",
      operator: "/operator/v1/workflows",
      tenant: null,
    },
    // The menu and a tenant's own runs are tenant questions and stay on the
    // tenant surface: sending them through the operator entry point would
    // spend a deployment secret on a question the session already answers.
    //
    // 菜单与租户自己的运行是租户的问题，留在租户面上：把它们经由运维入口发送，等于
    // 为一个会话本来就能回答的问题花掉一个部署密钥。
    {
      name: "the workflow menu is a tenant question",
      method: "GET",
      path: "/admin/v1/workflows",
      operator: null,
      tenant: "/admin/v1/workflows",
    },
    {
      name: "a tenant's runs stay on the tenant surface",
      method: "GET",
      path: "/admin/v1/jobs",
      operator: null,
      tenant: "/admin/v1/jobs",
    },
    {
      name: "an unknown operator path",
      method: "GET",
      path: "/operator/v1/unknown",
      operator: null,
      tenant: null,
    },
    {
      name: "a tenant's persisted job history stays on the tenant surface",
      method: "GET",
      path: "/admin/v1/jobs/history",
      operator: null,
      tenant: "/admin/v1/jobs/history",
    },
    {
      name: "a persisted job's detail stays on the tenant surface",
      method: "GET",
      path: "/admin/v1/jobs/history/job_1",
      operator: null,
      tenant: "/admin/v1/jobs/history/job_1",
    },
  ];

  for (const item of cases) {
    assert.equal(
      operatorResolve(item.method, item.path)?.path ?? null,
      item.operator,
      `${item.name}: operator table`
    );
    assert.equal(
      resolve(item.method, item.path)?.path ?? null,
      item.tenant,
      `${item.name}: tenant table`
    );
  }
});

test("platform operations and audit are allowlisted only on their own surface", () => {
  const operatorResolve = (method: string, path: string) => resolveOperatorUpstream(method, path.split("/").filter(Boolean), new URLSearchParams());
  for (const [method, path] of [
    ["GET", "/operator/v1/nodes/states"],
    ["POST", "/operator/v1/nodes/node-1/approve"],
    ["POST", "/operator/v1/nodes/node-1/disable"],
    ["POST", "/operator/v1/nodes/node-1/enable"],
    ["POST", "/operator/v1/nodes/node-1/maintenance"],
    ["DELETE", "/operator/v1/nodes/node-1/maintenance"],
    ["GET", "/operator/v1/audit"],
    ["POST", "/operator/v1/auth/password"],
    ["POST", "/operator/v1/auth/sessions/revoke"],
    ["GET", "/operator/v1/operators"],
    ["POST", "/operator/v1/operators"],
    ["PUT", "/operator/v1/operators/plt_1/password"],
    ["POST", "/operator/v1/operators/plt_1/disable"],
    ["POST", "/operator/v1/operators/plt_1/enable"],
    ["POST", "/operator/v1/operators/plt_1/sessions/revoke"],
  ]) {
    assert.equal(operatorResolve(method, path)?.path, path);
    assert.equal(resolve(method, path), null);
  }
  assert.equal(operatorResolve("DELETE", "/operator/v1/nodes/node-1"), null);
  assert.equal(operatorResolve("POST", "/admin/v1/platform/operators"), null);
});

test("platform operator list forwards only its filters", () => {
  const result = resolveOperatorUpstream("GET", ["operator", "v1", "operators"], new URLSearchParams("limit=10&cursor=c&status=active&q=ops&tenant_id=tnt_1"));
  assert.equal(result?.search, "?limit=10&cursor=c&status=active&q=ops");
});

test("node routes accept operator labels, preserving one safe path segment", () => {
  for (const id of ["gpu:0", "台北节点", "n".repeat(180), "node%2Fone"]) {
    const request = nodeActionRequest(id, "disable");
    const result = resolveOperatorUpstream(request.method, request.path.split("/").slice(1).map(decodeURIComponent), new URLSearchParams());
    assert.equal(result?.path, `/operator/v1/nodes/${encodeURIComponent(id)}/disable`);
  }
  for (const id of ["..", ".", "node/one", "node\\one", "node\n"]) {
    assert.equal(resolveOperatorUpstream("POST", ["operator", "v1", "nodes", id, "disable"], new URLSearchParams()), null);
  }
});


test("platform audit forwards only its pagination and filter parameters", () => {
  const result = resolveOperatorUpstream("GET", ["operator", "v1", "audit"], new URLSearchParams("limit=10&cursor=c&action=node.disable&actor_id=pop_1&since=start&until=end&tenant_id=tnt_1&token=untrusted"));
  assert.equal(result?.search, "?limit=10&cursor=c&action=node.disable&actor_id=pop_1&since=start&until=end");
});

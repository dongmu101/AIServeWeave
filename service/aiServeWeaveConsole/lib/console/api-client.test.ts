import assert from "node:assert/strict";
import test from "node:test";

import { request, signIn, signOut } from "./api-client.ts";
import { parseNoContent } from "./contract.ts";
import { ApiError } from "./errors.ts";

/** Recorder is a fetch stand-in that answers from a script and remembers what
 * it was asked, so a test never touches a network or a clock.
 *
 * Recorder 是 fetch 的替身：按脚本作答并记住被问了什么，因此测试从不触碰网络或时钟。 */
function recorder(script: (Response | Error)[]) {
  const calls: { url: string; method: string; body: string | null }[] = [];
  const delays: number[] = [];
  let index = 0;

  const fetchImpl = (async (url: string | URL | Request, init?: RequestInit) => {
    calls.push({
      url: String(url),
      method: init?.method ?? "GET",
      body: typeof init?.body === "string" ? init.body : null,
    });
    const next = script[Math.min(index, script.length - 1)];
    index += 1;
    if (next instanceof Error) {
      throw next;
    }
    return next!.clone();
  }) as unknown as typeof fetch;

  return {
    calls,
    delays,
    deps: {
      fetchImpl,
      sleep: async (ms: number) => {
        delays.push(ms);
      },
    },
  };
}

/** json builds a response the way the Console's own server routes do.
 *
 * json 按 Console 自己的服务端路由的方式构造一个响应。 */
function json(status: number, body: unknown): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** identity is the parser for tests that do not care about the shape.
 *
 * identity 是那些不关心形状的测试所用的解析器。 */
const identity = (value: unknown) => value;

test("a read goes to the Console's own entry point, with only the query it names", async () => {
  const fake = recorder([json(200, [])]);
  await request(
    { method: "GET", path: "/admin/v1/audit", query: { limit: "50" }, parse: identity },
    fake.deps
  );
  assert.deepEqual(fake.calls, [
    { url: "/api/admin/admin/v1/audit?limit=50", method: "GET", body: null },
  ]);
});

test("a read is retried on a failure that says nothing about the outcome", async () => {
  const cases: {
    name: string;
    script: (Response | Error)[];
    wantCalls: number;
    wantDelays: number[];
  }[] = [
    {
      name: "a 500 then success",
      script: [json(500, { error: "upstream" }), json(200, [])],
      wantCalls: 2,
      wantDelays: [200],
    },
    {
      name: "a dropped connection then success",
      script: [new TypeError("fetch failed"), json(200, [])],
      wantCalls: 2,
      wantDelays: [200],
    },
    {
      name: "three failures stop at the attempt bound",
      script: [json(503, {}), json(503, {}), json(503, {})],
      wantCalls: 3,
      wantDelays: [200, 600],
    },
    {
      name: "a 403 is not retried",
      script: [json(403, {}), json(200, [])],
      wantCalls: 1,
      wantDelays: [],
    },
    {
      name: "a 404 is not retried",
      script: [json(404, {}), json(200, [])],
      wantCalls: 1,
      wantDelays: [],
    },
  ];

  for (const item of cases) {
    const fake = recorder(item.script);
    await request(
      { method: "GET", path: "/admin/v1/users", parse: identity },
      fake.deps
    ).catch(() => undefined);
    assert.equal(fake.calls.length, item.wantCalls, `${item.name}: attempts`);
    assert.deepEqual(fake.delays, item.wantDelays, `${item.name}: waits`);
  }
});

test("a write is never retried, whatever the failure looks like", async () => {
  for (const method of ["POST", "PUT", "DELETE"] as const) {
    const fake = recorder([json(500, {}), json(200, {})]);
    await request(
      { method, path: "/admin/v1/apikeys", body: { name: "prod" }, parse: identity },
      fake.deps
    ).catch(() => undefined);
    assert.equal(
      fake.calls.length,
      1,
      `${method}: a repeated write could mint a second credential`
    );
  }
});

test("a 204 reaches the caller as no content rather than a parse failure", async () => {
  const fake = recorder([json(204, null)]);
  const result = await request(
    { method: "DELETE", path: "/admin/v1/apikeys/k-1", parse: parseNoContent },
    fake.deps
  );
  assert.equal(result, null);
});

test("a status becomes the kind the UI decides from", async () => {
  const cases: { name: string; status: number; want: string }[] = [
    { name: "expired session", status: 401, want: "unauthorized" },
    { name: "wrong role", status: 403, want: "forbidden" },
    { name: "other tenant or unknown id", status: 404, want: "not_found" },
    { name: "duplicate", status: 409, want: "conflict" },
    { name: "rejected input", status: 400, want: "invalid" },
    { name: "upstream failure", status: 502, want: "server" },
    { name: "unrecognized status", status: 418, want: "server" },
  ];

  for (const item of cases) {
    const fake = recorder([json(item.status, { error: "internal detail" })]);
    await assert.rejects(
      request({ method: "POST", path: "/admin/v1/users", parse: identity }, fake.deps),
      (error: unknown) => {
        assert.ok(error instanceof ApiError, item.name);
        assert.equal(error.kind, item.want, item.name);
        assert.equal(
          error.message.includes("internal detail"),
          false,
          `${item.name}: upstream text must not reach the message`
        );
        return true;
      }
    );
  }
});

test("a body that is not the contract fails as a contract error", async () => {
  const fake = recorder([
    new Response("<html>gateway</html>", {
      status: 200,
      headers: { "Content-Type": "text/html" },
    }),
  ]);
  await assert.rejects(
    request({ method: "POST", path: "/admin/v1/users", parse: identity }, fake.deps),
    (error: unknown) => error instanceof ApiError && error.kind === "contract"
  );
});

test("a canceled read is reported as canceled, not as a network failure", async () => {
  const controller = new AbortController();
  controller.abort();
  const fake = recorder([new DOMException("aborted", "AbortError")]);
  await assert.rejects(
    request(
      { method: "GET", path: "/admin/v1/users", parse: identity, signal: controller.signal },
      fake.deps
    ),
    (error: unknown) => error instanceof ApiError && error.kind === "canceled"
  );
});

test("sign-in posts to the session route and returns only the rendered identity", async () => {
  const fake = recorder([
    json(200, {
      user: {
        id: "u-1",
        tenantId: "t-1",
        email: "owner@example.com",
        name: "Owner",
        role: "owner",
      },
      expiresAt: "2026-09-06T12:00:00Z",
    }),
  ]);
  const user = await signIn("owner@example.com", "correct horse battery", fake.deps);

  assert.equal(fake.calls[0]?.url, "/api/session");
  assert.equal(fake.calls[0]?.method, "POST");
  assert.deepEqual(user, {
    id: "u-1",
    tenantId: "t-1",
    email: "owner@example.com",
    name: "Owner",
    role: "owner",
  });
});

test("a rejected sign-in reports invalid credentials, and never exposes a token", async () => {
  const fake = recorder([json(401, { error: "invalid_credentials" })]);
  await assert.rejects(
    signIn("owner@example.com", "wrong", fake.deps),
    (error: unknown) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.kind, "invalid_credentials");
      assert.equal(error.status, 401);
      assert.match(error.message, /邮箱或密码/);
      assert.doesNotMatch(error.message, /已失效/);
      return true;
    }
  );

  const withToken = recorder([
    json(200, {
      user: { id: "u-1", tenantId: "t-1", email: "e", name: "n", role: "owner" },
      token: "must-not-be-here",
    }),
  ]);
  const user = await signIn("owner@example.com", "correct", withToken.deps);
  assert.equal("token" in user, false, "the browser identity carries no token");
});

test("signing out succeeds even when the session was already gone", async () => {
  for (const status of [204, 401]) {
    const fake = recorder([json(status, null)]);
    await signOut(fake.deps);
    assert.equal(fake.calls[0]?.method, "DELETE", `status ${status}`);
  }

  const failing = recorder([json(500, {})]);
  await assert.rejects(
    signOut(failing.deps),
    (error: unknown) => error instanceof ApiError && error.kind === "server"
  );
});

test("a request goes to the entry point its surface names", async () => {
  const cases: {
    name: string;
    surface: "admin" | "operator" | undefined;
    path: string;
    want: string;
  }[] = [
    {
      name: "the tenant Admin API by default",
      surface: undefined,
      path: "/admin/v1/users",
      want: "/api/admin/admin/v1/users",
    },
    {
      name: "the tenant Admin API, named",
      surface: "admin",
      path: "/admin/v1/users",
      want: "/api/admin/admin/v1/users",
    },
    // The fleet must not be requested through the tenant entry point: that
    // one forwards with the signed-in session, and the fleet is not a
    // tenant's to read. Getting this wrong is a 404 rather than a leak, but
    // it is a 404 that looks like an empty fleet.
    //
    // 机群不能经由租户入口请求：那个入口用已登录会话转发，而机群不是某个租户可读的
    // 东西。弄错的结果是 404 而不是泄漏，但那是一个看起来像「机群为空」的 404。
    {
      name: "the operator entry point for the fleet",
      surface: "operator",
      path: "/operator/v1/nodes",
      want: "/api/operator/operator/v1/nodes",
    },
    {
      name: "the operator entry point for the model catalog",
      surface: "operator",
      path: "/operator/v1/models",
      want: "/api/operator/operator/v1/models",
    },
  ];

  for (const item of cases) {
    const fake = recorder([json(200, {})]);
    await request(
      { method: "GET", surface: item.surface, path: item.path, parse: identity },
      fake.deps
    );
    assert.equal(fake.calls[0]?.url, item.want, item.name);
  }
});

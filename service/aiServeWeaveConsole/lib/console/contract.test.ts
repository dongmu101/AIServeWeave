import assert from "node:assert/strict";
import test from "node:test";

import {
  parseApiKeys,
  parseCreatedApiKey,
  parseAuditEntries,
  parseJobHistoryDetail,
  parseJobHistoryEntries,
  parseLoginResult,
  parsePlatformOperators,
  parseRequestLogEntries,
  parseTenantLimits,
  parseTenantProfile,
  parseUsers,
} from "./contract.ts";
import { ApiError } from "./errors.ts";

/** userDocument is what the control plane's renderUser produces.
 *
 * userDocument 是控制面的 renderUser 所产生的文档。 */
function userDocument(overrides: Record<string, unknown> = {}) {
  return {
    id: "u-1",
    tenant_id: "t-1",
    email: "owner@example.com",
    name: "Owner",
    role: "owner",
    status: "active",
    last_login_at: "2026-09-06T10:00:00Z",
    created_at: "2026-09-01T09:00:00Z",
    ...overrides,
  };
}

test("a user page is read from the envelope the API returns", () => {
  const users = parseUsers({ items: [userDocument()] });
  assert.equal(users.items.length, 1);
  assert.equal(users.nextCursor, null, "an absent next_cursor means the last page");
  assert.deepEqual(users.items[0], {
    id: "u-1",
    tenantId: "t-1",
    email: "owner@example.com",
    name: "Owner",
    role: "owner",
    status: "active",
    lastLoginAt: "2026-09-06T10:00:00Z",
    createdAt: "2026-09-01T09:00:00Z",
  });
  assert.deepEqual(parseUsers({ items: [] }), { items: [], nextCursor: null });
});

test("a platform operator page keeps status without inventing a tenant role", () => {
  const page = parsePlatformOperators({
    items: [{
      id: "plt_1",
      email: "ops@example.com",
      name: "Ops",
      status: "active",
      created_at: "2026-09-09T01:00:00Z",
    }],
    next_cursor: "next",
  });
  assert.deepEqual(page, {
    items: [{
      id: "plt_1",
      email: "ops@example.com",
      name: "Ops",
      status: "active",
      lastLoginAt: null,
      createdAt: "2026-09-09T01:00:00Z",
    }],
    nextCursor: "next",
  });
});

test("next_cursor decides whether there is another page, not the page size", () => {
  const cases: { name: string; value: unknown; want: string | null }[] = [
    { name: "absent by omitempty", value: { items: [] }, want: null },
    { name: "explicitly null", value: { items: [], next_cursor: null }, want: null },
    { name: "empty string", value: { items: [], next_cursor: "" }, want: null },
    { name: "a cursor", value: { items: [], next_cursor: "MjAyNnwx" }, want: "MjAyNnwx" },
  ];
  for (const item of cases) {
    assert.equal(parseUsers(item.value).nextCursor, item.want, item.name);
  }
});

test("a list response that is not an envelope is refused", () => {
  const invalid: { name: string; value: unknown }[] = [
    { name: "the bare array this API used to return", value: [userDocument()] },
    { name: "items missing", value: {} },
    { name: "items not an array", value: { items: userDocument() } },
    { name: "a numeric cursor", value: { items: [], next_cursor: 12 } },
  ];
  for (const item of invalid) {
    assert.throws(() => parseUsers(item.value), ApiError, item.name);
  }
});

test("an omitted last_login_at means never signed in, not a missing field", () => {
  const [user] = parseUsers({ items: [userDocument({ last_login_at: undefined })] }).items;
  assert.equal(user?.lastLoginAt, null);
});

test("a user document the Console cannot trust is refused", () => {
  const cases: { name: string; value: unknown }[] = [
    { name: "a null entry", value: { items: [null] } },
    { name: "an unknown role", value: { items: [userDocument({ role: "superuser" })] } },
    { name: "a numeric id", value: { items: [userDocument({ id: 1 })] } },
    { name: "a missing created_at", value: { items: [userDocument({ created_at: undefined })] } },
    { name: "an unparseable created_at", value: { items: [userDocument({ created_at: "yesterday" })] } },
    { name: "an unparseable last_login_at", value: { items: [userDocument({ last_login_at: "soon" })] } },
  ];

  for (const item of cases) {
    assert.throws(
      () => parseUsers(item.value),
      (error: unknown) =>
        error instanceof ApiError && error.kind === "contract",
      `${item.name}: expected a contract error`
    );
  }
});

test("a key list keeps the display form and the optional timestamps", () => {
  const keys = parseApiKeys({ items: [
    {
      id: "k-1",
      tenant_id: "t-1",
      name: "prod",
      display: "aisw_...c0de",
      status: "active",
      created_by: "u-1",
      created_at: "2026-09-01T09:00:00Z",
    },
    {
      id: "k-2",
      tenant_id: "t-1",
      name: "revoked",
      display: "aisw_...beef",
      status: "revoked",
      created_by: "u-2",
      expires_at: "2026-12-01T09:00:00Z",
      last_used_at: "2026-09-05T09:00:00Z",
      revoked_at: "2026-09-06T09:00:00Z",
      created_at: "2026-09-01T09:00:00Z",
    },
  ] }).items;

  assert.deepEqual(
    [keys[0]?.expiresAt, keys[0]?.lastUsedAt, keys[0]?.revokedAt],
    [null, null, null]
  );
  assert.equal(keys[1]?.display, "aisw_...beef");
  assert.equal(keys[1]?.revokedAt, "2026-09-06T09:00:00Z");
});

test("audit entries keep the fields the API actually returns", () => {
  const entries = parseAuditEntries({ items: [
    {
      id: "a-1",
      actor_id: "u-1",
      action: "apikey.create",
      target: "k-1",
      detail: "name=prod",
      ip: "127.0.0.1",
      created_at: "2026-09-06T09:00:00Z",
    },
  ] }).items;
  assert.equal(entries[0]?.actorId, "u-1");
  assert.equal(entries[0]?.action, "apikey.create");
  assert.throws(() => parseAuditEntries({ items: [{ id: "a-1" }] }), ApiError);
});

test("request log entries keep the fields the API actually returns", () => {
  const page = parseRequestLogEntries({
    items: [
      {
        request_id: "req_1",
        key_display: "aisw-abcd1234",
        endpoint: "chat",
        status_code: 200,
        outcome: "ok",
        duration_ms: 842,
        created_at: "2026-09-11T08:00:00Z",
      },
    ],
    next_cursor: "",
  });
  assert.deepEqual(page.items[0], {
    requestId: "req_1",
    tenantId: "",
    keyDisplay: "aisw-abcd1234",
    endpoint: "chat",
    statusCode: 200,
    outcome: "ok",
    durationMs: 842,
    createdAt: "2026-09-11T08:00:00Z",
  });
  assert.equal(page.nextCursor, null);
});

test("a request log entry the Console cannot trust is refused", () => {
  assert.throws(() => parseRequestLogEntries({ items: [{ request_id: "req_1" }] }), ApiError);
});

test("a job history list entry omits artifacts, which the detail endpoint fills in", () => {
  const entries = parseJobHistoryEntries({
    items: [
      {
        job_id: "job_1",
        workflow_id: "text-to-image",
        state: "succeeded",
        created_at: "2026-09-06T09:00:00Z",
        updated_at: "2026-09-06T09:05:00Z",
        terminal_at: "2026-09-06T09:05:00Z",
      },
    ],
  }).items;
  assert.equal(entries[0]?.jobId, "job_1");
  assert.equal(entries[0]?.workflowVersion, "", "omitted workflow_version reads as empty");
  assert.equal(entries[0]?.artifacts, null, "the list endpoint sends no artifacts field");

  const detail = parseJobHistoryDetail({
    job_id: "job_1",
    workflow_id: "text-to-image",
    state: "succeeded",
    created_at: "2026-09-06T09:00:00Z",
    updated_at: "2026-09-06T09:05:00Z",
    artifacts: [
      {
        artifact_id: "art_1",
        job_id: "job_1",
        tenant_id: "t-1",
        filename: "out.png",
        type: "output",
        created_at: "2026-09-06T09:05:00Z",
      },
    ],
  });
  assert.equal(detail.artifacts?.length, 1);
  assert.equal(detail.artifacts?.[0]?.artifactId, "art_1");
  assert.equal(detail.terminalAt, null, "an omitted terminal_at means the run has not finished");

  assert.throws(
    () => parseJobHistoryEntries({ items: [{ job_id: "job_1" }] }),
    ApiError,
    "a job history entry missing required fields is refused"
  );
});

test("an omitted limit is zero, and zero means unlimited", () => {
  const cases: {
    name: string;
    value: unknown;
    want: { requestsPerMinute: number; tokensPerMinute: number; maxConcurrent: number };
  }[] = [
    {
      name: "every dimension omitted by omitempty",
      value: {},
      want: { requestsPerMinute: 0, tokensPerMinute: 0, maxConcurrent: 0 },
    },
    {
      name: "one dimension set",
      value: { requests_per_minute: 600 },
      want: { requestsPerMinute: 600, tokensPerMinute: 0, maxConcurrent: 0 },
    },
    {
      name: "all three set",
      value: { requests_per_minute: 600, tokens_per_minute: 90000, max_concurrent: 8 },
      want: { requestsPerMinute: 600, tokensPerMinute: 90000, maxConcurrent: 8 },
    },
  ];

  for (const item of cases) {
    assert.deepEqual(parseTenantLimits(item.value), item.want, item.name);
  }

  for (const invalid of [{ max_concurrent: -1 }, { max_concurrent: 1.5 }, { max_concurrent: "8" }]) {
    assert.throws(
      () => parseTenantLimits(invalid),
      ApiError,
      `expected a contract error for ${JSON.stringify(invalid)}`
    );
  }
});

test("a login response must carry a token and a user", () => {
  const login = parseLoginResult({
    token: "jwt",
    expires_at: "2026-09-06T12:00:00Z",
    user: userDocument(),
  });
  assert.equal(login.token, "jwt");
  assert.equal(login.user.tenantId, "t-1");

  const invalid: { name: string; value: unknown }[] = [
    { name: "empty token", value: { token: "", expires_at: "2026-09-06T12:00:00Z", user: userDocument() } },
    { name: "missing user", value: { token: "jwt", expires_at: "2026-09-06T12:00:00Z" } },
    { name: "missing expiry", value: { token: "jwt", user: userDocument() } },
  ];
  for (const item of invalid) {
    assert.throws(() => parseLoginResult(item.value), ApiError, item.name);
  }
});

test("a created key carries the plaintext once, alongside its display form", () => {
  const created = parseCreatedApiKey({
    key: "aisw-5Z0klVEabcdefghijklmnopqrstuvwxyz0123456789",
    api_key: {
      id: "k-1",
      tenant_id: "t-1",
      name: "prod",
      display: "aisw-5Z0klVEa",
      status: "active",
      created_by: "u-1",
      expires_at: "2026-12-05T00:00:00Z",
      created_at: "2026-09-06T00:00:00Z",
    },
  });

  assert.equal(created.plaintext.startsWith("aisw-"), true);
  assert.equal(created.key.display, "aisw-5Z0klVEa");
  // The display form is a prefix of the plaintext, which is exactly why a
  // Console must never assemble one itself: only the control plane knows how
  // much of the secret it is safe to show.
  //
  // display 形式是明文的前缀，这正是 Console 绝不能自行拼接它的原因：只有控制面知道
  // 这个秘密可以安全地露出多少。
  assert.equal(created.plaintext.startsWith(created.key.display), true);

  const invalid: { name: string; value: unknown }[] = [
    { name: "no plaintext", value: { api_key: {} } },
    { name: "empty plaintext", value: { key: "", api_key: {} } },
    { name: "no key record", value: { key: "aisw-x" } },
  ];
  for (const item of invalid) {
    assert.throws(() => parseCreatedApiKey(item.value), ApiError, item.name);
  }
});

test("a key created with no expiry omits the field entirely", () => {
  // This is what the control plane actually returns for ttl_seconds < 0:
  // `expires_at` carries omitempty, so "never expires" arrives as an absent
  // field, not as null and not as a far-future date.
  //
  // 这是控制面对 ttl_seconds < 0 实际返回的样子：`expires_at` 带 omitempty，因此
  // 「永不过期」是以字段缺席的形式到达的，既不是 null，也不是一个很远的未来日期。
  const created = parseCreatedApiKey({
    key: "aisw-neverexpires000000000000000000000000000",
    api_key: {
      id: "k-2",
      tenant_id: "t-1",
      name: "forever",
      display: "aisw-neverexp",
      status: "active",
      created_by: "u-1",
      created_at: "2026-09-06T00:00:00Z",
    },
  });
  assert.equal(created.key.expiresAt, null);
});

test("a tenant profile carries the tenant and its quota, and refuses half of one", () => {
  const profile = parseTenantProfile({
    tenant: {
      id: "tnt_1",
      name: "Acme",
      status: "active",
      created_at: "2026-09-01T09:00:00Z",
    },
    // An unconfigured tenant's limits arrive as `{}`: every field carries
    // omitempty, so unlimited is encoded by absence.
    //
    // 未配置配额的租户，其 limits 以 `{}` 到达：每个字段都带 omitempty，因此「不限制」
    // 是用缺席来编码的。
    limits: {},
  });
  assert.equal(profile.tenant.name, "Acme");
  assert.deepEqual(profile.limits, {
    requestsPerMinute: 0,
    tokensPerMinute: 0,
    maxConcurrent: 0,
  });

  const invalid: { name: string; value: unknown }[] = [
    {
      name: "limits missing entirely",
      value: { tenant: { id: "t", name: "n", status: "active", created_at: "2026-09-01T09:00:00Z" } },
    },
    { name: "tenant missing", value: { limits: {} } },
    { name: "an unparseable created_at", value: { tenant: { id: "t", name: "n", status: "active", created_at: "soon" }, limits: {} } },
  ];
  for (const item of invalid) {
    assert.throws(() => parseTenantProfile(item.value), ApiError, item.name);
  }
});

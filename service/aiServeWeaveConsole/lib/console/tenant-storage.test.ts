import assert from "node:assert/strict";
import test from "node:test";

import { request } from "./api-client.ts";
import { ApiError } from "./errors.ts";
import { resolveUpstream } from "./upstream-routes.ts";
import {
  buildPutBody,
  checkDraft,
  deleteStorageRequest,
  draftFromStorage,
  emptyDraft,
  locationChanged,
  secretsKept,
  parseRetention,
  parseStorageState,
  parseTenantStorage,
  putStorageRequest,
  sanitizeStorageError,
  storageErrorMessage,
  storageRequest,
  type StorageDraft,
} from "./tenant-storage.ts";

const s3View = {
  kind: "s3",
  s3: { endpoint: "https://s3.example.com", region: "us-east-1", bucket: "b", prefix: "aisw", path_style: true, access_key_id_last4: "ABCD" },
  webdav: null, retention_days: 30, updated_by: "usr_1", updated_at: "2026-11-27T00:00:00Z",
};
const davView = {
  kind: "webdav", s3: null, webdav: { url: "https://nas.example.com/dav", dir: "aisw", username: "u" },
  retention_days: 0, updated_by: "usr_1", updated_at: "2026-11-27T00:00:00Z",
};
const savedS3 = parseTenantStorage(s3View);
const savedDav = parseTenantStorage(davView);

const s3Draft = (patch: Partial<StorageDraft> = {}): StorageDraft => ({
  ...emptyDraft(), kind: "s3", bucket: "b", accessKeyId: "AK", secretAccessKey: "SK", ...patch,
});
const davDraft = (patch: Partial<StorageDraft> = {}): StorageDraft => ({
  ...emptyDraft(), kind: "webdav", webdavUrl: "https://nas.example.com/dav", webdavUsername: "u", webdavPassword: "pw", ...patch,
});

test("parseStorageState reads every documented state", () => {
  assert.deepEqual(parseStorageState({ enabled: false }), { enabled: false, allowPrivateEndpoints: false, storage: null });
  assert.deepEqual(parseStorageState({ enabled: true, allow_private_endpoints: true, storage: null }), { enabled: true, allowPrivateEndpoints: true, storage: null });
  const s3 = parseStorageState({ enabled: true, allow_private_endpoints: false, storage: s3View }).storage;
  assert.equal(s3?.s3?.accessKeyIdLast4, "ABCD");
  assert.equal(s3?.s3?.pathStyle, true);
  assert.equal(s3?.webdav, null);
  assert.equal(s3?.retentionDays, 30);
  const dav = parseTenantStorage(davView);
  assert.equal(dav.webdav?.username, "u");
  assert.equal(dav.s3, null);
  // A response with `s3`/`webdav` keys left out reads as null, like an explicit null.
  assert.equal(parseTenantStorage({ ...davView, s3: undefined }).s3, null);
});

test("parse rejects contract violations", () => {
  for (const [name, value] of [
    ["not an object", []],
    ["enabled missing", {}],
    ["enabled without the private flag", { enabled: true, storage: null }],
    ["storage not an object", { enabled: true, allow_private_endpoints: false, storage: "x" }],
  ] as const) {
    assert.throws(() => parseStorageState(value), ApiError, name);
  }
  for (const [name, value] of [
    ["unknown kind", { ...s3View, kind: "gcs" }],
    ["kind without its block", { ...s3View, s3: null }],
    ["both blocks", { ...s3View, webdav: davView.webdav }],
    ["other kind's block", { ...davView, s3: null, webdav: null }],
    ["negative retention", { ...s3View, retention_days: -1 }],
    ["retention over the limit", { ...s3View, retention_days: 36501 }],
    ["fractional retention", { ...s3View, retention_days: 1.5 }],
    ["retention as text", { ...s3View, retention_days: "30" }],
    ["long key tail", { ...s3View, s3: { ...s3View.s3, access_key_id_last4: "123456789" } }],
    ["path_style missing", { ...s3View, s3: { ...s3View.s3, path_style: undefined } }],
    ["bucket not a string", { ...s3View, s3: { ...s3View.s3, bucket: 1 } }],
    ["bad timestamp", { ...s3View, updated_at: "yesterday" }],
    ["webdav url missing", { ...davView, webdav: { dir: "", username: "u" } }],
  ] as const) {
    assert.throws(() => parseTenantStorage(value), ApiError, name);
  }
  assert.equal(parseTenantStorage({ ...s3View, retention_days: 36500 }).retentionDays, 36500);
});

test("draftFromStorage fills the location and never a secret", () => {
  assert.deepEqual(draftFromStorage(null), emptyDraft());
  const s3 = draftFromStorage(savedS3);
  assert.equal(s3.kind, "s3");
  assert.equal(s3.endpoint, "https://s3.example.com");
  assert.equal(s3.pathStyle, true);
  assert.equal(s3.retentionDays, "30");
  assert.equal(s3.accessKeyId, "");
  assert.equal(s3.secretAccessKey, "");
  const dav = draftFromStorage(savedDav);
  assert.equal(dav.kind, "webdav");
  assert.equal(dav.webdavUsername, "u");
  assert.equal(dav.webdavPassword, "");
});

test("locationChanged follows the control plane's rule for keeping secrets", () => {
  const same = draftFromStorage(savedS3);
  for (const [name, draft, saved, want] of [
    ["unchanged", same, savedS3, false],
    ["no saved configuration", s3Draft(), null, false],
    ["not using storage", { ...same, kind: "none" }, savedS3, false],
    ["endpoint", { ...same, endpoint: "https://other.example.com" }, savedS3, true],
    ["endpoint padded with spaces only", { ...same, endpoint: " https://s3.example.com " }, savedS3, false],
    ["region", { ...same, region: "eu-west-1" }, savedS3, true],
    ["bucket", { ...same, bucket: "c" }, savedS3, true],
    ["prefix does not move it", { ...same, prefix: "elsewhere" }, savedS3, false],
    ["path style does not move it", { ...same, pathStyle: false }, savedS3, false],
    ["retention does not move it", { ...same, retentionDays: "1" }, savedS3, false],
    ["kind", davDraft(), savedS3, true],
    ["webdav unchanged", draftFromStorage(savedDav), savedDav, false],
    ["webdav url", { ...draftFromStorage(savedDav), webdavUrl: "https://nas.example.com/other" }, savedDav, true],
    ["webdav dir does not move it", { ...draftFromStorage(savedDav), webdavDir: "x" }, savedDav, false],
    ["webdav user does not move it", { ...draftFromStorage(savedDav), webdavUsername: "x" }, savedDav, false],
  ] as const) {
    assert.equal(locationChanged(draft, saved), want, name);
  }
});

test("secretsKept also needs the same WebDAV user name", () => {
  const dav = draftFromStorage(savedDav);
  assert.equal(secretsKept(dav, savedDav), true);
  assert.equal(secretsKept({ ...dav, webdavUsername: "someone-else" }, savedDav), false);
  assert.equal(secretsKept({ ...dav, webdavDir: "x" }, savedDav), true);
  assert.equal(secretsKept(draftFromStorage(savedS3), savedS3), true);
  assert.equal(secretsKept({ ...draftFromStorage(savedS3), bucket: "c" }, savedS3), false);
  assert.equal(secretsKept(s3Draft(), null), false);
  assert.equal(checkDraft({ ...dav, webdavUsername: "someone-else", webdavPassword: "" }, savedDav, false), "credentials");
});

test("parseRetention accepts only whole days in range", () => {
  for (const [text, want] of [
    ["0", 0], ["30", 30], [" 7 ", 7], ["36500", 36500], ["36501", null], ["-1", null], ["1.5", null], ["", null], ["abc", null], ["1e2", null], ["123456", null],
  ] as const) {
    assert.equal(parseRetention(text), want, text);
  }
});

test("checkDraft covers the form's rules", () => {
  const unchanged = draftFromStorage(savedS3);
  for (const [name, draft, saved, allowPrivate, want] of [
    ["not using storage needs nothing", emptyDraft(), null, false, null],
    ["valid new s3", s3Draft(), null, false, null],
    ["empty endpoint means AWS", s3Draft({ endpoint: "" }), null, false, null],
    ["bucket required", s3Draft({ bucket: " " }), null, false, "bucket"],
    ["endpoint without a scheme", s3Draft({ endpoint: "s3.example.com" }), null, false, "url"],
    ["endpoint with another scheme", s3Draft({ endpoint: "ftp://s3.example.com" }), null, false, "url"],
    ["http refused without private endpoints", s3Draft({ endpoint: "http://s3.example.com" }), null, false, "https"],
    ["http accepted with private endpoints", s3Draft({ endpoint: "http://10.0.0.5:9000" }), null, true, null],
    ["https accepted", s3Draft({ endpoint: "https://s3.example.com" }), null, false, null],
    ["new s3 needs the key id", s3Draft({ accessKeyId: "" }), null, false, "credentials"],
    ["new s3 needs the secret", s3Draft({ secretAccessKey: "  " }), null, false, "credentials"],
    ["retention out of range", s3Draft({ retentionDays: "36501" }), null, false, "retention"],
    ["retention empty", s3Draft({ retentionDays: "" }), null, false, "retention"],
    ["unchanged location keeps secrets", unchanged, savedS3, false, null],
    ["unchanged location accepts one secret", { ...unchanged, secretAccessKey: "new" }, savedS3, false, null],
    ["moved location needs secrets", { ...unchanged, bucket: "c" }, savedS3, false, "credentials"],
    ["moved location with one secret", { ...unchanged, bucket: "c", accessKeyId: "AK" }, savedS3, false, "credentials"],
    ["moved location with both secrets", { ...unchanged, bucket: "c", accessKeyId: "AK", secretAccessKey: "SK" }, savedS3, false, null],
    ["switching kind needs secrets", davDraft({ webdavPassword: "" }), savedS3, false, "credentials"],
    ["valid new webdav", davDraft(), null, false, null],
    ["webdav url required", davDraft({ webdavUrl: "" }), null, false, "url"],
    ["webdav http refused", davDraft({ webdavUrl: "http://nas.example.com" }), null, false, "https"],
    ["new webdav needs the password", davDraft({ webdavPassword: "" }), null, false, "credentials"],
    ["unchanged webdav keeps the password", { ...draftFromStorage(savedDav) }, savedDav, false, null],
    ["webdav url change needs the password", { ...draftFromStorage(savedDav), webdavUrl: "https://nas.example.com/x" }, savedDav, false, "credentials"],
  ] as const) {
    assert.equal(checkDraft(draft, saved, allowPrivate), want, name);
  }
});

test("buildPutBody omits empty secrets and the other kind's block", () => {
  assert.deepEqual(buildPutBody(s3Draft({ endpoint: " https://s3.example.com ", region: "r", prefix: "p", pathStyle: true, retentionDays: "30", accessKeyId: " AK ", secretAccessKey: "SK\n" })), {
    kind: "s3",
    s3: { endpoint: "https://s3.example.com", region: "r", bucket: "b", prefix: "p", path_style: true, access_key_id: "AK", secret_access_key: "SK" },
    retention_days: 30,
  });
  // Keeping the saved secrets: both fields are left out, not sent empty or null.
  const keep = buildPutBody({ ...draftFromStorage(savedS3), accessKeyId: "", secretAccessKey: "" });
  assert.deepEqual(Object.keys(keep.s3 as object).sort(), ["bucket", "endpoint", "path_style", "prefix", "region"]);
  assert.equal("webdav" in keep, false);
  // Replacing one secret leaves the other out.
  const one = buildPutBody({ ...draftFromStorage(savedS3), secretAccessKey: "new" });
  assert.equal((one.s3 as Record<string, unknown>).secret_access_key, "new");
  assert.equal("access_key_id" in (one.s3 as object), false);
  // An empty endpoint is sent as "" (AWS), never dropped.
  assert.equal((buildPutBody(s3Draft()).s3 as Record<string, unknown>).endpoint, "");

  assert.deepEqual(buildPutBody(davDraft({ webdavDir: " d ", retentionDays: "0" })), {
    kind: "webdav", webdav: { url: "https://nas.example.com/dav", dir: "d", username: "u", password: "pw" }, retention_days: 0,
  });
  const davKeep = buildPutBody({ ...draftFromStorage(savedDav), webdavPassword: "" });
  assert.equal("password" in (davKeep.webdav as object), false);
  assert.equal("s3" in davKeep, false);
  // A password is sent as typed, spaces included.
  assert.equal((buildPutBody(davDraft({ webdavPassword: " p w " })).webdav as Record<string, unknown>).password, " p w ");
});

test("request builders use the allowlisted route", () => {
  assert.equal(storageRequest().method, "GET");
  assert.equal(storageRequest().path, "/admin/v1/storage");
  const put = putStorageRequest(s3Draft());
  assert.equal(put.method, "PUT");
  assert.equal(put.path, "/admin/v1/storage");
  assert.ok((put.timeoutMs ?? 0) > 15_000, "a save outlasts the default request timeout");
  assert.equal(deleteStorageRequest().method, "DELETE");
  assert.equal(deleteStorageRequest().parse(null), null);
  for (const method of ["GET", "PUT", "DELETE"]) {
    assert.equal(resolveUpstream(method, ["admin", "v1", "storage"], new URLSearchParams("tenant_id=other"))?.path, "/admin/v1/storage", method);
    assert.equal(resolveUpstream(method, ["admin", "v1", "storage"], new URLSearchParams("tenant_id=other"))?.search, "", `${method}: no query passes`);
  }
  assert.equal(resolveUpstream("POST", ["admin", "v1", "storage"], new URLSearchParams()), null);
  assert.equal(resolveUpstream("GET", ["admin", "v1", "storage", "extra"], new URLSearchParams()), null);
});

test("sanitizeStorageError passes only the fixed vocabulary", () => {
  assert.deepEqual(sanitizeStorageError('{"error":"leaked internals","code":"invalid_storage"}'), { code: "invalid_storage" });
  assert.deepEqual(sanitizeStorageError('{"code":"credentials_required"}'), { code: "credentials_required" });
  assert.deepEqual(sanitizeStorageError('{"code":"endpoint_not_allowed","reason":"other"}'), { code: "endpoint_not_allowed" });
  assert.deepEqual(sanitizeStorageError('{"code":"storage_unreachable","reason":"auth"}'), { code: "storage_unreachable", reason: "auth" });
  assert.deepEqual(sanitizeStorageError('{"code":"storage_unreachable","reason":"dial tcp 10.0.0.1: refused"}'), { code: "storage_unreachable" });
  assert.deepEqual(sanitizeStorageError('{"code":"storage_unreachable"}'), { code: "storage_unreachable" });
  for (const body of ["", "not json", "[]", "null", '{"code":"something_new"}', '{"error":"x"}', '{"code":1}']) {
    assert.equal(sanitizeStorageError(body), null, body);
  }
});

test("storageErrorMessage maps every code and reason to fixed copy", () => {
  const bad = (code?: string, reason?: string, status = 400) => new ApiError("invalid", status, { code, reason });
  assert.equal(storageErrorMessage(bad("invalid_storage")), "tenant.storageErrInvalid");
  assert.equal(storageErrorMessage(bad("credentials_required")), "tenant.storageErrCredentials");
  assert.equal(storageErrorMessage(bad("endpoint_not_allowed")), "tenant.storageErrEndpoint");
  for (const [reason, key] of [
    ["network", "tenant.storageReasonNetwork"],
    ["auth", "tenant.storageReasonAuth"],
    ["not_found", "tenant.storageReasonNotFound"],
    ["permission", "tenant.storageReasonPermission"],
    ["endpoint_not_allowed", "tenant.storageErrEndpoint"],
    ["other", "tenant.storageReasonOther"],
    ["something_new", "tenant.storageReasonOther"],
    [undefined, "tenant.storageReasonOther"],
  ] as const) {
    assert.equal(storageErrorMessage(bad("storage_unreachable", reason)), key, String(reason));
  }
  // Anything else falls back to the generic copy of the failure's kind.
  assert.equal(storageErrorMessage(bad("something_new")), null);
  assert.equal(storageErrorMessage(bad()), null);
  assert.equal(storageErrorMessage(new ApiError("forbidden", 403, { code: "invalid_storage" })), null);
  assert.equal(storageErrorMessage(new ApiError("timeout")), null);
  assert.equal(storageErrorMessage(new Error("boom")), null);
  assert.equal(storageErrorMessage(null), null);
});

test("the request layer carries a 400's code and reason, and a save is not retried", async () => {
  const calls: { url: string; method: string; body: unknown }[] = [];
  const fetchImpl = (async (url: string, init: RequestInit) => {
    calls.push({ url, method: String(init.method), body: init.body === undefined ? undefined : JSON.parse(String(init.body)) });
    return new Response(JSON.stringify({ error: "upstream_error", code: "storage_unreachable", reason: "permission" }), { status: 400 });
  }) as unknown as typeof fetch;
  await assert.rejects(request(putStorageRequest(s3Draft()), { fetchImpl, sleep: async () => {} }), (error: unknown) => {
    assert.ok(error instanceof ApiError);
    assert.equal(error.status, 400);
    assert.equal(error.code, "storage_unreachable");
    assert.equal(error.reason, "permission");
    assert.equal(storageErrorMessage(error), "tenant.storageReasonPermission");
    return true;
  });
  assert.equal(calls.length, 1, "a write is sent once");
  assert.equal(calls[0].url, "/api/admin/admin/v1/storage");
  assert.equal(calls[0].method, "PUT");
  assert.equal((calls[0].body as { kind: string }).kind, "s3");
});

test("the request layer drops a code that is not a short identifier", async () => {
  const fetchImpl = (async () => new Response(JSON.stringify({ code: "<script>alert(1)</script>", reason: "x".repeat(100) }), { status: 400 })) as unknown as typeof fetch;
  await assert.rejects(request(putStorageRequest(s3Draft()), { fetchImpl }), (error: unknown) => {
    assert.ok(error instanceof ApiError);
    assert.equal(error.code, "");
    assert.equal(error.reason, "");
    return true;
  });
  const notJson = (async () => new Response("<html>bad</html>", { status: 400 })) as unknown as typeof fetch;
  await assert.rejects(request(putStorageRequest(s3Draft()), { fetchImpl: notJson }), (error: unknown) => error instanceof ApiError && error.kind === "invalid" && error.code === "");
});

test("the request layer treats 404 and 204 as the control plane documents them", async () => {
  const notFound = (async () => new Response("{}", { status: 404 })) as unknown as typeof fetch;
  await assert.rejects(request(storageRequest(), { fetchImpl: notFound }), (error: unknown) => error instanceof ApiError && error.kind === "not_found");
  const noContent = (async () => new Response(null, { status: 204 })) as unknown as typeof fetch;
  assert.equal(await request(deleteStorageRequest(), { fetchImpl: noContent }), null);
});

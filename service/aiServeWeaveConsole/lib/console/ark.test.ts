import assert from "node:assert/strict";
import test from "node:test";

import { request } from "./api-client.ts";
import { ApiError } from "./errors.ts";
import { resolveOperatorUpstream, resolveUpstream } from "./upstream-routes.ts";
import {
  DEFAULT_ARK_BASE_URL,
  arkErrorMessage,
  arkRequest,
  arkTasksRequest,
  buildArkPutBody,
  checkArkDraft,
  deleteArkRequest,
  deleteArkTaskRequest,
  draftFromCredential,
  isArkPath,
  keyKept,
  normalizeBaseUrl,
  pageCount,
  parseArkCredential,
  parseArkState,
  parseArkTask,
  parseArkTaskPage,
  putArkRequest,
  sanitizeArkError,
  splitModels,
  taskAction,
  type ArkDraft,
} from "./ark.ts";

const view = { key_last4: "abcd", base_url: "", models: ["doubao-seedance-1-0-pro"], updated_by: "usr_1", updated_at: "2026-11-27T00:00:00Z" };
const saved = parseArkCredential(view);
const draft = (patch: Partial<ArkDraft> = {}): ArkDraft => ({ apiKey: "", baseUrl: "", models: "", ...patch });
const task = { id: "cgt-1", model: "m", status: "queued", completion_tokens: 0, created_at: "2026-11-27T00:00:00Z" };

test("parseArkState reads every documented state", () => {
  assert.deepEqual(parseArkState({ enabled: false, credential: null, platform_available: false }), { enabled: false, credential: null, platformAvailable: false });
  assert.deepEqual(parseArkState({ enabled: false }), { enabled: false, credential: null, platformAvailable: false });
  assert.deepEqual(parseArkState({ enabled: true, credential: null, platform_available: true }), { enabled: true, credential: null, platformAvailable: true });
  const own = parseArkState({ enabled: true, credential: view, platform_available: false });
  assert.deepEqual(own.credential, { keyLast4: "abcd", baseUrl: "", models: ["doubao-seedance-1-0-pro"], updatedBy: "usr_1", updatedAt: "2026-11-27T00:00:00Z" });
  // Missing optional pieces read as empty, like explicit nulls.
  assert.deepEqual(parseArkCredential({ key_last4: "", models: null, updated_at: "2026-11-27T00:00:00Z" }).models, []);
});

test("parse rejects contract violations", () => {
  const cases: [string, () => unknown][] = [
    ["not an object", () => parseArkState([])],
    ["enabled missing", () => parseArkState({})],
    ["platform_available not boolean", () => parseArkState({ enabled: true, platform_available: "yes" })],
    ["credential not an object", () => parseArkState({ enabled: true, credential: "x" })],
    ["last4 too long", () => parseArkCredential({ ...view, key_last4: "123456789" })],
    ["models not an array", () => parseArkCredential({ ...view, models: "m" })],
    ["a model not text", () => parseArkCredential({ ...view, models: [1] })],
    ["too many models", () => parseArkCredential({ ...view, models: Array.from({ length: 101 }, (_, i) => `m${i}`) })],
    ["bad time", () => parseArkCredential({ ...view, updated_at: "yesterday" })],
    ["task without id", () => parseArkTask({ ...task, id: "" })],
    ["task status not text", () => parseArkTask({ ...task, status: 1 })],
    ["negative duration", () => parseArkTask({ ...task, duration: -1 })],
    ["fractional tokens", () => parseArkTask({ ...task, completion_tokens: 1.5 })],
    ["bad created_at", () => parseArkTask({ ...task, created_at: "soon" })],
    ["items missing", () => parseArkTaskPage({ total: 0, page_num: 1, page_size: 20 })],
    ["page_num zero", () => parseArkTaskPage({ total: 0, page_num: 0, page_size: 20, items: [] })],
    ["page_size above the limit", () => parseArkTaskPage({ total: 0, page_num: 1, page_size: 101, items: [] })],
    ["total negative", () => parseArkTaskPage({ total: -1, page_num: 1, page_size: 20, items: [] })],
  ];
  for (const [name, run] of cases) {
    assert.throws(run, (error: unknown) => error instanceof ApiError && error.kind === "contract", name);
  }
});

test("tasks parse with optional fields and unknown states", () => {
  const page = parseArkTaskPage({ total: 41, page_num: 2, page_size: 20, items: [task, { id: "cgt-2", status: "paused" }] });
  assert.equal(page.total, 41);
  assert.equal(page.items[0].status, "queued");
  assert.equal(page.items[0].createdAt, "2026-11-27T00:00:00Z");
  assert.deepEqual(page.items[1], {
    id: "cgt-2", model: "", status: "unknown", errorCode: "", resolution: "", ratio: "", duration: 0, completionTokens: 0, createdAt: "", updatedAt: "",
  });
  const full = parseArkTask({ ...task, status: "failed", error_code: "OutputVideoSensitiveContentDetected", resolution: "720p", ratio: "16:9", duration: 5 });
  assert.equal(full.errorCode, "OutputVideoSensitiveContentDetected");
  assert.equal(full.duration, 5);
  assert.equal(pageCount(41, 20), 3);
  assert.equal(pageCount(0, 20), 1);
  assert.equal(pageCount(20, 20), 1);
});

test("taskAction allows cancel only when queued and delete only when finished", () => {
  assert.equal(taskAction("queued"), "cancel");
  assert.equal(taskAction("running"), null);
  assert.equal(taskAction("unknown"), null);
  for (const status of ["succeeded", "failed", "cancelled", "expired"] as const) assert.equal(taskAction(status), "delete", status);
});

test("splitModels trims, splits on lines and commas, and de-duplicates", () => {
  assert.deepEqual(splitModels(""), []);
  assert.deepEqual(splitModels(" a \n\nb,c\r\na , b "), ["a", "b", "c"]);
  assert.equal(draftFromCredential(saved).models, "doubao-seedance-1-0-pro");
  assert.deepEqual(draftFromCredential(null), { apiKey: "", baseUrl: "", models: "" });
});

test("base URLs normalise like the control plane", () => {
  assert.equal(normalizeBaseUrl(""), "");
  assert.equal(normalizeBaseUrl(` ${DEFAULT_ARK_BASE_URL}/ `), "");
  assert.equal(normalizeBaseUrl("https://ark.example.com/api/v3//"), "https://ark.example.com/api/v3");
  assert.equal(keyKept(draft(), saved), true);
  assert.equal(keyKept(draft({ baseUrl: DEFAULT_ARK_BASE_URL }), saved), true);
  assert.equal(keyKept(draft({ baseUrl: "https://other.example.com" }), saved), false, "a saved key never follows a new host");
  assert.equal(keyKept(draft(), null), false);
});

test("checkArkDraft mirrors the control plane's input rules", () => {
  const cases: [string, "admin" | "operator", ArkDraft, typeof saved | null, string | null][] = [
    ["new key", "admin", draft({ apiKey: " sk-1 " }), null, null],
    ["first save needs a key", "admin", draft(), null, "key"],
    ["kept key", "admin", draft({ models: "m" }), saved, null],
    ["a tenant's base URL is ignored", "admin", draft({ baseUrl: "not a url" }), saved, null],
    ["key with a space", "admin", draft({ apiKey: "sk 1" }), null, "key"],
    ["key with non-ASCII", "admin", draft({ apiKey: "密钥" }), null, "key"],
    ["key too long", "admin", draft({ apiKey: "k".repeat(513) }), null, "key"],
    ["key at the limit", "admin", draft({ apiKey: "k".repeat(512) }), null, null],
    ["operator custom base", "operator", draft({ apiKey: "k", baseUrl: "https://ark.example.com/api/v3" }), null, null],
    ["operator base changes: key required", "operator", draft({ baseUrl: "https://ark.example.com" }), saved, "key"],
    ["operator base not http", "operator", draft({ apiKey: "k", baseUrl: "ftp://ark.example.com" }), null, "base_url"],
    ["operator base with query", "operator", draft({ apiKey: "k", baseUrl: "https://ark.example.com?x=1" }), null, "base_url"],
    ["operator base with user", "operator", draft({ apiKey: "k", baseUrl: "https://u:p@ark.example.com" }), null, "base_url"],
    ["operator base not a URL", "operator", draft({ apiKey: "k", baseUrl: "ark" }), null, "base_url"],
    ["model with a space", "admin", draft({ apiKey: "k", models: "a b" }), null, "models"],
    ["model with a template", "admin", draft({ apiKey: "k", models: "{{inputs.m}}" }), null, "models"],
    ["model too long", "admin", draft({ apiKey: "k", models: "m".repeat(257) }), null, "models"],
    ["model at the limit", "admin", draft({ apiKey: "k", models: "m".repeat(256) }), null, null],
    ["too many models", "admin", draft({ apiKey: "k", models: Array.from({ length: 101 }, (_, i) => `m${i}`).join("\n") }), null, "models"],
    ["100 models", "admin", draft({ apiKey: "k", models: Array.from({ length: 100 }, (_, i) => `m${i}`).join("\n") }), null, null],
  ];
  for (const [name, surface, d, s, want] of cases) assert.equal(checkArkDraft(surface, d, s), want, name);
});

test("buildArkPutBody leaves out an empty key and a tenant's base URL", () => {
  assert.deepEqual(buildArkPutBody("admin", draft({ apiKey: " sk ", baseUrl: "https://x", models: "a\nb" })), { models: ["a", "b"], api_key: "sk" });
  assert.deepEqual(buildArkPutBody("admin", draft()), { models: [] });
  assert.deepEqual(buildArkPutBody("operator", draft({ baseUrl: `${DEFAULT_ARK_BASE_URL}/` })), { models: [], base_url: "" });
  assert.deepEqual(buildArkPutBody("operator", draft({ apiKey: "k", baseUrl: "https://ark.example.com/" })), { models: [], api_key: "k", base_url: "https://ark.example.com" });
});

test("requests name the right surface and every path is allowlisted there", () => {
  for (const surface of ["admin", "operator"] as const) {
    const specs = [
      arkRequest(surface), putArkRequest(surface, draft({ apiKey: "k" })), deleteArkRequest(surface),
      arkTasksRequest(surface, { pageNum: 2, status: "queued" }), deleteArkTaskRequest(surface, "cgt-2025.1"),
    ];
    const resolve = surface === "admin" ? resolveUpstream : resolveOperatorUpstream;
    for (const spec of specs) {
      assert.equal(spec.surface, surface);
      assert.ok(spec.path.startsWith(`/${surface}/v1/ark`));
      assert.ok(isArkPath(surface, spec.path), spec.path);
      const query = new URLSearchParams(spec.query ?? {});
      assert.ok(resolve(spec.method, spec.path.split("/").filter(Boolean), query), `${spec.method} ${spec.path}`);
    }
  }
  const put = putArkRequest("admin", draft({ apiKey: "k" }));
  assert.equal(put.method, "PUT");
  assert.ok((put.timeoutMs ?? 0) >= 30_000, "the key check may take 20 seconds");
  assert.deepEqual(arkTasksRequest("admin", { pageNum: 0, status: "" }).query, { page_num: "1", page_size: "20" });
  assert.deepEqual(arkTasksRequest("admin", { pageNum: 3.7, status: "expired" }).query, { page_num: "3", page_size: "20", status: "expired" });
  assert.equal(deleteArkTaskRequest("admin", "a/b").path, "/admin/v1/ark/tasks/a%2Fb");
  assert.equal(isArkPath("admin", "/admin/v1/arkx"), false);
  assert.equal(isArkPath("admin", "/operator/v1/ark"), false);
  assert.equal(isArkPath("admin", "/admin/v1/storage"), false);
});

test("sanitizeArkError passes on only the documented vocabulary", () => {
  assert.deepEqual(sanitizeArkError(JSON.stringify({ error: "secret upstream text", code: "ark_unreachable", reason: "auth" })), { code: "ark_unreachable", reason: "auth" });
  assert.deepEqual(sanitizeArkError(JSON.stringify({ code: "ark_unreachable", reason: "dns" })), { code: "ark_unreachable" });
  assert.deepEqual(sanitizeArkError(JSON.stringify({ code: "invalid_ark", reason: "auth" })), { code: "invalid_ark" });
  assert.deepEqual(sanitizeArkError(JSON.stringify({ code: "task_not_cancelable" })), { code: "task_not_cancelable" });
  assert.deepEqual(sanitizeArkError(JSON.stringify({ code: "key_required" })), { code: "key_required" });
  assert.equal(sanitizeArkError(JSON.stringify({ code: "invalid_storage" })), null);
  assert.equal(sanitizeArkError("not json"), null);
  assert.equal(sanitizeArkError("[]"), null);
  assert.equal(sanitizeArkError(""), null);
});

test("arkErrorMessage maps codes to fixed copy and falls back otherwise", () => {
  assert.equal(arkErrorMessage(new ApiError("invalid", 400, { code: "invalid_ark" })), "tenant.arkErrInvalid");
  assert.equal(arkErrorMessage(new ApiError("invalid", 400, { code: "key_required" })), "tenant.arkErrKeyRequired");
  assert.equal(arkErrorMessage(new ApiError("invalid", 400, { code: "task_not_cancelable" })), "tenant.arkErrNotCancelable");
  assert.equal(arkErrorMessage(new ApiError("server", 503, { code: "ark_unreachable", reason: "network" })), "tenant.arkReasonNetwork");
  assert.equal(arkErrorMessage(new ApiError("server", 503, { code: "ark_unreachable", reason: "auth" })), "tenant.arkReasonAuth");
  assert.equal(arkErrorMessage(new ApiError("server", 503, { code: "ark_unreachable", reason: "rate_limited" })), "tenant.arkReasonRateLimited");
  assert.equal(arkErrorMessage(new ApiError("server", 503, { code: "ark_unreachable" })), "tenant.arkReasonOther");
  assert.equal(arkErrorMessage(new ApiError("server", 503)), null);
  assert.equal(arkErrorMessage(new ApiError("forbidden", 403, { code: "invalid_ark" })), null);
  assert.equal(arkErrorMessage(new ApiError("invalid", 400, { code: "something_else" })), null);
  assert.equal(arkErrorMessage(new Error("x")), null);
});

test("a 503 answer's code and reason reach the caller, and a save is sent once", async () => {
  let calls = 0;
  const fetchImpl = (async () => {
    calls += 1;
    return new Response(JSON.stringify({ error: "upstream_error", code: "ark_unreachable", reason: "rate_limited" }), { status: 503, headers: { "content-type": "application/json" } });
  }) as typeof fetch;
  await assert.rejects(
    request(putArkRequest("admin", draft({ apiKey: "k" })), { fetchImpl, sleep: async () => {} }),
    (error: unknown) => error instanceof ApiError && error.status === 503 && error.code === "ark_unreachable" && error.reason === "rate_limited",
  );
  assert.equal(calls, 1, "a write is never retried");
});

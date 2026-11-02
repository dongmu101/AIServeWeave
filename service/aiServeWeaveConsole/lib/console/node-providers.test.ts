import assert from "node:assert/strict";
import test from "node:test";

import { ApiError } from "./errors.ts";
import {
  checkProviderDraft,
  deleteNodeProviderRequest,
  isRemoteId,
  parseNodeProviderWrite,
  parseNodeProviders,
  putNodeProviderRequest,
  type NodeProvider,
  type ProviderDraft,
} from "./node-providers.ts";

const provider = {
  id: "remote-openai", kind: "openai", base_url: "", models: ["gpt-5"], key_last4: "abcd",
  updated_by: "usr_1", updated_at: "2026-11-02T00:00:00Z", state: "active",
};

test("parseNodeProviders accepts the documented shape", () => {
  const parsed = parseNodeProviders({
    version: 3, connected: true, items: [provider],
    report: { applied_version: 3, attempted_version: 3, outcome: "applied", updated_at: "2026-11-02T00:00:01Z" },
  });
  assert.equal(parsed.items[0].id, "remote-openai");
  assert.equal(parsed.items[0].state, "active");
  assert.equal(parsed.report?.outcome, "applied");
  assert.deepEqual(parseNodeProviders({ version: 0, connected: false, items: [] }).report, null);
});

test("parseNodeProviders rejects contract violations", () => {
  for (const [name, value] of [
    ["not an object", []],
    ["missing connected", { version: 1, items: [] }],
    ["negative version", { version: -1, connected: true, items: [] }],
    ["id without prefix", { version: 1, connected: true, items: [{ ...provider, id: "openai" }] }],
    ["duplicate ids", { version: 1, connected: true, items: [provider, provider] }],
    ["unknown state", { version: 1, connected: true, items: [{ ...provider, state: "weird" }] }],
    ["unknown outcome", { version: 1, connected: true, items: [], report: { applied_version: 1, attempted_version: 1, outcome: "x" } }],
    ["long key tail", { version: 1, connected: true, items: [{ ...provider, key_last4: "123456789" }] }],
    ["too many", { version: 1, connected: true, items: Array.from({ length: 33 }, (_, i) => ({ ...provider, id: `remote-${i}` })) }],
  ] as const) {
    assert.throws(() => parseNodeProviders(value), ApiError, name);
  }
});

test("parseNodeProviderWrite accepts put and delete answers", () => {
  assert.equal(parseNodeProviderWrite({ item: provider, version: 2, delivered: true }).item?.id, "remote-openai");
  assert.equal(parseNodeProviderWrite({ version: 3, delivered: false }).item, null);
  assert.throws(() => parseNodeProviderWrite({ version: 3 }), ApiError);
});

test("isRemoteId mirrors remoteprovider.ValidID", () => {
  for (const [id, want] of [
    ["remote-a", true], ["remote-a.b_c-1", true], ["remote-", false], ["openai", false],
    ["remote-a/b", false], ["remote-" + "x".repeat(57), true], ["remote-" + "x".repeat(58), false],
  ] as const) {
    assert.equal(isRemoteId(id), want, id);
  }
});

const saved: NodeProvider = {
  id: "remote-openai", kind: "openai", baseUrl: "", models: [], keyLast4: "abcd", updatedBy: "u", updatedAt: "2026-11-02T00:00:00Z", state: "active",
};
const draft = (patch: Partial<ProviderDraft>): ProviderDraft => ({ id: "remote-openai", kind: "openai", baseUrl: "", apiKey: "sk-test", models: "", ...patch });

test("checkProviderDraft mirrors the control plane's input rules", () => {
  for (const [name, d, s, want] of [
    ["valid new", draft({}), null, null],
    ["bad id", draft({ id: "openai" }), null, "id"],
    ["local kind", draft({ kind: "ollama" }), null, "kind"],
    ["compatible needs url", draft({ kind: "openai_compatible" }), null, "base_url"],
    ["http refused", draft({ baseUrl: "http://api.example.com/v1" }), null, "base_url"],
    ["loopback refused", draft({ baseUrl: "https://127.0.0.1/v1" }), null, "base_url"],
    ["private refused", draft({ baseUrl: "https://10.1.2.3/v1" }), null, "base_url"],
    ["localhost refused", draft({ baseUrl: "https://localhost/v1" }), null, "base_url"],
    ["ipv6 loopback refused", draft({ baseUrl: "https://[::1]/v1" }), null, "base_url"],
    ["credentials in url refused", draft({ baseUrl: "https://u:p@api.example.com" }), null, "base_url"],
    ["public https accepted", draft({ kind: "openai_compatible", baseUrl: "https://api.example.com/v1" }), null, null],
    ["new needs key", draft({ apiKey: "" }), null, "key_required"],
    ["keep key unchanged", draft({ apiKey: "" }), saved, null],
    ["kind change needs key", draft({ apiKey: "", kind: "deepseek" }), saved, "key_required"],
    ["url change needs key", draft({ apiKey: "", baseUrl: "https://api.example.com/v1" }), saved, "key_required"],
    ["multiline key", draft({ apiKey: "a\nb" }), null, "key"],
    ["padded key", draft({ apiKey: " sk" }), null, "key"],
    ["duplicate model", draft({ models: "a, a" }), null, "models"],
    ["models split", draft({ models: "a,\nb" }), null, null],
  ] as const) {
    assert.equal(checkProviderDraft(d, s), want, name);
  }
});

test("request builders target the chosen surface and omit an empty key", () => {
  const put = putNodeProviderRequest("admin", "tnode.t.0123", draft({ apiKey: "", models: "a, b" }));
  assert.equal(put.surface, "admin");
  assert.equal(put.method, "PUT");
  assert.equal(put.path, "/admin/v1/nodes/tnode.t.0123/providers/remote-openai");
  assert.deepEqual(put.body, { kind: "openai", base_url: "", models: ["a", "b"] });
  const withKey = putNodeProviderRequest("operator", "gpu 1", draft({}));
  assert.equal(withKey.path, "/operator/v1/nodes/gpu%201/providers/remote-openai");
  assert.equal((withKey.body as Record<string, unknown>).api_key, "sk-test");
  assert.equal(deleteNodeProviderRequest("operator", "n1", "remote-x").method, "DELETE");
});

import assert from "node:assert/strict";
import test from "node:test";

import {
  deriveSessionKey,
  isExpired,
  open,
  seal,
  type SessionPayload,
} from "./session-payload.ts";

const key = deriveSessionKey("a-development-secret-of-at-least-32-chars");
const other = deriveSessionKey("a-different-secret-of-at-least-32-chars!!");

const payload: SessionPayload = {
  token: "control-plane.jwt.value",
  expiresAt: "2026-09-06T12:00:00Z",
  user: {
    id: "u-1",
    tenantId: "t-1",
    email: "owner@example.com",
    name: "Owner",
    role: "owner",
  },
};

test("a sealed session opens to what went in", () => {
  const opened = open(seal(payload, key), key);
  assert.deepEqual(opened, payload);
});

test("sealing twice produces different ciphertexts", () => {
  // Equal outputs would mean a fixed nonce, which in GCM is a break rather
  // than an inefficiency.
  //
  // 输出相同意味着 nonce 固定，而在 GCM 中那是被攻破，不是效率问题。
  assert.notEqual(seal(payload, key), seal(payload, key));
});

test("the control plane token is not readable from the cookie value", () => {
  const sealed = seal(payload, key);
  assert.equal(sealed.includes(payload.token), false, sealed);
  assert.equal(
    Buffer.from(sealed.slice(sealed.indexOf(".") + 1), "base64url")
      .toString("utf8")
      .includes("owner@example.com"),
    false
  );
});

test("open refuses every cookie this server did not issue", () => {
  const sealed = seal(payload, key);
  const body = sealed.slice(sealed.indexOf(".") + 1);
  const bytes = Buffer.from(body, "base64url");
  const flipped = Buffer.from(bytes);
  flipped[flipped.length - 1] ^= 0x01;

  const cases: { name: string; value: string; key?: Buffer }[] = [
    { name: "empty", value: "" },
    { name: "no version separator", value: body },
    { name: "unknown version", value: `v2.${body}` },
    { name: "not base64url", value: "v1.!!!!" },
    { name: "truncated", value: `v1.${bytes.subarray(0, 20).toString("base64url")}` },
    { name: "tampered ciphertext", value: `v1.${flipped.toString("base64url")}` },
    { name: "sealed with another key", value: sealed, key: other },
  ];

  for (const item of cases) {
    assert.equal(
      open(item.value, item.key ?? key),
      null,
      `${item.name}: expected null`
    );
  }
});

test("open refuses a document that is not a session", () => {
  // These are sealed with the real key: the check being exercised is the shape
  // check, which is what stops a valid cookie from a different feature — or a
  // future format — from being read as an identity.
  //
  // 这些都是用真密钥密封的：这里检验的是形状检查，正是它阻止了来自另一项功能——或将来
  // 某个格式——的有效 cookie 被当作一个身份来读取。
  const documents = [
    "null",
    '"a string"',
    '{"token":"t"}',
    '{"token":"t","expiresAt":"2026-09-06T12:00:00Z"}',
    '{"token":"t","expiresAt":"2026-09-06T12:00:00Z","user":{"id":"u-1"}}',
    '{"token":1,"expiresAt":"2026-09-06T12:00:00Z","user":{"id":"u-1","tenantId":"t","email":"e","name":"n","role":"owner"}}',
  ];

  for (const document of documents) {
    const forged = sealRaw(document);
    assert.equal(open(forged, key), null, `expected null for ${document}`);
  }
});

test("isExpired compares against the injected time, not the wall clock", () => {
  const cases: { name: string; expiresAt: string; now: string; want: boolean }[] = [
    { name: "well before expiry", expiresAt: "2026-09-06T12:00:00Z", now: "2026-09-06T11:59:00Z", want: false },
    { name: "one second before", expiresAt: "2026-09-06T12:00:00Z", now: "2026-09-06T11:59:59Z", want: false },
    { name: "exactly at expiry", expiresAt: "2026-09-06T12:00:00Z", now: "2026-09-06T12:00:00Z", want: true },
    { name: "after expiry", expiresAt: "2026-09-06T12:00:00Z", now: "2026-09-06T12:00:01Z", want: true },
    { name: "unparseable expiry", expiresAt: "not-a-time", now: "2026-09-06T11:00:00Z", want: true },
  ];

  for (const item of cases) {
    assert.equal(
      isExpired({ ...payload, expiresAt: item.expiresAt }, new Date(item.now)),
      item.want,
      `${item.name}: expires ${item.expiresAt} at ${item.now}`
    );
  }
});

/** sealRaw seals an arbitrary document with the test key, so a shape check can
 * be exercised without a valid payload.
 *
 * sealRaw 用测试密钥密封任意文档，好在没有合法载荷的情况下检验形状检查。 */
function sealRaw(document: string): string {
  const parsed = JSON.parse(document) as unknown;
  return seal(parsed as SessionPayload, key);
}

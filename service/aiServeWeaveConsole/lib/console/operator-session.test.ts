import { test } from "node:test";
import assert from "node:assert/strict";
import { open, seal, deriveSessionKey } from "./session-payload.ts";
import { openOperator, sealOperator, parseOperatorLogin, operatorTarget } from "./operator-session.ts";

const key = deriveSessionKey("operator-test-secret-long-enough-123456");
const now = new Date("2026-09-08T00:00:00Z");
const session = { token: "private-upstream-token", expiresAt: "2026-09-08T01:00:00Z", operator: { id: "pop_1", email: "ops@example.test", name: "Ops" } };

test("platform cookies round trip, expire and reject tampering or tenant substitution", () => {
  const cookie = sealOperator(session, key);
  assert.deepEqual(openOperator(cookie, key, now), session);
  assert.equal(openOperator(cookie, key, new Date(session.expiresAt)), null);
  assert.equal(openOperator(cookie.slice(0, -8) + "AAAAAAAA", key, now), null);
  assert.equal(openOperator(cookie, Buffer.alloc(32), now), null);
  assert.equal(open(cookie, key), null);
  const tenant = seal({ token: "tenant", expiresAt: session.expiresAt, user: { id: "usr_1", tenantId: "tnt_1", role: "owner", email: "ops@example.test", name: "Ops" } }, key);
  assert.equal(openOperator(tenant, key, now), null);
});

test("platform login accepts only its own active identity and a valid future expiry", () => {
  const wire = { token: session.token, expires_at: session.expiresAt, operator: { ...session.operator, status: "active" } };
  assert.deepEqual(parseOperatorLogin(wire, now), session);
  for (const value of [null, {...wire, user: wire.operator, operator: undefined}, {...wire, token: ""}, {...wire, expires_at: "invalid"}, {...wire, expires_at: now.toISOString()}, {...wire, operator: {...wire.operator, status: "disabled"}}]) {
    assert.throws(() => parseOperatorLogin(value, now), /invalid platform session/, "invalid platform login must fail");
  }
});

test("operator navigation never crosses to tenant, login or external paths", () => {
  for (const value of [undefined, "//evil.test", "/console", "/operator/login", "/operator/../console", "/operator/\\evil", "/operator/%2e%2e/console", "/operator/login?next=x"]) {
    assert.equal(operatorTarget(value), "/operator/fleet", `target ${value}`);
  }
  assert.equal(operatorTarget("/operator/audit?action=node.disable"), "/operator/audit?action=node.disable");
});

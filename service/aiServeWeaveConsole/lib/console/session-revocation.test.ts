import assert from "node:assert/strict";
import test from "node:test";

import { logoutDisposition } from "./session-revocation.ts";

test("logout clears locally only after upstream revocation or an already invalid session", () => {
  const cases = [
    { name: "no local session", result: null, want: { clear: true, status: 204, error: null } },
    { name: "revoked", result: { kind: "response" as const, status: 204 }, want: { clear: true, status: 204, error: null } },
    { name: "already invalid", result: { kind: "response" as const, status: 401 }, want: { clear: true, status: 204, error: null } },
    { name: "Redis unavailable", result: { kind: "response" as const, status: 503 }, want: { clear: false, status: 503, error: "upstream" } },
    { name: "timeout", result: { kind: "timeout" as const }, want: { clear: false, status: 504, error: "upstream" } },
    { name: "unreachable", result: { kind: "unreachable" as const }, want: { clear: false, status: 502, error: "upstream" } },
  ];
  for (const item of cases) {
    assert.deepEqual(logoutDisposition(item.result), item.want, item.name);
  }
});

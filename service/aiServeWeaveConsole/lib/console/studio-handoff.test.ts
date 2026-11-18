import assert from "node:assert/strict";
import { test } from "node:test";

import { handoffKey, openTicket, sealTicket, TICKET_TTL_MS, type HandoffTicket } from "./studio-handoff.ts";

const key = handoffKey("x".repeat(32));
const now = new Date("2026-11-17T00:00:00Z");
const ticket: HandoffTicket = {
  token: "session-token",
  expiresAt: "2026-11-18T00:00:00Z",
  notAfter: new Date(now.getTime() + TICKET_TTL_MS).toISOString(),
  user: { id: "usr_1", tenantId: "tnt_1", email: "a@example.com", name: "A", role: "owner" },
};

test("a sealed ticket opens with the same key before it lapses", () => {
  assert.deepEqual(openTicket(sealTicket(ticket, key), key, now), ticket);
});

test("a ticket is refused with another key, after notAfter, or tampered", () => {
  const sealed = sealTicket(ticket, key);
  assert.equal(openTicket(sealed, handoffKey("y".repeat(32)), now), null);
  assert.equal(openTicket(sealed, key, new Date(now.getTime() + TICKET_TTL_MS)), null);
  assert.equal(openTicket(sealed.slice(0, -2) + "AA", key, now), null);
  assert.equal(openTicket("v1." + sealed.slice(3), key, now), null);
});

test("a ticket claiming a longer life than the TTL is refused", () => {
  const long = { ...ticket, notAfter: new Date(now.getTime() + TICKET_TTL_MS * 10).toISOString() };
  assert.equal(openTicket(sealTicket(long, key), key, now), null);
});

test("a short secret is refused", () => {
  assert.throws(() => handoffKey("short"));
});

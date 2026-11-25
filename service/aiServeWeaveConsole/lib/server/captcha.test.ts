import { test } from "node:test";
import assert from "node:assert/strict";
import { pbkdf2, solveChallenge } from "altcha/lib";

import {
  CHALLENGE_TTL_SECONDS,
  issueCaptchaChallenge,
  memoryReplayGuard,
  verifyCaptcha,
  type CaptchaSecrets,
} from "./captcha.ts";

const SECRETS: CaptchaSecrets = { signature: "sig-secret", keySignature: "key-secret" };

/** solvedPayload issues and solves a challenge, encoded as the widget would. */
async function solvedPayload(nowMs = Date.now(), secrets = SECRETS): Promise<string> {
  const challenge = await issueCaptchaChallenge(secrets, nowMs);
  const solution = await solveChallenge({ challenge, deriveKey: pbkdf2.deriveKey });
  assert.ok(solution);
  return Buffer.from(JSON.stringify({ challenge: { parameters: challenge.parameters, signature: challenge.signature }, solution })).toString("base64");
}

function encode(value: unknown): string {
  return Buffer.from(JSON.stringify(value)).toString("base64");
}

test("issued challenge carries an expiry one TTL ahead", async () => {
  const now = Date.UTC(2026, 0, 1);
  const challenge = await issueCaptchaChallenge(SECRETS, now);
  assert.equal(challenge.parameters.expiresAt, now / 1000 + CHALLENGE_TTL_SECONDS);
  assert.equal(typeof challenge.signature, "string");
});

test("a fresh solution verifies once and is refused on replay", async () => {
  const guard = memoryReplayGuard();
  const payload = await solvedPayload();
  assert.equal(await verifyCaptcha(payload, SECRETS, guard), true);
  assert.equal(await verifyCaptcha(payload, SECRETS, guard), false);
});

test("a solution signed with other secrets is refused", async () => {
  const payload = await solvedPayload(Date.now(), { signature: "other", keySignature: "other-key" });
  assert.equal(await verifyCaptcha(payload, SECRETS, memoryReplayGuard()), false);
});

test("an expired challenge is refused", async () => {
  const issued = Date.now() - (CHALLENGE_TTL_SECONDS + 5) * 1000;
  const payload = await solvedPayload(issued);
  assert.equal(await verifyCaptcha(payload, SECRETS, memoryReplayGuard()), false);
});

test("tampered parameters or solution are refused", async () => {
  const decoded = JSON.parse(Buffer.from(await solvedPayload(), "base64").toString());
  const longer = structuredClone(decoded);
  longer.challenge.parameters.expiresAt += 3600;
  const wrongKey = structuredClone(decoded);
  wrongKey.solution.derivedKey = "00".repeat(wrongKey.solution.derivedKey.length / 2);
  const unsigned = structuredClone(decoded);
  delete unsigned.challenge.signature;
  const noExpiry = structuredClone(decoded);
  delete noExpiry.challenge.parameters.expiresAt;
  for (const [name, value] of Object.entries({ longer, wrongKey, unsigned, noExpiry })) {
    assert.equal(await verifyCaptcha(encode(value), SECRETS, memoryReplayGuard()), false, name);
  }
});

test("malformed payloads are refused", async () => {
  const cases: Array<[string, unknown]> = [
    ["undefined", undefined],
    ["null", null],
    ["number", 42],
    ["empty", ""],
    ["not base64 json", "%%%"],
    ["json array", encode([])],
    ["json null", encode(null)],
    ["no solution", encode({ challenge: { parameters: {} } })],
    ["no parameters", encode({ challenge: {}, solution: {} })],
    ["string challenge", encode({ challenge: "x", solution: {} })],
  ];
  for (const [name, payload] of cases) {
    assert.equal(await verifyCaptcha(payload, SECRETS, memoryReplayGuard()), false, name);
  }
});

test("replay guard forgets entries once they expire", () => {
  const guard = memoryReplayGuard();
  assert.equal(guard.spend("a", 100, 50), true);
  assert.equal(guard.spend("a", 100, 99), false);
  assert.equal(guard.spend("b", 300, 101), true);
  assert.equal(guard.spend("a", 400, 101), true);
});

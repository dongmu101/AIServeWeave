import assert from "node:assert/strict";
import test from "node:test";
import { kindForStatus } from "./errors.ts";
import { localSignupProblem, parseSignupBody, signupErrorKind } from "./signup.ts";

const valid = { tenantName: "A", email: "synthetic@example.invalid", password: "abcdefgh", confirmPassword: "abcdefgh" };
test("signup validation translates the reason without changing submitted values", () => {
  assert.equal(localSignupProblem({ ...valid, tenantName: "" }, "en-US"), "Enter a tenant name.");
  assert.equal(localSignupProblem({ ...valid, password: "x" }, "en-US"), "Password must contain at least 8 characters.");
  assert.equal(localSignupProblem({ ...valid, confirmPassword: "different" }, "en-US"), "The passwords do not match.");
  assert.equal(localSignupProblem(valid, "ja-JP"), null);
});

test("signup body accepts an optional string captcha and nothing else in its place", () => {
  const base = { tenantName: "A", email: "synthetic@example.invalid", password: "abcdefgh" };
  const cases: Array<[string, unknown, unknown]> = [
    ["without captcha", base, base],
    ["with captcha", { ...base, captcha: "cGF5bG9hZA==" }, { ...base, captcha: "cGF5bG9hZA==" }],
    ["empty captcha is kept for the route to refuse", { ...base, captcha: "" }, { ...base, captcha: "" }],
    ["numeric captcha", { ...base, captcha: 1 }, null],
    ["null captcha", { ...base, captcha: null }, null],
    ["object captcha", { ...base, captcha: {} }, null],
  ];
  for (const [name, body, want] of cases) {
    assert.deepEqual(parseSignupBody(JSON.stringify(body)), want, name);
  }
});

test("a 428 from the sign-up route is a failed human check", () => {
  assert.equal(signupErrorKind(428, kindForStatus), "captcha_failed");
});

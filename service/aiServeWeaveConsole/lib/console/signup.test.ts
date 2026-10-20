import assert from "node:assert/strict";
import test from "node:test";
import { localSignupProblem } from "./signup.ts";

const valid = { tenantName: "A", email: "synthetic@example.invalid", password: "abcdefgh", confirmPassword: "abcdefgh" };
test("signup validation translates the reason without changing submitted values", () => {
  assert.equal(localSignupProblem({ ...valid, tenantName: "" }, "en-US"), "Enter a tenant name.");
  assert.equal(localSignupProblem({ ...valid, password: "x" }, "en-US"), "Password must contain at least 8 characters.");
  assert.equal(localSignupProblem({ ...valid, confirmPassword: "different" }, "en-US"), "The passwords do not match.");
  assert.equal(localSignupProblem(valid, "ja-JP"), null);
});

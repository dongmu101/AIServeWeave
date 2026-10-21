import assert from "node:assert/strict";
import test from "node:test";
import { ApiError, describe } from "./errors.ts";

test("API failures use fixed locale copy and never expose upstream text", () => {
  assert.equal(describe(new ApiError("forbidden"), "en-US"), "Your current role does not have permission to perform this action.");
  const secret = new Error("Authorization: Bearer synthetic-secret /internal/private");
  assert.equal(describe(secret, "en-US"), "The server could not process the request. Please try again later.");
  assert.doesNotMatch(describe(secret, "de-DE"), /Authorization|synthetic-secret|internal/);
});

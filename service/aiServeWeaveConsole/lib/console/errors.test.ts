import assert from "node:assert/strict";
import test from "node:test";
import { ApiError, describe, kindForStatus } from "./errors.ts";

test("API failures use fixed locale copy and never expose upstream text", () => {
  assert.equal(describe(new ApiError("forbidden"), "en-US"), "Your current role does not have permission to perform this action.");
  const secret = new Error("Authorization: Bearer synthetic-secret /internal/private");
  assert.equal(describe(secret, "en-US"), "The server could not process the request. Please try again later.");
  assert.doesNotMatch(describe(secret, "de-DE"), /Authorization|synthetic-secret|internal/);
});

test("428 is the failed human check, with its own copy", () => {
  assert.equal(kindForStatus(428), "captcha_failed");
  assert.equal(describe(new ApiError("captcha_failed"), "en-US"), "Human verification failed or expired. Please verify again.");
  assert.equal(describe(new ApiError("captcha_failed"), "zh-CN"), "人机验证未通过或已过期，请重新验证。");
});

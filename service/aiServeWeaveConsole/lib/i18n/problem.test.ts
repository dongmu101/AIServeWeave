import assert from "node:assert/strict";
import test from "node:test";
import { ApiError, describe } from "../console/errors.ts";
import { draftWriteProblem } from "./problem.ts";
import { loadMessages } from "./messages.ts";
import { createTranslator } from "./translate.ts";

test("a retained write failure translates its cause and draft notice together", async () => {
  const error = new ApiError("forbidden");
  const problem = draftWriteProblem(error);
  const t = createTranslator("en-US", await loadMessages("en-US"));
  const text = t(problem.key, { value: describe(problem.failure, "en-US"), ...problem.values });
  assert.match(text, /current role/);
  assert.match(text, /draft/);
  assert.equal(problem.failure, error);
  assert.equal(draftWriteProblem(new ApiError("conflict")).failure, undefined);
});

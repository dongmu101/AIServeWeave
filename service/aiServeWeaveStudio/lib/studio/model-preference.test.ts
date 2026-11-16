import assert from "node:assert/strict";
import { test } from "node:test";

import { pickModel } from "./model-preference.ts";

const models = [{ id: "a" }, { id: "b" }];

test("pickModel keeps a stored choice that is still offered", () => {
  assert.equal(pickModel(models, "b"), "b");
});

test("pickModel falls back to the first model", () => {
  assert.equal(pickModel(models, "gone"), "a");
  assert.equal(pickModel(models, null), "a");
});

test("pickModel is empty when nothing is offered", () => {
  assert.equal(pickModel([], "a"), "");
});

import assert from "node:assert/strict";
import { test } from "node:test";

import { chooseModel, pickModel } from "./model-preference.ts";

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

test("chooseModel takes the first candidate still offered", () => {
  const models = [{ id: "a" }, { id: "b" }];
  assert.equal(chooseModel(models, null, "gone", "b", "a"), "b");
  assert.equal(chooseModel(models, undefined, "gone"), "a");
  assert.equal(chooseModel([], "a"), "");
});

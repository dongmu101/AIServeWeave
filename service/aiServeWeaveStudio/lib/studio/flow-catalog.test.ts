import assert from "node:assert/strict";
import { test } from "node:test";

import { ContractError } from "./contract.ts";
import { parseImageModels, parseWorkflowTemplates } from "./flow-catalog.ts";

test("parseImageModels reads the models and whether the deployment default can run", () => {
  assert.deepEqual(parseImageModels({ data: [{ id: "flux", name: "Flux" }, { id: "sd" }], default_available: true }), {
    models: [{ id: "flux", name: "Flux" }, { id: "sd", name: "sd" }],
    defaultAvailable: true,
  });
  assert.deepEqual(parseImageModels({ data: [], default_available: false }), { models: [], defaultAvailable: false });
});

test("parseImageModels tolerates an empty or missing list and flag, and drops duplicate ids", () => {
  assert.deepEqual(parseImageModels({ data: null }), { models: [], defaultAvailable: false });
  assert.deepEqual(parseImageModels({}), { models: [], defaultAvailable: false });
  assert.equal(parseImageModels({ data: [{ id: "a", name: "A" }, { id: "a", name: "again" }] }).models.length, 1);
});

test("parseImageModels refuses shapes it does not know", () => {
  for (const bad of [null, [], "x", { data: {} }, { data: [{ name: "no id" }] }, { data: [], default_available: "yes" }, { data: [], default_available: 1 }]) {
    assert.throws(() => parseImageModels(bad), ContractError, JSON.stringify(bad));
  }
});

test("parseWorkflowTemplates reads inputs with their types, bounds and defaults", () => {
  const templates = parseWorkflowTemplates({
    data: [
      {
        id: "portrait",
        description: "人像",
        inputs: [
          { name: "prompt", type: "string", required: true, max_length: 500 },
          { name: "steps", type: "integer", required: false, default: 20, min: 1, max: 50 },
          { name: "cfg", type: "number", required: false, default: 7.5 },
          { name: "hires", type: "boolean", required: false, default: false },
          { name: "ref", type: "file", required: true },
        ],
        outputs: [{ name: "image", type: "image", description: "成图" }, { name: "extra", type: "file" }],
      },
    ],
  });
  assert.equal(templates.length, 1);
  assert.equal(templates[0].description, "人像");
  assert.deepEqual(templates[0].inputs[0], { name: "prompt", type: "string", required: true, maxLength: 500 });
  assert.deepEqual(templates[0].inputs[1], { name: "steps", type: "integer", required: false, default: 20, min: 1, max: 50 });
  assert.equal(templates[0].inputs[2].default, 7.5);
  assert.equal(templates[0].inputs[3].default, false);
  assert.deepEqual(templates[0].inputs[4], { name: "ref", type: "file", required: true });
  assert.deepEqual(templates[0].outputs, [
    { name: "image", type: "image", description: "成图" },
    { name: "extra", type: "file", description: "" },
  ]);
});

test("parseWorkflowTemplates tolerates missing optional parts, and drops duplicate ids", () => {
  assert.deepEqual(parseWorkflowTemplates({ data: null }), []);
  assert.deepEqual(parseWorkflowTemplates({ data: [] }), []);
  assert.deepEqual(parseWorkflowTemplates({ data: [{ id: "bare" }] }), [{ id: "bare", description: "", inputs: [], outputs: [] }]);
  assert.equal(parseWorkflowTemplates({ data: [{ id: "a" }, { id: "a", description: "again" }] }).length, 1);
  assert.equal(parseWorkflowTemplates({ data: [{ id: "a", inputs: [{ name: "p", type: "string", required: false, default: { x: 1 } }] }] })[0].inputs[0].default, undefined);
});

test("parseWorkflowTemplates refuses shapes it does not know", () => {
  const bad: unknown[] = [
    null,
    { data: {} },
    { data: [{}] },
    { data: [{ id: 5 }] },
    { data: [{ id: "a", inputs: {} }] },
    { data: [{ id: "a", inputs: [{ name: "", type: "string" }] }] },
    { data: [{ id: "a", inputs: [{ name: "p", type: "bytes" }] }] },
    { data: [{ id: "a", inputs: [{ name: "p", type: "string", max_length: "5" }] }] },
    { data: [{ id: "a", inputs: [{ name: "p", type: "integer", min: null, max: Infinity }] }] },
    { data: [{ id: "a", outputs: [{ type: "image" }] }] },
  ];
  for (const value of bad) assert.throws(() => parseWorkflowTemplates(value), ContractError, JSON.stringify(value));
});

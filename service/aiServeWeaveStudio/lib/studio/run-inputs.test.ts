import assert from "node:assert/strict";
import { test } from "node:test";

import { newDefinition } from "./flow-graph.ts";
import type { FlowInput } from "./flows.ts";
import { buildRunInputs, flowFile, initialFormValues, readFlowFile } from "./run-inputs.ts";

const inputs: FlowInput[] = [
  { name: "topic", type: "text", required: true },
  { name: "tone", type: "text", required: false, default: "plain" },
  { name: "count", type: "number", required: true },
  { name: "strict", type: "boolean", required: false },
];

test("the form starts from defaults, or from a previous run", () => {
  assert.deepEqual(initialFormValues(inputs), { topic: "", tone: "plain", count: "", strict: false });
  assert.deepEqual(initialFormValues(inputs, { topic: "cats", count: 3, strict: true }), { topic: "cats", tone: "plain", count: "3", strict: true });
});

test("buildRunInputs checks required fields and numbers", () => {
  assert.deepEqual(buildRunInputs(inputs, { topic: " ", tone: "", count: "x", strict: false }), {
    ok: false,
    errors: { topic: "必填。", count: "请输入数字。" },
  });
  assert.deepEqual(buildRunInputs(inputs, { topic: "cats", tone: "", count: " 2.5 ", strict: true }), {
    ok: true,
    inputs: { topic: "cats", count: 2.5, strict: true },
  });
});

test("a flow survives export and import; other JSON does not import", () => {
  const def = newDefinition();
  assert.deepEqual(readFlowFile(flowFile("摘要", def)), { name: "摘要", definition: def });
  assert.deepEqual(readFlowFile(JSON.stringify(def))?.definition, def);
  assert.equal(readFlowFile('{"hello":"world"}'), null);
  assert.equal(readFlowFile("not json"), null);
  assert.equal(readFlowFile('{"format":"aiserveweave-flow","definition":{"nodes":[{"id":"x","type":"loop"}]}}'), null);
});

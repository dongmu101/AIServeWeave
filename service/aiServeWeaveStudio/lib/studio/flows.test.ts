import assert from "node:assert/strict";
import { test } from "node:test";

import { ContractError } from "./contract.ts";
import { flowErrorText, issueText, parseFlow, parseFlowPage, parseIssues, parseRun, parseRunPage, runErrorText, statusText } from "./flows.ts";

const definition = {
  inputs: [{ name: "topic", label: "主题", type: "text", required: true }],
  nodes: [
    { id: "start", type: "input", position: { x: 0, y: 0 } },
    { id: "draft", type: "llm", config: { model: "qwen3", prompt: "{{inputs.topic}}" }, position: { x: 200, y: 10 } },
    { id: "end", type: "output", position: { x: 400, y: 0 } },
  ],
  edges: [{ from: "start", to: "draft" }, { from: "draft", to: "end" }],
  outputs: [{ name: "answer", ref: "{{draft.text}}" }],
};

test("parseFlow reads a flow and its definition", () => {
  const flow = parseFlow({ id: "flw_1", name: "摘要", version: 3, definition, created_at: "2026-11-26T08:00:00Z", updated_at: "2026-11-26T09:00:00Z" });
  assert.equal(flow.id, "flw_1");
  assert.equal(flow.version, 3);
  assert.equal(flow.definition.nodes[1].config?.prompt, "{{inputs.topic}}");
  assert.deepEqual(flow.definition.edges[0], { from: "start", to: "draft" });
  assert.deepEqual(flow.definition.inputs[0], { name: "topic", label: "主题", type: "text", required: true });
});

test("parseFlowPage reads summaries without definitions", () => {
  const page = parseFlowPage({ data: [{ id: "flw_1", name: "a", version: 1, updated_at: "x" }], next: "c" });
  assert.deepEqual(page, { flows: [{ id: "flw_1", name: "a", version: 1, updatedAt: "x" }], next: "c" });
  assert.deepEqual(parseFlowPage({ data: null }), { flows: [], next: "" });
});

test("parsers refuse shapes they do not know", () => {
  assert.throws(() => parseFlow({ id: "flw_1", definition: { nodes: [{ id: "x", type: "loop" }] } }), ContractError);
  assert.throws(() => parseFlow({ id: "flw_1", definition: { edges: [{ from: "a", to: "b", branch: "maybe" }] } }), ContractError);
  assert.throws(() => parseRun({ id: "frn_1", status: "paused" }), ContractError);
  assert.throws(() => parseRun({ id: "frn_1", status: "running", outputs: { a: 1 } }), ContractError);
});

test("parseRun reads steps, outputs and usage", () => {
  const run = parseRun({
    id: "frn_1", flow_id: "flw_1", flow_version: 2, status: "failed", error_code: "node_timeout", failed_node: "draft",
    inputs: { topic: "cats" }, outputs: { answer: "x" }, usage: { prompt_tokens: 5, completion_tokens: 3 },
    created_at: "2026-11-26T08:00:00Z", finished_at: "2026-11-26T08:01:00Z",
    steps: [{ node_id: "draft", node_type: "llm", status: "failed", error_code: "node_timeout", inputs: { prompt: "p" }, usage: { prompt_tokens: 5 }, started_at: "a", finished_at: "b" }],
  });
  assert.equal(run.status, "failed");
  assert.deepEqual(run.inputs, { topic: "cats" });
  assert.deepEqual(run.usage, { promptTokens: 5, completionTokens: 3 });
  assert.deepEqual(run.steps[0].inputs, { prompt: "p" });
  assert.deepEqual(run.steps[0].outputs, {});
  assert.deepEqual(run.steps[0].usage, { promptTokens: 5, completionTokens: 0 });
  assert.equal(parseRunPage({ data: [{ id: "frn_2", status: "running" }] }).runs[0].id, "frn_2");
});

test("parseIssues keeps well-formed issues only, bounded", () => {
  const issues = parseIssues({
    error: { code: "invalid_flow" },
    issues: [{ node: "draft", field: "config.prompt", code: "unknown_reference", message: "{{inputs.x}} names an input" }, { code: "" }, "junk", { field: "inputs.topic", code: "required" }],
  });
  assert.deepEqual(issues, [
    { node: "draft", field: "config.prompt", code: "unknown_reference" },
    { node: "", field: "inputs.topic", code: "required" },
  ]);
  assert.equal(parseIssues({ issues: Array.from({ length: 80 }, () => ({ code: "cycle" })) }).length, 50);
  assert.deepEqual(parseIssues(null), []);
  assert.deepEqual(parseIssues({ error: "x" }), []);
});

test("issue, status and error wording is Chinese and names the field", () => {
  assert.equal(issueText({ node: "draft", field: "config.prompt", code: "unknown_reference" }), "提示词：引用了不存在的变量。");
  assert.equal(issueText({ node: "", field: "inputs.topic", code: "required" }), "输入「topic」：这一项必填。");
  assert.equal(issueText({ node: "x", field: "", code: "cycle" }), "连线形成了环。");
  assert.equal(issueText({ node: "", field: "", code: "brand_new" }), "这里有问题，请检查。");
  assert.equal(statusText("interrupted"), "已中断");
  assert.equal(runErrorText("invalid_json_output"), "模型没有按要求返回 JSON。");
  assert.equal(flowErrorText("run_limit", () => "fallback"), "同时进行的运行太多，请等其中一个结束。");
  assert.equal(flowErrorText("rate_limited", () => "fallback"), "fallback");
});

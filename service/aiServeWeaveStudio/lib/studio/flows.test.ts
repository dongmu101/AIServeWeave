import assert from "node:assert/strict";
import { test } from "node:test";

import { ContractError } from "./contract.ts";
import { flowErrorText, isFileInput, issueText, parseDefinition, parseFlow, parseFlowPage, parseIssues, parseRun, parseRunPage, runErrorText, statusText } from "./flows.ts";

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

test("parseDefinition reads image_generation and comfyui_template nodes", () => {
  const def = parseDefinition({
    ...definition,
    nodes: [
      ...definition.nodes,
      { id: "img", type: "image_generation", config: { prompt: "p", size: "512x512" }, position: { x: 1, y: 2 } },
      { id: "flux", type: "comfyui_template", config: { template: "t", inputs: { prompt: "x" } }, position: { x: 3, y: 4 } },
    ],
  });
  assert.deepEqual(def.nodes.slice(-2).map((n) => n.type), ["image_generation", "comfyui_template"]);
  assert.deepEqual(def.nodes.at(-1)?.config, { template: "t", inputs: { prompt: "x" } });
  assert.throws(() => parseDefinition({ ...definition, nodes: [{ id: "x", type: "audio_generation", position: {} }] }), ContractError);
});

test("issues of the second-phase nodes read in Chinese and name their field", () => {
  const reference = "图片或文件只能整个作为结果字段的取值，或整个填入 ComfyUI 模板的文件型输入（ComfyUI 的 artifacts 不行），不能放进提示词、模板、条件，也不能与其他文字混写。";
  assert.equal(issueText({ node: "end", field: "outputs.cover.ref", code: "artifact_reference" }), `输出「cover」：${reference}`);
  assert.equal(issueText({ node: "n", field: "config.prompt", code: "artifact_reference" }), `提示词：${reference}`);
  assert.equal(issueText({ node: "n", field: "config.inputs.ref", code: "artifact_reference" }), `模板输入「ref」：${reference}`);
  assert.match(issueText({ node: "n", field: "config.inputs.prompt", code: "invalid_input" }), /^模板输入「prompt」：.*文本输入不能绑定文件、文件输入只能选文件来源/);
  assert.equal(issueText({ node: "n", field: "config.size", code: "invalid_config" }), "尺寸：配置不正确。");
  assert.equal(issueText({ node: "n", field: "config.template", code: "template_unavailable" }), "模板：所选工作流模板当前不可用（未发布或对你不可见），请换一个。");
  assert.equal(issueText({ node: "n", field: "config.model", code: "image_generation_unavailable" }), "模型：部署没有默认的图像生成，请选择一个图像模型。");
});

test("image and file inputs parse without a default", () => {
  const def = parseDefinition({
    ...definition,
    inputs: [
      { name: "photo", type: "image", required: true, default: "ignored" },
      { name: "doc", label: "文档", type: "file", required: false },
      { name: "topic", type: "text", required: false, default: "x" },
    ],
  });
  assert.deepEqual(def.inputs, [
    { name: "photo", type: "image", required: true },
    { name: "doc", label: "文档", type: "file", required: false },
    { name: "topic", type: "text", required: false, default: "x" },
  ]);
  assert.deepEqual(def.inputs.map(isFileInput), [true, true, false]);
  assert.throws(() => parseDefinition({ ...definition, inputs: [{ name: "x", type: "video", required: false }] }), ContractError);
});

test("the issues of a refused file input read in Chinese", () => {
  const text = (code: string) => issueText({ node: "", field: "inputs.photo", code });
  assert.match(text("unsupported_file_type"), /^输入「photo」：文件类型不受支持.*png、jpg、jpeg、webp、gif、bmp/);
  assert.match(text("not_found"), /^输入「photo」：上次运行的这个文件已不存在或已过保留期/);
  assert.match(text("artifact_storage_unavailable"), /^输入「photo」：部署没有配置文件存储/);
  assert.match(text("unknown_input"), /多出了没有对应输入的文件/);
  assert.match(text("invalid_value"), /不能同时上传新文件又沿用上次的文件/);
  assert.match(text("required"), /必填/);
  assert.match(issueText({ node: "", field: "inputs[0].default", code: "invalid_default" }), /图片和文件输入不能设默认值/);
});

test("a stored file that is gone fails a node with its own wording; the old unsupported_input stays readable", () => {
  const generic = runErrorText("never_heard_of_it");
  assert.match(runErrorText("not_found"), /已不存在，可能超过了保留期被清理/);
  assert.match(runErrorText("invalid_input"), /文本输入绑了文件、文件输入绑了文本、必填的文件没有选/);
  assert.notEqual(runErrorText("unsupported_input"), generic);
  assert.match(runErrorText("unsupported_input"), /编排暂时无法提供/);
});

test("refused run starts that are not about one input read in Chinese", () => {
  const fallback = () => "fallback";
  assert.match(flowErrorText("too_large", fallback), /换小一点的文件.*200 MiB/);
  assert.equal(flowErrorText("body_too_large", fallback), flowErrorText("too_large", fallback));
  assert.match(flowErrorText("storage_unavailable", fallback), /上传文件的存储暂不可用/);
  assert.equal(flowErrorText("invalid_inputs", fallback), "输入值有误，请按提示修改。");
});

test("every new node failure code has its own Chinese wording", () => {
  const generic = runErrorText("never_heard_of_it");
  for (const code of [
    "no_image_produced", "image_generation_unavailable", "model_unavailable", "template_unavailable", "invalid_input", "unsupported_input", "no_capable_node",
    "queue_timeout", "generation_failed", "out_of_memory", "job_cancelled", "artifact_storage_unavailable", "rate_limited", "upstream_error", "node_timeout",
  ]) {
    const text = runErrorText(code);
    assert.notEqual(text, generic, code);
    assert.match(text, /[\u4e00-\u9fff]/, code);
  }
});

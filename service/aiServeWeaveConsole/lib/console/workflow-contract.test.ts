import assert from "node:assert/strict";
import test from "node:test";

import { ApiError } from "./errors.ts";
import { parseJobView, parseWorkflowCatalogue } from "./fleet.ts";

/** catalogueDocument is what the control plane's fleet.Catalogue produces.
 *
 * catalogueDocument 是控制面的 fleet.Catalogue 所产生的文档。 */
function catalogueDocument(templates: unknown[]) {
  return {
    collected_at: "2026-01-01T12:00:00Z",
    replicas: [
      {
        endpoint: "http://gateway-1:8091",
        replica_id: "replica-1",
        generated_at: "2026-01-01T11:59:00Z",
        node_count: templates.length,
      },
    ],
    partial: false,
    templates,
  };
}

test("a workflow menu keeps the declared inputs and their bounds", () => {
  const catalogue = parseWorkflowCatalogue(
    catalogueDocument([
      {
        id: "portrait",
        description: "A portrait workflow",
        valid: true,
        replicas: ["replica-1"],
        divergent: false,
        inputs: [
          { name: "prompt", type: "string", required: true, max_length: 4096 },
          { name: "steps", type: "integer", min: 1, max: 50, default: 20 },
          { name: "seed", type: "integer" },
        ],
      },
    ])
  );

  const template = catalogue.templates[0];
  assert.equal(template?.id, "portrait");
  assert.equal(template?.valid, true);
  assert.equal(template?.inputs.length, 3);

  const [prompt, steps, seed] = template!.inputs;
  assert.equal(prompt?.required, true);
  assert.equal(prompt?.maxLength, 4096);
  // An unbounded input keeps null rather than zero: "no maximum" and "a
  // maximum of zero" are different declarations, and one of them accepts
  // nothing.
  //
  // 无界的输入保持 null 而不是零：「没有上界」与「上界为零」是两种不同的声明，而其中
  // 一种什么都不接受。
  assert.equal(prompt?.min, null);
  assert.equal(steps?.min, 1);
  assert.equal(steps?.max, 50);
  // The default is kept as the raw JSON text it arrived as, never interpreted.
  //
  // 默认值保持它到达时的原始 JSON 文本，绝不被解释。
  assert.equal(steps?.defaultValue, "20");
  assert.equal(seed?.defaultValue, null);
  assert.equal(seed?.maxLength, null);
});

test("the menu carries no graph, because the contract has nowhere to put one", () => {
  // The control plane would have to invent a field for a graph to arrive in.
  // This asserts the parser drops anything of the sort rather than passing it
  // through to a component that might render it.
  //
  // 控制面得先凭空造出一个字段，图才有地方到达。这里断言解析器会丢掉这类东西，而不是
  // 把它透传给某个可能渲染它的组件。
  const catalogue = parseWorkflowCatalogue(
    catalogueDocument([
      {
        id: "portrait",
        valid: true,
        inputs: [],
        graph: { "3": { class_type: "KSampler", inputs: { seed: 1 } } },
      },
    ])
  );
  assert.equal(
    JSON.stringify(catalogue).includes("class_type"),
    false,
    "a graph that arrived anyway must not survive parsing"
  );
});

test("a divergent template is a fact the parser preserves", () => {
  const catalogue = parseWorkflowCatalogue(
    catalogueDocument([
      { id: "upscale", valid: true, inputs: [], replicas: ["replica-1"], divergent: true },
    ])
  );
  assert.equal(catalogue.templates[0]?.divergent, true);
  assert.deepEqual(catalogue.templates[0]?.replicas, ["replica-1"]);
});

test("a catalogue missing what it is judged by is refused", () => {
  const invalid: { name: string; value: unknown }[] = [
    { name: "no collected_at", value: { replicas: [], templates: [], partial: false } },
    {
      name: "a template with no id",
      value: catalogueDocument([{ valid: true, inputs: [] }]),
    },
    {
      name: "an input with no type",
      value: catalogueDocument([{ id: "x", valid: true, inputs: [{ name: "prompt" }] }]),
    },
    {
      name: "a non-numeric bound",
      value: catalogueDocument([
        { id: "x", valid: true, inputs: [{ name: "n", type: "integer", min: "one" }] },
      ]),
    },
  ];
  for (const item of invalid) {
    assert.throws(() => parseWorkflowCatalogue(item.value), ApiError, item.name);
  }
});

test("a job view keeps the two flags that stop it being read as a history", () => {
  const view = parseJobView({
    collected_at: "2026-01-01T12:00:00Z",
    replicas: [
      { endpoint: "http://gateway-1:8091", replica_id: "replica-1", generated_at: "2026-01-01T11:59:00Z", node_count: 1 },
      { endpoint: "http://gateway-2:8091", error: "timeout", node_count: 0 },
    ],
    partial: true,
    truncated: true,
    jobs: [
      {
        id: "job-1",
        workflow_id: "portrait",
        state: "running",
        queue_position: 3,
        created_at: "2026-01-01T11:58:00Z",
        updated_at: "2026-01-01T11:59:00Z",
        artifact_ids: ["art-1", "art-2"],
        replica: "replica-1",
      },
    ],
  });

  assert.equal(view.partial, true, "a replica that did not answer leaves a gap");
  assert.equal(view.truncated, true, "an evicting table leaves a gap");
  assert.equal(view.jobs[0]?.queuePosition, 3);
  assert.equal(view.jobs[0]?.artifactIds.length, 2);
  assert.equal(view.jobs[0]?.replica, "replica-1");
});

test("a job never carries where it ran", () => {
  // The node, the runtime and the resolved model are absent from the contract
  // by design: nodes are an operator view, and scheduler.Candidate.Model is
  // documented as something the client never learns. A control plane that
  // sent them anyway must not have them survive into a tenant's page.
  //
  // 节点、运行时与解析后的模型按设计就不在契约里：节点属于运维视图，而
  // scheduler.Candidate.Model 的文档写明客户端从不得知它。即便控制面把它们发了过来，
  // 也不能让它们存活到租户的页面上。
  const view = parseJobView({
    collected_at: "2026-01-01T12:00:00Z",
    replicas: [],
    partial: false,
    truncated: false,
    jobs: [
      {
        id: "job-1",
        workflow_id: "portrait",
        state: "running",
        created_at: "2026-01-01T11:58:00Z",
        updated_at: "2026-01-01T11:59:00Z",
        node_id: "node-gpu-01",
        runtime_id: "comfy-a",
        model: "sdxl-internal-alias",
      },
    ],
  });

  const rendered = JSON.stringify(view);
  for (const leaked of ["node-gpu-01", "comfy-a", "sdxl-internal-alias"]) {
    assert.equal(rendered.includes(leaked), false, `${leaked} survived parsing`);
  }
});

test("a job view missing its timestamps is refused", () => {
  const invalid: { name: string; value: unknown }[] = [
    { name: "no collected_at", value: { replicas: [], jobs: [], partial: false, truncated: false } },
    {
      name: "a job with no created_at",
      value: {
        collected_at: "2026-01-01T12:00:00Z",
        replicas: [],
        partial: false,
        truncated: false,
        jobs: [{ id: "job-1", workflow_id: "portrait", state: "running", updated_at: "2026-01-01T12:00:00Z" }],
      },
    },
    {
      name: "a job with no state",
      value: {
        collected_at: "2026-01-01T12:00:00Z",
        replicas: [],
        partial: false,
        truncated: false,
        jobs: [{ id: "job-1", workflow_id: "portrait", created_at: "2026-01-01T12:00:00Z", updated_at: "2026-01-01T12:00:00Z" }],
      },
    },
  ];
  for (const item of invalid) {
    assert.throws(() => parseJobView(item.value), ApiError, item.name);
  }
});

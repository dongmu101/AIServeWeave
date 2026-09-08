import assert from "node:assert/strict";
import test from "node:test";

import { ApiError } from "./errors.ts";
import { parseFleetSnapshot, parseModelCatalog } from "./fleet.ts";

/** replicaDocument is what the control plane's fleet.ReplicaStatus produces.
 *
 * replicaDocument 是控制面的 fleet.ReplicaStatus 所产生的文档。 */
function replicaDocument(overrides: Record<string, unknown> = {}) {
  return {
    endpoint: "http://gateway-1:8091",
    replica_id: "replica-1",
    generated_at: "2026-01-01T11:59:00Z",
    node_count: 1,
    ...overrides,
  };
}

/** nodeDocument is what fleet.Node produces for a fully reported node.
 *
 * nodeDocument 是 fleet.Node 为一个上报完整的节点所产生的文档。 */
function nodeDocument(overrides: Record<string, unknown> = {}) {
  return {
    node_id: "node-a",
    agent_version: "1.2.3",
    live: true,
    maintenance: false,
    last_heartbeat: "2026-01-01T11:59:00Z",
    labels: { zone: "rack-1" },
    resources: { cpu_cores: 32, gpu_count: 2, os: "linux", arch: "amd64" },
    inflight_requests: 2,
    idle_slots: { inference: 4 },
    declared_runtime_ids: ["vllm-a"],
    runtimes: [
      {
        id: "vllm-a",
        kind: "vllm",
        base_url: "http://127.0.0.1:8000",
        state: "healthy",
        identity_verified: true,
        models: [{ id: "qwen-7b" }],
      },
    ],
    replicas: ["replica-1"],
    observed_at: "2026-01-01T11:59:00Z",
    ...overrides,
  };
}

test("a fleet snapshot keeps the fields that make it honest", () => {
  const snapshot = parseFleetSnapshot({
    collected_at: "2026-01-01T12:00:00Z",
    replicas: [replicaDocument()],
    nodes: [nodeDocument()],
    partial: false,
  });

  assert.equal(snapshot.collectedAt, "2026-01-01T12:00:00Z");
  assert.equal(snapshot.partial, false);
  assert.equal(snapshot.replicas[0]?.replicaId, "replica-1");
  assert.equal(snapshot.replicas[0]?.error, null);

  const node = snapshot.nodes[0];
  assert.equal(node?.nodeId, "node-a");
  assert.equal(node?.live, true);
  assert.equal(node?.draining, false, "an absent draining flag is false, not unknown");
  assert.equal(node?.resources?.gpuCount, 2);
  assert.equal(node?.idleSlots.inference, 4);
  assert.equal(node?.runtimes[0]?.models[0]?.id, "qwen-7b");
  // No health check has completed, so latency stays absent. Zero would be a
  // measurement, and it would be a lie.
  //
  // 尚未完成过健康检查，因此耗时保持缺席。零会是一个测量结果，而那会是谎言。
  assert.equal(node?.runtimes[0]?.latencyMillis, null);
  assert.equal(node?.runtimes[0]?.checkedAt, null);
});

test("a replica that did not answer keeps its error code and loses its timestamp", () => {
  const snapshot = parseFleetSnapshot({
    collected_at: "2026-01-01T12:00:00Z",
    replicas: [
      replicaDocument({
        replica_id: undefined,
        generated_at: undefined,
        node_count: 0,
        error: "timeout",
      }),
    ],
    nodes: [],
    partial: true,
  });

  assert.equal(snapshot.partial, true);
  assert.equal(snapshot.replicas[0]?.error, "timeout");
  assert.equal(snapshot.replicas[0]?.replicaId, null);
  assert.equal(snapshot.replicas[0]?.generatedAt, null);
  // The endpoint survives: it is how an operator with the deployment in front
  // of them knows which replica this was.
  //
  // endpoint 保留下来：对着部署清单的运维，正是靠它认出这是哪个副本。
  assert.equal(snapshot.replicas[0]?.endpoint, "http://gateway-1:8091");
});

test("a snapshot missing the fields it is judged by is refused", () => {
  const invalid: { name: string; value: unknown }[] = [
    {
      name: "no collected_at, so the age of the data is unknowable",
      value: { replicas: [], nodes: [], partial: false },
    },
    {
      name: "an unparseable collected_at",
      value: { collected_at: "just now", replicas: [], nodes: [], partial: false },
    },
    {
      name: "a partial flag that is not a boolean",
      value: { collected_at: "2026-01-01T12:00:00Z", replicas: [], nodes: [], partial: "yes" },
    },
    {
      name: "a node with no id",
      value: {
        collected_at: "2026-01-01T12:00:00Z",
        replicas: [],
        nodes: [nodeDocument({ node_id: undefined })],
        partial: false,
      },
    },
    {
      name: "a node with no observed_at",
      value: {
        collected_at: "2026-01-01T12:00:00Z",
        replicas: [],
        nodes: [nodeDocument({ observed_at: undefined })],
        partial: false,
      },
    },
    {
      name: "labels that are not strings",
      value: {
        collected_at: "2026-01-01T12:00:00Z",
        replicas: [],
        nodes: [nodeDocument({ labels: { zone: 1 } })],
        partial: false,
      },
    },
    {
      name: "the bare array a list endpoint would return",
      value: [nodeDocument()],
    },
  ];

  for (const item of invalid) {
    assert.throws(
      () => parseFleetSnapshot(item.value),
      (error: unknown) => error instanceof ApiError && error.kind === "contract",
      item.name
    );
  }
});

test("a model catalog keeps the model, the backend and the deployment apart", () => {
  const catalog = parseModelCatalog({
    collected_at: "2026-01-01T12:00:00Z",
    replicas: [replicaDocument()],
    partial: false,
    models: [
      {
        id: "qwen-7b",
        available_deployments: 1,
        deployments: [
          {
            node_id: "node-a",
            runtime_id: "vllm-a",
            backend: "vllm",
            state: "healthy",
            node_live: true,
          },
          {
            node_id: "node-b",
            runtime_id: "ollama-a",
            backend: "ollama",
            state: "healthy",
            node_live: false,
          },
        ],
      },
    ],
  });

  const model = catalog.models[0];
  assert.equal(model?.id, "qwen-7b");
  assert.equal(model?.deployments.length, 2);
  // One model, two backends, two deployments — and only one of them counts as
  // available, because the other node is not reachable.
  //
  // 一个模型、两个后端、两处部署——而其中只有一处计入可用，因为另一个节点够不到。
  assert.equal(model?.deployments[0]?.backend, "vllm");
  assert.equal(model?.deployments[1]?.backend, "ollama");
  assert.equal(model?.availableDeployments, 1);
  assert.equal(model?.deployments[1]?.nodeLive, false);
});

test("a catalog that lost its timestamp or its models is refused", () => {
  const invalid: { name: string; value: unknown }[] = [
    { name: "no collected_at", value: { replicas: [], models: [], partial: false } },
    {
      name: "a model with no id",
      value: {
        collected_at: "2026-01-01T12:00:00Z",
        replicas: [],
        partial: false,
        models: [{ deployments: [] }],
      },
    },
    {
      name: "a deployment with no runtime",
      value: {
        collected_at: "2026-01-01T12:00:00Z",
        replicas: [],
        partial: false,
        models: [{ id: "qwen-7b", deployments: [{ node_id: "node-a", state: "healthy" }] }],
      },
    },
  ];
  for (const item of invalid) {
    assert.throws(() => parseModelCatalog(item.value), ApiError, item.name);
  }
});

for (const maintenance of [true, false]) {
  test(`fleet preserves observed maintenance=${maintenance}`, () => {
    const snapshot = parseFleetSnapshot({ collected_at: "2026-01-01T12:00:00Z", replicas: [replicaDocument()], nodes: [nodeDocument({ maintenance })], partial: false });
    assert.equal(snapshot.nodes[0]?.maintenance, maintenance);
  });
}
for (const [name, maintenance] of [["missing", undefined], ["null", null], ["string", "false"], ["number", 0]] as const) {
  test(`fleet rejects ${name} observed maintenance instead of inventing state`, () => {
    assert.throws(() => parseFleetSnapshot({ collected_at: "2026-01-01T12:00:00Z", replicas: [replicaDocument()], nodes: [nodeDocument({ maintenance })], partial: false }), ApiError);
  });
}

import { ApiError } from "./errors.ts";

/**
 * This module mirrors the fleet inventory the control plane aggregates
 * (`internal/fleet`), which in turn mirrors what each Gateway replica reports
 * (`common/nodeview`).
 *
 * Three fields carry the honesty of this view and are validated rather than
 * assumed: `collected_at` and each replica's `generated_at` are what let a
 * console say how old the data is, and `partial` is what lets it say the data
 * is incomplete. A parser that defaulted any of them would turn a partial,
 * stale answer into one that looks current and whole.
 *
 * 本模块镜像控制面聚合出的机群清单（`internal/fleet`），而后者又镜像各 Gateway 副本
 * 所报告的内容（`common/nodeview`）。
 *
 * 有三个字段承载着这个视图的诚实性，它们是被校验的而不是被假定的：`collected_at` 与每个
 * 副本的 `generated_at` 让控制台能说清数据有多旧，`partial` 让它能说清数据不完整。任何
 * 一个被解析器补上默认值，都会把一个局部且过期的答案，变成一个看起来又新又完整的答案。
 */

/** ReplicaStatus is what happened when one Gateway replica was asked.
 *
 * ReplicaStatus 是询问某个 Gateway 副本时发生的事。 */
export interface ReplicaStatus {
  endpoint: string;
  replicaId: string | null;
  generatedAt: string | null;
  /** error is null when the replica answered, otherwise one of the four fixed
   * codes the control plane emits.
   *
   * error 在副本作答时为 null，否则是控制面给出的四个固定代号之一。 */
  error: string | null;
  nodeCount: number;
}

/** RuntimeModel is one model a runtime serves.
 *
 * RuntimeModel 是一个运行时所提供的一个模型。 */
export interface RuntimeModel {
  id: string;
  capabilities: Record<string, string>;
}

/** NodeRuntime is one inference backend on a node.
 *
 * NodeRuntime 是一个节点上的一个推理后端。 */
export interface NodeRuntime {
  id: string;
  kind: string;
  baseUrl: string;
  state: string;
  version: string;
  identityVerified: boolean;
  latencyMillis: number | null;
  checkedAt: string | null;
  errorSummary: string;
  capabilities: Record<string, string>;
  models: RuntimeModel[];
  warnings: string[];
  degraded: string[];
  discoveredAt: string | null;
  updatedAt: string | null;
}

/** FleetNode is one node as the fleet sees it.
 *
 * FleetNode 是机群眼中的一个节点。 */
export interface FleetNode {
  nodeId: string;
  agentVersion: string;
  live: boolean;
  draining: boolean;
  maintenance: boolean;
  lastHeartbeat: string | null;
  labels: Record<string, string>;
  resources: NodeResources | null;
  inflightRequests: number;
  idleSlots: Record<string, number>;
  declaredRuntimeIds: string[];
  runtimes: NodeRuntime[];
  replicas: string[];
  observedAt: string;
}

/** NodeResources is the hardware the Agent reported.
 *
 * NodeResources 是 Agent 上报的硬件情况。 */
export interface NodeResources {
  cpuCores: number;
  memoryBytes: number;
  gpuCount: number;
  gpuMemoryBytes: number;
  os: string;
  arch: string;
}

/** FleetSnapshot is the whole node answer.
 *
 * FleetSnapshot 是完整的节点答案。 */
export interface FleetSnapshot {
  collectedAt: string;
  replicas: ReplicaStatus[];
  nodes: FleetNode[];
  partial: boolean;
}

/** Deployment is one place a model is actually served.
 *
 * Deployment 是一个模型真正被提供的一处所在。 */
export interface Deployment {
  nodeId: string;
  runtimeId: string;
  backend: string;
  state: string;
  nodeLive: boolean;
  capabilities: Record<string, string>;
}

/** CatalogModel is one model id and everywhere it is served.
 *
 * CatalogModel 是一个模型 id，以及它被提供的所有位置。 */
export interface CatalogModel {
  id: string;
  deployments: Deployment[];
  availableDeployments: number;
}

/** ModelCatalog is the model view of the same snapshot.
 *
 * ModelCatalog 是同一份快照的模型视角。 */
export interface ModelCatalog {
  collectedAt: string;
  replicas: ReplicaStatus[];
  partial: boolean;
  models: CatalogModel[];
}

function fail(): never {
  throw new ApiError("contract");
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    fail();
  }
  return value as Record<string, unknown>;
}

function text(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (value === undefined) {
    return "";
  }
  if (typeof value !== "string") {
    fail();
  }
  return value;
}

function required(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (typeof value !== "string" || value === "") {
    fail();
  }
  return value;
}

function timestamp(source: Record<string, unknown>, key: string): string {
  const value = required(source, key);
  if (Number.isNaN(Date.parse(value))) {
    fail();
  }
  return value;
}

function optionalTimestamp(
  source: Record<string, unknown>,
  key: string
): string | null {
  const value = source[key];
  if (value === undefined || value === null || value === "") {
    return null;
  }
  if (typeof value !== "string" || Number.isNaN(Date.parse(value))) {
    fail();
  }
  return value;
}

function flag(source: Record<string, unknown>, key: string): boolean {
  const value = source[key];
  if (value === undefined) {
    return false;
  }
  if (typeof value !== "boolean") {
    fail();
  }
  return value;
}

function count(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  if (value === undefined || value === null) {
    return 0;
  }
  if (typeof value !== "number" || !Number.isFinite(value)) {
    fail();
  }
  return value;
}

function strings(source: Record<string, unknown>, key: string): string[] {
  const value = source[key];
  if (value === undefined || value === null) {
    return [];
  }
  if (!Array.isArray(value) || value.some((item) => typeof item !== "string")) {
    fail();
  }
  return value as string[];
}

function stringMap(
  source: Record<string, unknown>,
  key: string
): Record<string, string> {
  const value = source[key];
  if (value === undefined || value === null) {
    return {};
  }
  const entries = record(value);
  for (const item of Object.values(entries)) {
    if (typeof item !== "string") {
      fail();
    }
  }
  return entries as Record<string, string>;
}

function numberMap(
  source: Record<string, unknown>,
  key: string
): Record<string, number> {
  const value = source[key];
  if (value === undefined || value === null) {
    return {};
  }
  const entries = record(value);
  for (const item of Object.values(entries)) {
    if (typeof item !== "number") {
      fail();
    }
  }
  return entries as Record<string, number>;
}

function list<T>(value: unknown, parse: (item: unknown) => T): T[] {
  if (value === undefined || value === null) {
    return [];
  }
  if (!Array.isArray(value)) {
    fail();
  }
  return value.map(parse);
}

function parseReplica(value: unknown): ReplicaStatus {
  const source = record(value);
  return {
    endpoint: required(source, "endpoint"),
    replicaId: text(source, "replica_id") || null,
    generatedAt: optionalTimestamp(source, "generated_at"),
    error: text(source, "error") || null,
    nodeCount: count(source, "node_count"),
  };
}

function parseRuntime(value: unknown): NodeRuntime {
  const source = record(value);
  const latency = source.latency_millis;
  if (
    latency !== undefined &&
    latency !== null &&
    (typeof latency !== "number" || !Number.isFinite(latency))
  ) {
    fail();
  }
  return {
    id: required(source, "id"),
    kind: text(source, "kind"),
    baseUrl: text(source, "base_url"),
    state: required(source, "state"),
    version: text(source, "version"),
    identityVerified: flag(source, "identity_verified"),
    latencyMillis: typeof latency === "number" ? latency : null,
    checkedAt: optionalTimestamp(source, "checked_at"),
    errorSummary: text(source, "error_summary"),
    capabilities: stringMap(source, "capabilities"),
    models: list(source.models, (item) => {
      const model = record(item);
      return {
        id: required(model, "id"),
        capabilities: stringMap(model, "capabilities"),
      };
    }),
    warnings: strings(source, "warnings"),
    degraded: strings(source, "degraded"),
    discoveredAt: optionalTimestamp(source, "discovered_at"),
    updatedAt: optionalTimestamp(source, "updated_at"),
  };
}

function parseNode(value: unknown): FleetNode {
  const source = record(value);
  const resources = source.resources;
  return {
    nodeId: required(source, "node_id"),
    agentVersion: text(source, "agent_version"),
    live: flag(source, "live"),
    draining: flag(source, "draining"),
    maintenance: typeof source.maintenance === "boolean" ? source.maintenance : fail(),
    lastHeartbeat: optionalTimestamp(source, "last_heartbeat"),
    labels: stringMap(source, "labels"),
    resources:
      resources === undefined || resources === null
        ? null
        : (() => {
            const entries = record(resources);
            return {
              cpuCores: count(entries, "cpu_cores"),
              memoryBytes: count(entries, "memory_bytes"),
              gpuCount: count(entries, "gpu_count"),
              gpuMemoryBytes: count(entries, "gpu_memory_bytes"),
              os: text(entries, "os"),
              arch: text(entries, "arch"),
            };
          })(),
    inflightRequests: count(source, "inflight_requests"),
    idleSlots: numberMap(source, "idle_slots"),
    declaredRuntimeIds: strings(source, "declared_runtime_ids"),
    runtimes: list(source.runtimes, parseRuntime),
    replicas: strings(source, "replicas"),
    observedAt: timestamp(source, "observed_at"),
  };
}

/**
 * parseFleetSnapshot validates the node inventory.
 *
 * parseFleetSnapshot 校验节点清单。
 */
export function parseFleetSnapshot(value: unknown): FleetSnapshot {
  const source = record(value);
  return {
    collectedAt: timestamp(source, "collected_at"),
    replicas: list(source.replicas, parseReplica),
    nodes: list(source.nodes, parseNode),
    partial: flag(source, "partial"),
  };
}

/**
 * parseModelCatalog validates the model catalog.
 *
 * parseModelCatalog 校验模型目录。
 */
export function parseModelCatalog(value: unknown): ModelCatalog {
  const source = record(value);
  return {
    collectedAt: timestamp(source, "collected_at"),
    replicas: list(source.replicas, parseReplica),
    partial: flag(source, "partial"),
    models: list(source.models, (item) => {
      const model = record(item);
      return {
        id: required(model, "id"),
        deployments: list(model.deployments, (entry) => {
          const deployment = record(entry);
          return {
            nodeId: required(deployment, "node_id"),
            runtimeId: required(deployment, "runtime_id"),
            backend: text(deployment, "backend"),
            state: required(deployment, "state"),
            nodeLive: flag(deployment, "node_live"),
            capabilities: stringMap(deployment, "capabilities"),
          };
        }),
        availableDeployments: count(model, "available_deployments"),
      };
    }),
  };
}

/**
 * WorkflowInput is one substitutable value a template declares.
 *
 * WorkflowInput 是一个模板声明的可替换取值。
 */
export interface WorkflowInput {
  name: string;
  type: string;
  required: boolean;
  /** defaultValue is the template author's own literal, kept as the raw JSON
   * it arrived as. It is rendered as text, never interpreted: it is data from
   * a configuration file, and a console is not the place to evaluate it.
   *
   * defaultValue 是模板作者自己写下的字面量，保持它到达时的原始 JSON。它以文本渲染、
   * 绝不被解释：那是来自配置文件的数据，而控制台不是解释它的地方。 */
  defaultValue: string | null;
  maxLength: number | null;
  min: number | null;
  max: number | null;
}

/**
 * WorkflowTemplate is one registered workflow, as a caller needs to know it.
 *
 * WorkflowTemplate 是一个已注册的工作流，只到调用方需要知道的程度。
 */
export interface WorkflowTemplate {
  id: string;
  description: string;
  inputs: WorkflowInput[];
  valid: boolean;
  validationError: string;
  /** replicas is empty on the tenant surface: which replicas hold a template
   * is an operator's question, and the tenant endpoint clears it.
   *
   * replicas 在租户面上为空：哪些副本持有某个模板是运维的问题，租户端点会清空它。 */
  replicas: string[];
  divergent: boolean;
}

/**
 * WorkflowCatalogue is the merged workflow menu.
 *
 * WorkflowCatalogue 是合并后的工作流菜单。
 */
export interface WorkflowCatalogue {
  collectedAt: string;
  replicas: ReplicaStatus[];
  partial: boolean;
  templates: WorkflowTemplate[];
}

/**
 * WorkflowJob is one run, and the replica holding it.
 *
 * WorkflowJob 是一次运行，以及持有它的副本。
 */
export interface WorkflowJob {
  id: string;
  workflowId: string;
  state: string;
  queuePosition: number;
  errorSummary: string;
  createdAt: string;
  updatedAt: string;
  artifactIds: string[];
  replica: string;
}

/**
 * JobView is a tenant's current runs — a live view, not a history.
 *
 * `truncated` and `partial` are the two fields that keep it from being read as
 * one. A parser that defaulted either would turn "some runs are missing" into
 * "these are all the runs".
 *
 * JobView 是某个租户当前的运行——实时视图，不是历史。
 *
 * `truncated` 与 `partial` 是阻止它被读成历史的那两个字段。任何一个被解析器补上默认值，
 * 都会把「有些运行不见了」变成「这些就是全部运行」。
 */
export interface JobView {
  collectedAt: string;
  replicas: ReplicaStatus[];
  partial: boolean;
  truncated: boolean;
  jobs: WorkflowJob[];
}

/**
 * parseWorkflowCatalogue validates the workflow menu.
 *
 * parseWorkflowCatalogue 校验工作流菜单。
 */
export function parseWorkflowCatalogue(value: unknown): WorkflowCatalogue {
  const source = record(value);
  return {
    collectedAt: timestamp(source, "collected_at"),
    replicas: list(source.replicas, parseReplica),
    partial: flag(source, "partial"),
    templates: list(source.templates, (item) => {
      const template = record(item);
      return {
        id: required(template, "id"),
        description: text(template, "description"),
        valid: flag(template, "valid"),
        validationError: text(template, "validation_error"),
        replicas: strings(template, "replicas"),
        divergent: flag(template, "divergent"),
        inputs: list(template.inputs, (entry) => {
          const input = record(entry);
          const raw = input.default;
          return {
            name: required(input, "name"),
            type: required(input, "type"),
            required: flag(input, "required"),
            // The default arrives as whatever JSON the template author wrote.
            // It is re-serialized to text here rather than kept as a value,
            // so no component can accidentally render an object or spread it.
            //
            // 默认值以模板作者所写的任意 JSON 到达。这里把它重新序列化为文本而不是
            // 保留为值，这样就没有组件会不小心渲染一个对象或把它展开。
            defaultValue: raw === undefined || raw === null ? null : JSON.stringify(raw),
            maxLength: optionalNumber(input, "max_length"),
            min: optionalNumber(input, "min"),
            max: optionalNumber(input, "max"),
          };
        }),
      };
    }),
  };
}

/**
 * parseJobView validates a tenant's live run list.
 *
 * parseJobView 校验某个租户的实时运行列表。
 */
export function parseJobView(value: unknown): JobView {
  const source = record(value);
  return {
    collectedAt: timestamp(source, "collected_at"),
    replicas: list(source.replicas, parseReplica),
    partial: flag(source, "partial"),
    truncated: flag(source, "truncated"),
    jobs: list(source.jobs, (item) => {
      const job = record(item);
      return {
        id: required(job, "id"),
        workflowId: required(job, "workflow_id"),
        state: required(job, "state"),
        queuePosition: count(job, "queue_position"),
        errorSummary: text(job, "error_summary"),
        createdAt: timestamp(job, "created_at"),
        updatedAt: timestamp(job, "updated_at"),
        artifactIds: strings(job, "artifact_ids"),
        replica: text(job, "replica"),
      };
    }),
  };
}

/** optionalNumber reads a numeric field that may be absent, keeping absent
 * distinct from zero: an unbounded input and one bounded at zero are not the
 * same declaration.
 *
 * optionalNumber 读取一个可能缺席的数值字段，并让缺席与零保持可区分：一个无界的输入
 * 与一个界为零的输入不是同一种声明。 */
function optionalNumber(
  source: Record<string, unknown>,
  key: string
): number | null {
  const value = source[key];
  if (value === undefined || value === null) {
    return null;
  }
  if (typeof value !== "number" || !Number.isFinite(value)) {
    fail();
  }
  return value;
}

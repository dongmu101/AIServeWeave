import type { RequestSpec } from "./api-client.ts";
import { ApiError } from "./errors.ts";

/**
 * This module mirrors GET/POST /operator/v1/nodes/:id/comfyui-managed and
 * POST /operator/v1/nodes/:id/comfyui-managed/custom-nodes
 * (internal/types.ComfyUIManagedStatusResponse/ComfyUIManagedTriggerResponse),
 * which the control plane builds by fanning a request out to every
 * configured Gateway replica and folding their answers (STATUS.md's P2
 * ComfyUI Managed Docker deployment, subtasks two through four).
 *
 * A trigger's 202 only confirms the request reached at least one connected
 * replica's tunnel — never that the Agent actually applied it. The real
 * outcome is only observable by reading status again afterward, which is why
 * this page's actions never chain a re-fetch as if they were synchronous.
 *
 * 本模块镜像 GET/POST /operator/v1/nodes/:id/comfyui-managed 与
 * POST /operator/v1/nodes/:id/comfyui-managed/custom-nodes
 * (internal/types.ComfyUIManagedStatusResponse/ComfyUIManagedTriggerResponse)，
 * 控制面把请求扇出给每个已配置 Gateway 副本并折叠它们的回答来构造它
 * (STATUS.md 的 P2 ComfyUI Managed Docker 部署，子任务二至四)。
 *
 * 一次触发的 202 只确认请求到达了至少一个已连接副本的隧道——从不确认 Agent
 * 真的执行了它。真实结果只能通过之后再读一次状态来观察，这也是本页面的操作
 * 从不把重新拉取状态串成同步动作的原因。
 */

/** ComfyUIManagedAction is the closed lifecycle vocabulary the trigger
 * endpoint accepts.
 *
 * ComfyUIManagedAction 是触发端点接受的封闭生命周期词表。 */
export type ComfyUIManagedAction = "start" | "stop" | "restart";

/** CustomNode is one custom node a Managed instance reports installed.
 *
 * CustomNode 是一个 Managed 实例上报已安装的自定义节点。 */
export interface CustomNode {
  name: string;
  version: string;
}

/** InstanceStatus is the one Managed instance's status a connected replica
 * reported.
 *
 * InstanceStatus 是某个已连接副本报告的那一个 Managed 实例的状态。 */
export interface InstanceStatus {
  containerName: string;
  state: string;
  updatedAt: string;
  customNodes: CustomNode[];
}

/** ReplicaStatus is what happened when one configured Gateway replica was
 * asked.
 *
 * ReplicaStatus 是询问某个已配置 Gateway 副本时发生的事。 */
export interface ReplicaStatus {
  endpoint: string;
  connected: boolean;
  error: string;
}

/** ComfyUIManagedStatus is the parsed GET response.
 *
 * ComfyUIManagedStatus 是解析后的 GET 响应。 */
export interface ComfyUIManagedStatus {
  instances: InstanceStatus[];
  replicas: ReplicaStatus[];
}

/** ComfyUIManagedTriggerOutcome is the parsed response of a trigger or
 * install-custom-node call — only Replicas, since a write never returns
 * Instances (the same shape ComfyUIManagedStatus's replicas half has).
 *
 * ComfyUIManagedTriggerOutcome 是一次触发或自定义节点安装调用的解析结果
 * ——只有 Replicas，因为一次写操作从不返回 Instances（与
 * ComfyUIManagedStatus 的 replicas 那一半同一形状）。 */
export interface ComfyUIManagedTriggerOutcome {
  replicas: ReplicaStatus[];
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

function required(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (typeof value !== "string" || value === "") {
    fail();
  }
  return value;
}

function bool(source: Record<string, unknown>, key: string): boolean {
  const value = source[key];
  if (typeof value !== "boolean") {
    fail();
  }
  return value;
}

/** optionalText reads an `omitempty` string field, defaulting to "".
 * optionalText 读取一个 `omitempty` 字符串字段，缺席时取 ""。 */
function optionalText(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (value === undefined) {
    return "";
  }
  if (typeof value !== "string") {
    fail();
  }
  return value;
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

function parseCustomNode(value: unknown): CustomNode {
  const source = record(value);
  return { name: required(source, "name"), version: optionalText(source, "version") };
}

function parseInstance(value: unknown): InstanceStatus {
  const source = record(value);
  return {
    containerName: required(source, "container_name"),
    state: required(source, "state"),
    updatedAt: required(source, "updated_at"),
    customNodes: list(source.custom_nodes, parseCustomNode),
  };
}

function parseReplica(value: unknown): ReplicaStatus {
  const source = record(value);
  return {
    endpoint: required(source, "endpoint"),
    connected: bool(source, "connected"),
    error: optionalText(source, "error"),
  };
}

/** parseComfyUIManagedStatus validates a status GET response.
 * parseComfyUIManagedStatus 校验一次状态 GET 响应。 */
export function parseComfyUIManagedStatus(value: unknown): ComfyUIManagedStatus {
  const source = record(value);
  return {
    instances: list(source.instances, parseInstance),
    replicas: list(source.replicas, parseReplica),
  };
}

/** parseComfyUIManagedTriggerOutcome validates a trigger or custom-node
 * install response.
 *
 * parseComfyUIManagedTriggerOutcome 校验一次触发或自定义节点安装响应。 */
export function parseComfyUIManagedTriggerOutcome(value: unknown): ComfyUIManagedTriggerOutcome {
  const source = record(value);
  return { replicas: list(source.replicas, parseReplica) };
}

/** comfyUIManagedStatusRequest builds the status read for nodeId.
 * comfyUIManagedStatusRequest 构造对 nodeId 的状态读取。 */
export function comfyUIManagedStatusRequest(nodeId: string): RequestSpec<ComfyUIManagedStatus> {
  return {
    surface: "operator",
    method: "GET",
    path: `/operator/v1/nodes/${encodeURIComponent(nodeId)}/comfyui-managed`,
    parse: parseComfyUIManagedStatus,
  };
}

/** comfyUIManagedTriggerRequest builds a lifecycle-action trigger for
 * nodeId; the request layer never retries it, matching every other write
 * in this app.
 *
 * comfyUIManagedTriggerRequest 构造对 nodeId 的一次生命周期动作触发；请求层
 * 从不重试它，与本应用的其他每个写操作一致。 */
export function comfyUIManagedTriggerRequest(nodeId: string, action: ComfyUIManagedAction): RequestSpec<ComfyUIManagedTriggerOutcome> {
  return {
    surface: "operator",
    method: "POST",
    path: `/operator/v1/nodes/${encodeURIComponent(nodeId)}/comfyui-managed`,
    body: { action },
    parse: parseComfyUIManagedTriggerOutcome,
  };
}

/** comfyUIManagedCustomNodeInstallRequest builds a by-name custom node
 * install trigger for nodeId. It never carries a repository URL — only the
 * name, which the Agent's own local allowlist decides is known or not (see
 * this module's package doc and the subtask 4 design doc).
 *
 * comfyUIManagedCustomNodeInstallRequest 构造对 nodeId 的一次按名字触发的
 * 自定义节点安装。它从不携带仓库 URL——只有名字，是否已知由 Agent 自己本地
 * 的允许列表决定（见本模块包文档与子任务四设计文档）。 */
export function comfyUIManagedCustomNodeInstallRequest(nodeId: string, name: string): RequestSpec<ComfyUIManagedTriggerOutcome> {
  return {
    surface: "operator",
    method: "POST",
    path: `/operator/v1/nodes/${encodeURIComponent(nodeId)}/comfyui-managed/custom-nodes`,
    body: { name },
    parse: parseComfyUIManagedTriggerOutcome,
  };
}

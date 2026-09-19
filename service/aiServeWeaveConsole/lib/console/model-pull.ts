import type { RequestSpec } from "./api-client.ts";
import { ApiError } from "./errors.ts";

/**
 * This module mirrors GET/POST /operator/v1/nodes/:id/model-pulls
 * (internal/types.ModelPullStatusResponse/ModelPullTriggerResponse), which
 * the control plane builds by fanning a request out to every configured
 * Gateway replica and folding their answers (STATUS.md's P2 model
 * distribution, subtasks two through four).
 *
 * A trigger's 202 only confirms the request reached at least one connected
 * replica's tunnel — never that the Agent actually finished (or even
 * started) the pull. The real outcome is only observable by reading status
 * again afterward, which is why this page's actions never chain a re-fetch
 * as if they were synchronous — the same restraint comfyui-managed.ts uses.
 *
 * 本模块镜像 GET/POST /operator/v1/nodes/:id/model-pulls
 * (internal/types.ModelPullStatusResponse/ModelPullTriggerResponse)，控制面
 * 把请求扇出给每个已配置 Gateway 副本并折叠它们的回答来构造它（STATUS.md 的
 * P2 模型分发，子任务二至四）。
 *
 * 一次触发的 202 只确认请求到达了至少一个已连接副本的隧道——从不确认 Agent
 * 真的完成了（甚至开始了）这次拉取。真实结果只能通过之后再读一次状态来观
 * 察，这也是本页面的操作从不把重新拉取状态串成同步动作的原因，与
 * comfyui-managed.ts 同一种克制。
 */

/** ReplicaStatus is what happened when one configured Gateway replica was
 * asked.
 *
 * ReplicaStatus 是询问某个已配置 Gateway 副本时发生的事。 */
export interface ReplicaStatus {
  endpoint: string;
  connected: boolean;
  error: string;
}

/** PullStatus is one named pull's last known state, as reported by whichever
 * connected replica answered.
 *
 * PullStatus 是某个命名拉取的最后已知状态，由回答的那个已连接副本上报。 */
export interface PullStatus {
  name: string;
  /** state is one of "pending" | "downloading" | "done" | "failed" |
   * "unspecified" (common/modelpullstatus.State.String()).
   *
   * state 取值为 "pending" | "downloading" | "done" | "failed" |
   * "unspecified"（common/modelpullstatus.State.String()）。 */
  state: string;
  bytesDownloaded: number;
  bytesTotal: number;
  /** reason is only meaningful when state is "failed"; otherwise "".
   *
   * reason 只在 state 为 "failed" 时才有意义，否则为 ""。 */
  reason: string;
  updatedAt: string;
}

/** ModelPullStatusResult is the parsed GET response.
 *
 * ModelPullStatusResult 是解析后的 GET 响应。 */
export interface ModelPullStatusResult {
  pulls: PullStatus[];
  replicas: ReplicaStatus[];
}

/** ModelPullTriggerOutcome is the parsed response of a trigger call — only
 * Replicas, since a write never returns Pulls (the same shape
 * ModelPullStatusResult's replicas half has).
 *
 * ModelPullTriggerOutcome 是一次触发调用的解析结果——只有 Replicas，因为一
 * 次写操作从不返回 Pulls（与 ModelPullStatusResult 的 replicas 那一半同一
 * 形状）。 */
export interface ModelPullTriggerOutcome {
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

function num(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  if (typeof value !== "number" || !Number.isFinite(value)) {
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

function parsePull(value: unknown): PullStatus {
  const source = record(value);
  return {
    name: required(source, "name"),
    state: required(source, "state"),
    bytesDownloaded: num(source, "bytes_downloaded"),
    bytesTotal: num(source, "bytes_total"),
    reason: optionalText(source, "reason"),
    updatedAt: required(source, "updated_at"),
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

/** parseModelPullStatus validates a status GET response.
 * parseModelPullStatus 校验一次状态 GET 响应。 */
export function parseModelPullStatus(value: unknown): ModelPullStatusResult {
  const source = record(value);
  return {
    pulls: list(source.pulls, parsePull),
    replicas: list(source.replicas, parseReplica),
  };
}

/** parseModelPullTriggerOutcome validates a trigger response.
 * parseModelPullTriggerOutcome 校验一次触发响应。 */
export function parseModelPullTriggerOutcome(value: unknown): ModelPullTriggerOutcome {
  const source = record(value);
  return { replicas: list(source.replicas, parseReplica) };
}

/** modelPullStatusRequest builds the status read for nodeId.
 * modelPullStatusRequest 构造对 nodeId 的状态读取。 */
export function modelPullStatusRequest(nodeId: string): RequestSpec<ModelPullStatusResult> {
  return {
    surface: "operator",
    method: "GET",
    path: `/operator/v1/nodes/${encodeURIComponent(nodeId)}/model-pulls`,
    parse: parseModelPullStatus,
  };
}

/** modelPullTriggerRequest builds a by-name pull trigger for nodeId. names
 * must be non-empty — the control plane rejects an empty list as a 400
 * before it ever reaches a replica. Every name must already be declared in
 * that node's own local manifest; this call never carries a URL (see the
 * subtask 2/3 design docs).
 *
 * modelPullTriggerRequest 构造对 nodeId 的一次按名字触发拉取。names 必须非
 * 空——控制面在到达任何副本之前就会把空列表拒绝为 400。每个名字都必须已经
 * 声明在该节点自己的本地清单里；这次调用从不携带 URL（见子任务二/三设计
 * 文档）。 */
export function modelPullTriggerRequest(nodeId: string, names: string[]): RequestSpec<ModelPullTriggerOutcome> {
  return {
    surface: "operator",
    method: "POST",
    path: `/operator/v1/nodes/${encodeURIComponent(nodeId)}/model-pulls`,
    body: { names },
    parse: parseModelPullTriggerOutcome,
  };
}

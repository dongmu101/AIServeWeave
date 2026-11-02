import type { RequestSpec } from "./api-client.ts";
import { ApiError } from "./errors.ts";

/**
 * This module mirrors the node provider endpoints of the control plane:
 * GET/PUT/DELETE /admin/v1/nodes/:node_id/providers[/:runtime_id] for a
 * tenant's own nodes and /operator/v1/nodes/:id/providers[/:runtime_id] for
 * platform nodes (internal/types NodeProvidersResponse,
 * NodeProviderWriteResponse). The input checks mirror
 * common/remoteprovider.ValidateProvider so a typo is caught before a request;
 * the control plane checks again. The key is write-only: no response carries
 * it, only its last four characters.
 *
 * 本模块镜像控制面的节点供应商端点：商户自有节点用 GET/PUT/DELETE
 * /admin/v1/nodes/:node_id/providers[/:runtime_id]，平台节点用
 * /operator/v1/nodes/:id/providers[/:runtime_id]（internal/types 的
 * NodeProvidersResponse、NodeProviderWriteResponse）。输入检查镜像
 * common/remoteprovider.ValidateProvider，让笔误在请求前就被发现；控制面会再检查一次。
 * Key 只写不读：任何响应都不携带它，只给末四位。
 */

/** REMOTE_ID_PREFIX mirrors remoteprovider.IDPrefix. / REMOTE_ID_PREFIX 镜像 remoteprovider.IDPrefix。 */
export const REMOTE_ID_PREFIX = "remote-";
/** MAX_NODE_PROVIDERS mirrors remoteprovider.MaxProviders. / MAX_NODE_PROVIDERS 镜像 remoteprovider.MaxProviders。 */
export const MAX_NODE_PROVIDERS = 32;
const MAX_ID_BYTES = 64;
const MAX_KEY_BYTES = 16 << 10;
const MAX_URL_BYTES = 2048;
const MAX_MODELS = 256;
const MAX_MODEL_BYTES = 256;

/** HOSTED_KINDS mirrors runtime.Kind.IsHosted. / HOSTED_KINDS 镜像 runtime.Kind.IsHosted。 */
export const HOSTED_KINDS = ["openai", "anthropic", "gemini", "deepseek", "kimi", "qwen", "zhipu", "minimax", "openai_compatible", "anthropic_compatible"] as const;
/** HostedKind is one hosted provider kind. / HostedKind 是一种托管供应商种类。 */
export type HostedKind = (typeof HOSTED_KINDS)[number];

/** kindNeedsBaseURL reports the kinds without a default endpoint. / kindNeedsBaseURL 报告没有默认端点的种类。 */
export function kindNeedsBaseURL(kind: string): boolean {
  return kind === "openai_compatible" || kind === "anthropic_compatible";
}

/** ProviderState mirrors the per-runtime states the Agent reports. / ProviderState 镜像 Agent 回报的单个运行时状态。 */
export type ProviderState = "" | "active" | "conflict" | "failed" | "unknown";
const STATES: readonly ProviderState[] = ["", "active", "conflict", "failed", "unknown"];

/** ProviderOutcome mirrors remoteprovider.Outcome's String forms. / ProviderOutcome 镜像 remoteprovider.Outcome 的 String 形式。 */
export type ProviderOutcome = "none" | "applied" | "disabled" | "open_failed" | "invalid" | "stale" | "store_failed";
const OUTCOMES: readonly ProviderOutcome[] = ["none", "applied", "disabled", "open_failed", "invalid", "stale", "store_failed"];

/** NodeProvider mirrors types.NodeProvider. / NodeProvider 镜像 types.NodeProvider。 */
export interface NodeProvider {
  id: string;
  kind: string;
  baseUrl: string;
  models: string[];
  keyLast4: string;
  updatedBy: string;
  updatedAt: string;
  state: ProviderState;
}

/** NodeProviderReport mirrors types.NodeProviderReport. / NodeProviderReport 镜像 types.NodeProviderReport。 */
export interface NodeProviderReport {
  appliedVersion: number;
  attemptedVersion: number;
  outcome: ProviderOutcome;
  updatedAt: string | null;
}

/** NodeProviders mirrors types.NodeProvidersResponse. / NodeProviders 镜像 types.NodeProvidersResponse。 */
export interface NodeProviders {
  version: number;
  connected: boolean;
  report: NodeProviderReport | null;
  items: NodeProvider[];
}

/** NodeProviderWrite mirrors types.NodeProviderWriteResponse. / NodeProviderWrite 镜像 types.NodeProviderWriteResponse。 */
export interface NodeProviderWrite {
  item: NodeProvider | null;
  version: number;
  delivered: boolean;
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}

function version(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) throw new ApiError("contract");
  return value;
}

function time(value: unknown): string {
  if (typeof value !== "string" || !Number.isFinite(Date.parse(value))) throw new ApiError("contract");
  return value;
}

function parseProvider(raw: unknown): NodeProvider {
  const item = object(raw);
  if (typeof item.id !== "string" || !isRemoteId(item.id)) throw new ApiError("contract");
  if (typeof item.kind !== "string" || typeof item.base_url !== "string" || typeof item.key_last4 !== "string" || item.key_last4.length > 8) throw new ApiError("contract");
  if (!Array.isArray(item.models) || item.models.length > MAX_MODELS || !item.models.every((m) => typeof m === "string")) throw new ApiError("contract");
  if (typeof item.updated_by !== "string") throw new ApiError("contract");
  const state = item.state === undefined ? "" : item.state;
  if (!STATES.includes(state as ProviderState)) throw new ApiError("contract");
  return {
    id: item.id, kind: item.kind, baseUrl: item.base_url, models: item.models as string[], keyLast4: item.key_last4,
    updatedBy: item.updated_by, updatedAt: time(item.updated_at), state: state as ProviderState,
  };
}

/** parseNodeProviders validates GET .../providers. / parseNodeProviders 校验 GET .../providers。 */
export function parseNodeProviders(value: unknown): NodeProviders {
  const data = object(value);
  if (typeof data.connected !== "boolean" || !Array.isArray(data.items) || data.items.length > MAX_NODE_PROVIDERS) throw new ApiError("contract");
  const items = data.items.map(parseProvider);
  if (new Set(items.map((p) => p.id)).size !== items.length) throw new ApiError("contract");
  let report: NodeProviderReport | null = null;
  if (data.report !== undefined && data.report !== null) {
    const r = object(data.report);
    if (!OUTCOMES.includes(r.outcome as ProviderOutcome)) throw new ApiError("contract");
    report = {
      appliedVersion: version(r.applied_version), attemptedVersion: version(r.attempted_version),
      outcome: r.outcome as ProviderOutcome, updatedAt: r.updated_at === undefined || r.updated_at === null ? null : time(r.updated_at),
    };
  }
  return { version: version(data.version), connected: data.connected, report, items };
}

/** parseNodeProviderWrite validates a PUT or DELETE answer. / parseNodeProviderWrite 校验 PUT 或 DELETE 的响应。 */
export function parseNodeProviderWrite(value: unknown): NodeProviderWrite {
  const data = object(value);
  if (typeof data.delivered !== "boolean") throw new ApiError("contract");
  return { item: data.item === undefined || data.item === null ? null : parseProvider(data.item), version: version(data.version), delivered: data.delivered };
}

const utf8 = new TextEncoder();

/** isRemoteId mirrors remoteprovider.ValidID. / isRemoteId 镜像 remoteprovider.ValidID。 */
export function isRemoteId(id: string): boolean {
  const rest = id.startsWith(REMOTE_ID_PREFIX) ? id.slice(REMOTE_ID_PREFIX.length) : "";
  return rest !== "" && id.length <= MAX_ID_BYTES && /^[A-Za-z0-9._-]+$/.test(rest);
}

/** ProviderDraft is the edit form's content. An empty apiKey keeps the saved key. / ProviderDraft 是编辑表单的内容。apiKey 为空表示保留已保存的 Key。 */
export interface ProviderDraft {
  id: string;
  kind: string;
  baseUrl: string;
  apiKey: string;
  models: string;
}

/** ProviderProblem names why a draft cannot be sent. / ProviderProblem 说明草稿为何不能发送。 */
export type ProviderProblem = "id" | "kind" | "base_url" | "key" | "key_required" | "models";

/** parseModelList splits a comma or newline separated list. / parseModelList 拆分以逗号或换行分隔的列表。 */
export function parseModelList(text: string): string[] {
  return text.split(/[,\n]/).map((m) => m.trim()).filter((m) => m !== "");
}

function privateHost(host: string): boolean {
  const h = host.toLowerCase().replace(/^\[|\]$/g, "");
  if (h === "localhost" || h.endsWith(".localhost")) return true;
  const v4 = /^(\d+)\.(\d+)\.(\d+)\.(\d+)$/.exec(h);
  if (v4) {
    const [a, b] = [Number(v4[1]), Number(v4[2])];
    return a === 0 || a === 10 || a === 127 || (a === 169 && b === 254) || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 100 && b >= 64 && b <= 127);
  }
  return h.includes(":") && (h === "::" || h === "::1" || h.startsWith("fe80:") || h.startsWith("fc") || h.startsWith("fd"));
}

/**
 * checkProviderDraft returns the first problem with draft, or null. A key is
 * required for a new provider and whenever kind or base URL changes, the
 * control plane's rule that a key never follows its runtime to another
 * endpoint.
 *
 * checkProviderDraft 返回草稿的第一个问题，没有时返回 null。新建供应商，以及种类或 base URL
 * 改变时都要求填写 Key，即控制面「Key 从不跟随运行时换到另一个端点」的规则。
 */
export function checkProviderDraft(draft: ProviderDraft, saved: NodeProvider | null): ProviderProblem | null {
  if (!isRemoteId(draft.id)) return "id";
  if (!(HOSTED_KINDS as readonly string[]).includes(draft.kind)) return "kind";
  const base = draft.baseUrl.trim();
  if (base === "") {
    if (kindNeedsBaseURL(draft.kind)) return "base_url";
  } else {
    let url: URL;
    try { url = new URL(base); } catch { return "base_url"; }
    if (url.protocol !== "https:" || url.hostname === "" || url.username !== "" || url.password !== "" || utf8.encode(base).length > MAX_URL_BYTES || privateHost(url.hostname)) return "base_url";
  }
  const key = draft.apiKey;
  if (key === "") {
    if (saved === null || saved.kind !== draft.kind || saved.baseUrl !== base) return "key_required";
  } else if (key !== key.trim() || /[\r\n]/.test(key) || utf8.encode(key).length > MAX_KEY_BYTES) {
    return "key";
  }
  const models = parseModelList(draft.models);
  if (models.length > MAX_MODELS || new Set(models).size !== models.length || models.some((m) => utf8.encode(m).length > MAX_MODEL_BYTES || /[\s,]/.test(m))) return "models";
  return null;
}

/** NodeSurface picks the tenant or operator routes. / NodeSurface 选择商户或运维路由。 */
export type NodeSurface = "admin" | "operator";

function base(surface: NodeSurface, nodeId: string): string {
  return `/${surface}/v1/nodes/${encodeURIComponent(nodeId)}/providers`;
}

/** nodeProvidersRequest reads a node's providers. / nodeProvidersRequest 读取节点的供应商。 */
export function nodeProvidersRequest(surface: NodeSurface, nodeId: string): RequestSpec<NodeProviders> {
  return { surface, method: "GET", path: base(surface, nodeId), parse: parseNodeProviders };
}

/** putNodeProviderRequest writes one provider; an empty key is omitted. / putNodeProviderRequest 写入一个供应商；空 Key 被省略。 */
export function putNodeProviderRequest(surface: NodeSurface, nodeId: string, draft: ProviderDraft): RequestSpec<NodeProviderWrite> {
  const body: Record<string, unknown> = { kind: draft.kind, base_url: draft.baseUrl.trim(), models: parseModelList(draft.models) };
  if (draft.apiKey !== "") body.api_key = draft.apiKey;
  return { surface, method: "PUT", path: `${base(surface, nodeId)}/${encodeURIComponent(draft.id)}`, body, parse: parseNodeProviderWrite };
}

/** deleteNodeProviderRequest removes one provider. / deleteNodeProviderRequest 移除一个供应商。 */
export function deleteNodeProviderRequest(surface: NodeSurface, nodeId: string, id: string): RequestSpec<NodeProviderWrite> {
  return { surface, method: "DELETE", path: `${base(surface, nodeId)}/${encodeURIComponent(id)}`, parse: parseNodeProviderWrite };
}

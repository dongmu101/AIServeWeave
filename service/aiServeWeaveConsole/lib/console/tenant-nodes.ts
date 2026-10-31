import { parseBundleFor, type EnrollmentBundle } from "./agent-enrollment.ts";
import { ApiError } from "./errors.ts";
import { tenantNodeOwner } from "./node-access.ts";

/** MAX_TENANT_NODES_LISTED mirrors logic.MaxTenantNodesListed. / MAX_TENANT_NODES_LISTED 镜像 logic.MaxTenantNodesListed。 */
export const MAX_TENANT_NODES_LISTED = 200;

/** TenantNodeStatus mirrors types.TenantNode's status values. / TenantNodeStatus 镜像 types.TenantNode 的 status 取值。 */
export type TenantNodeStatus = "online" | "offline" | "unknown" | "revoked";

const STATUSES: readonly TenantNodeStatus[] = ["online", "offline", "unknown", "revoked"];

/** TenantNode mirrors types.TenantNode: one of the tenant's own nodes. / TenantNode 镜像 types.TenantNode：商户的一台自有节点。 */
export interface TenantNode {
  nodeId: string;
  createdBy: string;
  createdAt: string;
  enrollBy: string;
  revokedAt: string | null;
  status: TenantNodeStatus;
  agentVersion: string | null;
  lastHeartbeat: string | null;
}

/** TenantNodeList mirrors types.TenantNodeListResponse. / TenantNodeList 镜像 types.TenantNodeListResponse。 */
export interface TenantNodeList { items: TenantNode[]; maxPerTenant: number; maxCreatesPerDay: number }

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}

function time(value: unknown): string {
  if (typeof value !== "string" || !Number.isFinite(Date.parse(value))) throw new ApiError("contract");
  return value;
}

function optionalTime(value: unknown): string | null {
  return value === undefined || value === null ? null : time(value);
}

/** isTenantNodeId reports whether id is a well-formed tenant-owned node id. / isTenantNodeId 报告 id 是否为格式正确的商户自有节点 id。 */
export function isTenantNodeId(id: string): boolean {
  const owner = tenantNodeOwner(id);
  return owner !== null && owner !== "";
}

/**
 * parseTenantNodeList validates GET /admin/v1/nodes. Every id must be a
 * well-formed tenant-owned id, appear once, and the list stays within the
 * server's bound; a deleted node must say so in both fields.
 *
 * parseTenantNodeList 校验 GET /admin/v1/nodes。每个 id 都必须是格式正确的商户自有 id
 * 且只出现一次，列表不超出服务端上限；已删除的节点两个字段都必须如实表明。
 */
export function parseTenantNodeList(value: unknown): TenantNodeList {
  const data = object(value);
  if (!Array.isArray(data.items) || data.items.length > MAX_TENANT_NODES_LISTED) throw new ApiError("contract");
  const limits = [data.max_per_tenant, data.max_creates_per_day];
  if (!limits.every((n) => typeof n === "number" && Number.isInteger(n) && n >= 1 && n <= 1000)) throw new ApiError("contract");
  const seen = new Set<string>();
  const items = data.items.map((raw): TenantNode => {
    const item = object(raw);
    if (typeof item.node_id !== "string" || !isTenantNodeId(item.node_id) || seen.has(item.node_id)) throw new ApiError("contract");
    seen.add(item.node_id);
    if (typeof item.created_by !== "string" || !STATUSES.includes(item.status as TenantNodeStatus)) throw new ApiError("contract");
    if (item.agent_version !== undefined && (typeof item.agent_version !== "string" || item.agent_version.length > 128)) throw new ApiError("contract");
    const revokedAt = optionalTime(item.revoked_at);
    if ((revokedAt !== null) !== (item.status === "revoked")) throw new ApiError("contract");
    return {
      nodeId: item.node_id,
      createdBy: item.created_by,
      createdAt: time(item.created_at),
      enrollBy: time(item.enroll_by),
      revokedAt,
      status: item.status as TenantNodeStatus,
      agentVersion: typeof item.agent_version === "string" ? item.agent_version : null,
      lastHeartbeat: optionalTime(item.last_heartbeat),
    };
  });
  return { items, maxPerTenant: data.max_per_tenant as number, maxCreatesPerDay: data.max_creates_per_day as number };
}

/** parseTenantNodeBundle validates POST /admin/v1/nodes, a bundle for a tenant-owned id. / parseTenantNodeBundle 校验 POST /admin/v1/nodes，即商户自有 id 的接入包。 */
export function parseTenantNodeBundle(value: unknown): EnrollmentBundle {
  const bundle = parseBundleFor(value, /^tnode\.[A-Za-z0-9_-]{1,89}\.[a-f0-9]{32}$/);
  if (!isTenantNodeId(bundle.nodeId)) throw new ApiError("contract");
  return bundle;
}

/** activeNodeCount counts the nodes not deleted, which MaxPerTenant bounds. / activeNodeCount 统计未删除的节点数，即 MaxPerTenant 所限制的数量。 */
export function activeNodeCount(list: TenantNodeList): number {
  return list.items.filter((node) => node.revokedAt === null).length;
}

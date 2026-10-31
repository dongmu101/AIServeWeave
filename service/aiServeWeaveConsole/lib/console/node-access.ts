import { ApiError } from "./errors.ts";

/** MAX_DEDICATED_NODES mirrors common/nodeaccess.MaxDedicatedNodes. / MAX_DEDICATED_NODES 镜像 common/nodeaccess.MaxDedicatedNodes。 */
export const MAX_DEDICATED_NODES = 1000;
/** MAX_TENANTS_PER_NODE mirrors common/nodeaccess.MaxTenantsPerNode. / MAX_TENANTS_PER_NODE 镜像 common/nodeaccess.MaxTenantsPerNode。 */
export const MAX_TENANTS_PER_NODE = 64;
/** MAX_ID_BYTES mirrors common/nodeaccess.MaxIDBytes. / MAX_ID_BYTES 镜像 common/nodeaccess.MaxIDBytes。 */
export const MAX_ID_BYTES = 128;

/** NodeAccess mirrors types.NodeAccess: one dedicated node and its tenants. / NodeAccess 镜像 types.NodeAccess：一台专属节点及其租户。 */
export interface NodeAccess { nodeId: string; tenants: string[]; updatedBy: string | null; updatedAt: string | null }

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}

/** validId mirrors common/nodeaccess's id rule: non empty, bounded, no whitespace or control characters. / validId 镜像 common/nodeaccess 的 id 规则：非空、有界、不含空白与控制字符。 */
export function validId(id: string): boolean {
  return id !== "" && new TextEncoder().encode(id).length <= MAX_ID_BYTES && !/[\s\p{Cc}]/u.test(id);
}

/** TENANT_NODE_PREFIX mirrors common/nodeaccess.TenantNodePrefix. / TENANT_NODE_PREFIX 镜像 common/nodeaccess.TenantNodePrefix。 */
export const TENANT_NODE_PREFIX = "tnode.";

/**
 * tenantNodeOwner mirrors common/nodeaccess.OwnerOf: null for a node that is
 * not tenant-owned, otherwise its owner, or "" for a malformed id that no
 * tenant may use.
 *
 * tenantNodeOwner 镜像 common/nodeaccess.OwnerOf：不是商户自有节点时返回 null，否则返回
 * 其所有者；格式不对、任何商户都不能用的 id 返回 ""。
 */
export function tenantNodeOwner(nodeId: string): string | null {
  if (!nodeId.startsWith(TENANT_NODE_PREFIX)) return null;
  if (new TextEncoder().encode(nodeId).length > MAX_ID_BYTES) return "";
  const rest = nodeId.slice(TENANT_NODE_PREFIX.length);
  const dot = rest.lastIndexOf(".");
  if (dot <= 0) return "";
  const tenant = rest.slice(0, dot);
  return tenant.includes(".") || !validId(tenant) || !/^[0-9a-f]{32}$/.test(rest.slice(dot + 1)) ? "" : tenant;
}

/** parseNodeAccess validates one binding. A write that made the node shared answers with no tenants. / parseNodeAccess 校验一条绑定。让节点恢复共享的写入以空租户列表作答。 */
export function parseNodeAccess(value: unknown): NodeAccess {
  const source = object(value);
  if (typeof source.node_id !== "string" || !validId(source.node_id) || !Array.isArray(source.tenants) || source.tenants.length > MAX_TENANTS_PER_NODE) throw new ApiError("contract");
  const tenants = source.tenants.map((tenant) => { if (typeof tenant !== "string" || !validId(tenant)) throw new ApiError("contract"); return tenant; });
  const optional = (key: string) => {
    const field = source[key];
    if (field === undefined || field === null || field === "") return null;
    if (typeof field !== "string") throw new ApiError("contract");
    return field;
  };
  return { nodeId: source.node_id, tenants, updatedBy: optional("updated_by"), updatedAt: optional("updated_at") };
}

/** parseNodeAccessList validates GET /operator/v1/node-access, where every item is dedicated. / parseNodeAccessList 校验 GET /operator/v1/node-access，其中每一项都是专属节点。 */
export function parseNodeAccessList(value: unknown): NodeAccess[] {
  const source = object(value);
  if (!Array.isArray(source.items) || source.items.length > MAX_DEDICATED_NODES) throw new ApiError("contract");
  const items = source.items.map(parseNodeAccess);
  if (items.some((item) => item.tenants.length === 0) || new Set(items.map((item) => item.nodeId)).size !== items.length) throw new ApiError("contract");
  return items;
}

/** normalizeTenants mirrors common/nodeaccess.NormalizeTenants so the page can reject a list before sending it. / normalizeTenants 镜像 common/nodeaccess.NormalizeTenants，使页面在发送前即可拒绝非法列表。 */
export function normalizeTenants(tenants: readonly string[]): string[] {
  const out = new Set<string>();
  for (const raw of tenants) {
    const tenant = raw.trim();
    if (!validId(tenant)) throw new ApiError("invalid");
    out.add(tenant);
  }
  if (out.size > MAX_TENANTS_PER_NODE) throw new ApiError("invalid");
  return [...out].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
}

/** NodeRow is one node the page lists: seen in the fleet, bound, or both. / NodeRow 是页面列出的一台节点：在机群中看到的、已绑定的，或两者皆是。 */
export interface NodeRow { nodeId: string; live: boolean | null; tenants: string[] }

/** mergeNodeRows lists every fleet node and every bound node once, sorted by id. A bound node absent from the fleet has live null. / mergeNodeRows 把机群节点与已绑定节点各列一次并按 id 排序。不在机群中的已绑定节点 live 为 null。 */
export function mergeNodeRows(fleet: readonly { nodeId: string; live: boolean }[], bindings: readonly NodeAccess[]): NodeRow[] {
  const rows = new Map<string, NodeRow>();
  for (const node of fleet) rows.set(node.nodeId, { nodeId: node.nodeId, live: node.live, tenants: [] });
  for (const binding of bindings) rows.set(binding.nodeId, { nodeId: binding.nodeId, live: rows.get(binding.nodeId)?.live ?? null, tenants: binding.tenants });
  return [...rows.values()].sort((a, b) => (a.nodeId < b.nodeId ? -1 : a.nodeId > b.nodeId ? 1 : 0));
}

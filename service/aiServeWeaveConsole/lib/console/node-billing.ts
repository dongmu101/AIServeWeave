import { ApiError } from "./errors.ts";
import { MAX_DEDICATED_NODES, validId } from "./node-access.ts";

/** NodeBilling mirrors types.NodeBilling: whether a platform node is free.
 * NodeBilling 镜像 types.NodeBilling：一台平台节点是否免费。 */
export interface NodeBilling { nodeId: string; free: boolean }

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}

/** parseNodeBilling validates one node's billing setting.
 * parseNodeBilling 校验一台节点的收费设置。 */
export function parseNodeBilling(value: unknown): NodeBilling {
  const source = object(value);
  if (typeof source.node_id !== "string" || !validId(source.node_id) || typeof source.free !== "boolean") throw new ApiError("contract");
  return { nodeId: source.node_id, free: source.free };
}

/** parseNodeBillingList validates GET /operator/v1/node-billing and returns
 * the free nodes; a node absent from it is charged.
 * parseNodeBillingList 校验 GET /operator/v1/node-billing 并返回免费节点；不在其中的节点收费。 */
export function parseNodeBillingList(value: unknown): Set<string> {
  const source = object(value);
  if (!Array.isArray(source.items) || source.items.length > MAX_DEDICATED_NODES) throw new ApiError("contract");
  const free = new Set<string>();
  for (const item of source.items) {
    const entry = parseNodeBilling(item);
    if (!entry.free || free.has(entry.nodeId)) throw new ApiError("contract");
    free.add(entry.nodeId);
  }
  return free;
}

import type { RequestSpec } from "./api-client.ts";
import { parseNoContent } from "./contract.ts";
import { ApiError } from "./errors.ts";

/** NodeState is Registry intent, independent of Gateway liveness.
 * NodeState 是 Registry 的期望状态，与 Gateway 活性独立。 */
export interface NodeState {
  nodeId: string;
  pendingApproval: boolean;
  disabled: boolean;
  maintenance: boolean;
  firstSeenAt: string;
  lastSeenAt: string;
}

/** NodeAction names the five platform node operations.
 * NodeAction 命名五种平台节点操作。 */
export type NodeAction = "approve" | "disable" | "enable" | "maintenance" | "resume";

/** NODE_ACTION_LABELS names operations without claiming propagation.
 * NODE_ACTION_LABELS 为操作提供名称，不声称已传播。 */
export const NODE_ACTION_LABELS: Record<NodeAction, string> = {
  approve: "审批通过", disable: "禁用节点", enable: "启用节点", maintenance: "进入维护", resume: "退出维护",
};

/** parseNodeStates validates the Registry ledger response, including offline nodes.
 * parseNodeStates 校验 Registry 账本响应，其中包括离线节点。 */
export function parseNodeStates(value: unknown): NodeState[] {
  if (!value || typeof value !== "object" || !("items" in value) || !Array.isArray(value.items)) throw new ApiError("contract");
  return value.items.map((item: unknown) => {
    if (!item || typeof item !== "object") throw new ApiError("contract");
    const row = item as Record<string, unknown>;
    const { node_id, pending_approval, disabled, maintenance, first_seen_at, last_seen_at } = row;
    if (typeof node_id !== "string" || !node_id || typeof pending_approval !== "boolean" || typeof disabled !== "boolean" || typeof maintenance !== "boolean" || typeof first_seen_at !== "string" || Number.isNaN(Date.parse(first_seen_at)) || typeof last_seen_at !== "string" || Number.isNaN(Date.parse(last_seen_at))) throw new ApiError("contract");
    return { nodeId: node_id, pendingApproval: pending_approval, disabled, maintenance, firstSeenAt: first_seen_at, lastSeenAt: last_seen_at };
  });
}

/** nodeActionRequest constructs a single write; the request layer never retries writes.
 * nodeActionRequest 构造单次写入；请求层绝不重试写操作。 */
export function nodeActionRequest(nodeId: string, action: NodeAction): RequestSpec<null> {
  return { surface: "operator", method: action === "resume" ? "DELETE" : "POST", path: `/operator/v1/nodes/${encodeURIComponent(nodeId)}/${action === "resume" ? "maintenance" : action}`, body: {}, parse: parseNoContent };
}

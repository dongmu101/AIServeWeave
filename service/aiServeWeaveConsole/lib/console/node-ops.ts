import { coreTranslator } from "../i18n/core.ts";
import { DEFAULT_LOCALE, type Locale } from "../i18n/locale.ts";
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

/** NodeAction names the six platform node operations.
 * NodeAction 命名六种平台节点操作。 */
export type NodeAction = "approve" | "disable" | "enable" | "maintenance" | "resume" | "delete";

/** nodeActionLabels names operations without claiming propagation.
 * nodeActionLabels 为操作提供名称，不声称已传播。 */
export function nodeActionLabels(locale: Locale = DEFAULT_LOCALE): Record<NodeAction, string> {
  const t = coreTranslator(locale);
  return {
  approve: t("common.approved"), disable: t("tenant.disableNode"), enable: t("tenant.enableNode"), maintenance: t("tenant.enterMaintenance"), resume: t("tenant.exitMaintenance"), delete: t("tenant.deleteNode"),
};
}
/** NODE_ACTION_LABELS preserves default-language compatibility. / NODE_ACTION_LABELS 保留默认语言的兼容接口。 */
export const NODE_ACTION_LABELS = nodeActionLabels();

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
 * Delete targets the node itself; resume deletes the maintenance mark.
 * nodeActionRequest 构造单次写入；请求层绝不重试写操作。
 * 删除作用于节点本身；退出维护删除的是维护标记。 */
export function nodeActionRequest(nodeId: string, action: NodeAction): RequestSpec<null> {
  const node = `/operator/v1/nodes/${encodeURIComponent(nodeId)}`;
  if (action === "delete") return { surface: "operator", method: "DELETE", path: node, body: {}, parse: parseNoContent };
  return { surface: "operator", method: action === "resume" ? "DELETE" : "POST", path: `${node}/${action === "resume" ? "maintenance" : action}`, body: {}, parse: parseNoContent };
}

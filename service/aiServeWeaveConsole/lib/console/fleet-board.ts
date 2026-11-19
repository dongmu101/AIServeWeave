import type { FleetNode } from "./fleet.ts";
import { matchesQuery } from "./format.ts";
import type { NodeAccess, NodeAccessMode } from "./node-access.ts";
import type { NodeState } from "./node-ops.ts";

/**
 * The fleet board joins three independent reads by node ID: Registry intent,
 * Gateway observation and the saved sharing policy. Joining them per node puts
 * everything an operator needs about one machine in one place, but each source
 * keeps its own field, so intent is never presented as an observation and a
 * source that could not be read is "unknown" rather than "absent".
 *
 * 机群面板按节点 ID 合并三份相互独立的读取：Registry 期望、Gateway 观测与已保存的
 * 共享策略。按节点合并让运维在一处看到一台机器的全部信息，但每份来源保留自己的字段：
 * 期望不会被当作观测呈现，读取失败的来源是「未知」而不是「不存在」。
 */

/** Liveness is the Gateway's verdict; "unobserved" means the fleet was read
 * and no replica reported the node, "unknown" means the fleet was not read.
 * Liveness 是 Gateway 的判定；unobserved 表示机群已读取但没有副本报告该节点，
 * unknown 表示机群未能读取。 */
export type Liveness = "online" | "offline" | "unobserved" | "unknown";

/** FleetBoardNode is one machine as the three sources describe it.
 * FleetBoardNode 是三份来源各自描述的同一台机器。 */
export interface FleetBoardNode {
  nodeId: string;
  /** Registry intent; null when the ledger has no record or was not read. / Registry 期望；账本无记录或未读取时为 null。 */
  desired: NodeState | null;
  /** Whether the Registry has a record; null when the ledger was not read. / Registry 是否有记录；账本未读取时为 null。 */
  inRegistry: boolean | null;
  /** Gateway observation; null when no replica reported it or the fleet was not read. / Gateway 观测；无副本报告或未读取时为 null。 */
  observed: FleetNode | null;
  liveness: Liveness;
  /** Saved sharing mode; null when the policy was not read. / 已保存的共享方式；策略未读取时为 null。 */
  access: NodeAccessMode | null;
}

/** FleetStatusFilter names the status tiles, in display order.
 * FleetStatusFilter 命名状态卡片，按展示顺序排列。 */
export const FLEET_STATUS_FILTERS = ["all", "pending", "online", "offline", "maintenance", "disabled"] as const;
export type FleetStatusFilter = (typeof FLEET_STATUS_FILTERS)[number];

function rank(node: FleetBoardNode): number {
  if (node.desired?.pendingApproval) return 0;
  return node.liveness === "online" ? 1 : 2;
}

/**
 * mergeFleetBoard joins the reads. A null argument means that source was not
 * read. Nodes awaiting approval come first, then online nodes, then the rest,
 * each group by node ID, so a refresh never reshuffles a stable fleet.
 *
 * mergeFleetBoard 合并各份读取；参数为 null 表示该来源未读取。待审批节点在前，其次是
 * 在线节点，然后是其余节点，组内按节点 ID 排序，刷新不会打乱稳定的机群顺序。
 */
export function mergeFleetBoard(
  desired: readonly NodeState[] | null,
  observed: readonly FleetNode[] | null,
  access: readonly NodeAccess[] | null,
): FleetBoardNode[] {
  const states = new Map(desired?.map((state) => [state.nodeId, state]));
  const nodes = new Map(observed?.map((node) => [node.nodeId, node]));
  const modes = new Map(access?.map((entry) => [entry.nodeId, entry.mode]));
  const ids = [...new Set([...states.keys(), ...nodes.keys()])];
  return ids
    .map((nodeId): FleetBoardNode => {
      const node = nodes.get(nodeId) ?? null;
      return {
        nodeId,
        desired: states.get(nodeId) ?? null,
        inRegistry: desired === null ? null : states.has(nodeId),
        observed: node,
        liveness: observed === null ? "unknown" : node === null ? "unobserved" : node.live ? "online" : "offline",
        access: access === null ? null : modes.get(nodeId) ?? "private",
      };
    })
    .sort((a, b) => rank(a) - rank(b) || (a.nodeId < b.nodeId ? -1 : a.nodeId > b.nodeId ? 1 : 0));
}

/** matchesStatus reports whether a node belongs under a status tile. "offline"
 * covers nodes the fleet read did not include; an unknown liveness is neither
 * online nor offline.
 * matchesStatus 判断节点是否属于某个状态卡片。「离线」包含机群读取中未出现的节点；
 * 未知活性既不算在线也不算离线。 */
export function matchesStatus(node: FleetBoardNode, filter: FleetStatusFilter): boolean {
  switch (filter) {
    case "all": return true;
    case "pending": return node.desired?.pendingApproval === true;
    case "online": return node.liveness === "online";
    case "offline": return node.liveness === "offline" || node.liveness === "unobserved";
    case "maintenance": return node.desired?.maintenance === true || node.observed?.maintenance === true;
    case "disabled": return node.desired?.disabled === true;
  }
}

/** countByStatus counts each tile over the merged board.
 * countByStatus 在合并后的面板上统计每个卡片。 */
export function countByStatus(nodes: readonly FleetBoardNode[]): Record<FleetStatusFilter, number> {
  return Object.fromEntries(
    FLEET_STATUS_FILTERS.map((filter) => [filter, nodes.filter((node) => matchesStatus(node, filter)).length]),
  ) as Record<FleetStatusFilter, number>;
}

/** matchesFleetQuery searches the node ID and what the Agent reported about
 * itself: version, labels and runtimes.
 * matchesFleetQuery 搜索节点 ID 以及 Agent 的自述：版本、标签与运行时。 */
export function matchesFleetQuery(node: FleetBoardNode, query: string): boolean {
  const observed = node.observed;
  return matchesQuery(
    [
      node.nodeId,
      ...(observed
        ? [
            observed.agentVersion,
            ...Object.entries(observed.labels).map(([name, value]) => `${name}=${value}`),
            ...observed.runtimes.map((runtime) => `${runtime.id} ${runtime.kind}`),
          ]
        : []),
    ],
    query,
  );
}

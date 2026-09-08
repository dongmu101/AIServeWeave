"use client";

import * as React from "react";
import { FleetView } from "@/app/console/fleet/fleet-view";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { nodePathSegment } from "@/lib/console/node-path";
import { describe } from "@/lib/console/errors";
import { formatDateTime, matchesQuery } from "@/lib/console/format";
import { NODE_ACTION_LABELS, nodeActionRequest, parseNodeStates, type NodeAction } from "@/lib/console/node-ops";

const EFFECTS: Record<NodeAction, string> = {
  approve: "清除待审批标记；节点仍需成功连接，才会出现在实时机群中。",
  disable: "将该节点标记为禁用，拒绝后续注册、续期与 Gateway 握手。Gateway 收到更新后关闭控制连接和空闲槽；正在处理的请求不被主动中断，继续完成或自然失败。",
  enable: "清除禁用标记，不会清除待审批或维护标记，也不保证节点已重新连接。",
  maintenance: "要求 Gateway 停止向该节点派发新请求，保留连接并完成在途工作。",
  resume: "清除维护标记。节点还需在线、通过审批且未被禁用，才可能再次接收请求。",
};

/** NodeOperationsView keeps desired state and observed liveness independently readable.
 * NodeOperationsView 让期望状态与观测活性可独立读取。 */
export function NodeOperationsView() {
  const registry = useResource({ method: "GET", surface: "operator", path: "/operator/v1/nodes/states", parse: parseNodeStates });
  const run = useConsoleRequest();
  const [query, setQuery] = React.useState("");
  const [selected, setSelected] = React.useState<{ nodeId: string; action: NodeAction } | null>(null);
  const [fleetRevision, setFleetRevision] = React.useState(0);
  const [notice, setNotice] = React.useState<string | null>(null);
  const shown = registry.data?.filter((node) => matchesQuery([node.nodeId], query)) ?? [];

  return <div className="grid gap-8">
    <section className="grid gap-4" aria-labelledby="registry-title">
      <div>
        <h1 id="registry-title" className="font-heading text-lg font-semibold">节点管理</h1>
        <p className="text-sm text-muted-foreground">Registry 记录的期望状态，包括待审批及未连接的节点。最近登记时间不是心跳，不能据此判断在线。操作成功仅表示 Registry 已接受变更；Gateway 的实际生效情况请独立刷新下方实时机群核对。</p>
      </div>
      <div className="flex flex-wrap gap-3">
        <Input className="max-w-xs" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="筛选 Registry 节点 ID" aria-label="筛选 Registry 节点 ID" />
        <Button variant="outline" disabled={registry.loading} onClick={registry.reload}>刷新 Registry 状态</Button>
      </div>
      {notice ? <p role="status" className="text-sm">{notice}</p> : null}
      {registry.error ? <ErrorState message={registry.error} onRetry={registry.reload} /> : registry.loading ? <LoadingState label="正在读取 Registry 状态" /> : shown.length === 0 ? <EmptyState title={registry.data?.length ? "没有符合筛选条件的节点" : "Registry 尚无节点记录"} /> : <div className="grid gap-3">
        {shown.map((node) => <div key={node.nodeId} className="grid gap-3 rounded-xl border p-4">
          <div className="flex flex-wrap items-center gap-2">
            <code className="min-w-0 break-all text-sm">{node.nodeId}</code>
            <Badge variant="outline">{node.pendingApproval ? "待审批" : "已审批"}</Badge>
            <Badge variant={node.disabled ? "destructive" : "outline"}>{node.disabled ? "已禁用" : "未禁用"}</Badge>
            <Badge variant="outline">{node.maintenance ? "维护中（期望）" : "未要求维护"}</Badge>
          </div>
          {!nodePathSegment(node.nodeId) ? <p className="text-sm text-muted-foreground">该节点 ID 包含路径分隔符或控制字符，当前 HTTP 管理接口不支持；请通过 Registry 运维工具操作。</p> : null}
          <p className="text-xs text-muted-foreground">首次登记：{formatDateTime(node.firstSeenAt)} · 最近登记：{formatDateTime(node.lastSeenAt)}</p>
          <div className="flex flex-wrap gap-2">
            {(node.pendingApproval ? ["approve" as const] : []).map((action) => <Button key={action} variant="outline" disabled={selected !== null || !nodePathSegment(node.nodeId)} onClick={() => setSelected({ nodeId: node.nodeId, action })}>{NODE_ACTION_LABELS[action]}</Button>)}
            {([node.disabled ? "enable" : "disable", node.maintenance ? "resume" : "maintenance"] as NodeAction[]).map((action) => <Button key={action} variant="outline" disabled={selected !== null || !nodePathSegment(node.nodeId)} onClick={() => setSelected({ nodeId: node.nodeId, action })}>{NODE_ACTION_LABELS[action]}</Button>)}
          </div>
        </div>)}
      </div>}
    </section>
    <FleetView key={fleetRevision} title="实时机群（Gateway 观测）" />
    <ConfirmDialog open={selected !== null} onOpenChange={(open) => { if (!open) setSelected(null); }} title={selected ? NODE_ACTION_LABELS[selected.action] : "节点操作"} confirmLabel="确认执行" destructive={selected?.action === "disable"} description={selected ? <><span className="break-all font-mono">{selected.nodeId}</span>：{EFFECTS[selected.action]} 状态传播需要时间。</> : ""} onConfirm={async () => {
      if (!selected) return;
      setNotice(null);
      try {
        await run(nodeActionRequest(selected.nodeId, selected.action));
        setFleetRevision((revision) => revision + 1);
        setNotice(`${NODE_ACTION_LABELS[selected.action]}：Registry 已接受变更，尚未确认所有 Gateway 生效。`);
      } catch (failure) {
        throw new Error(`${describe(failure)} 写入结果未确认；请刷新状态后再决定是否重新操作。`);
      } finally {
        registry.reload();
      }
    }} />
  </div>;
}

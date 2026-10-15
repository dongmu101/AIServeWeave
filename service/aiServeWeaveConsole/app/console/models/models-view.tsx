"use client";

import * as React from "react";
import Link from "next/link";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { parseGatewaySnapshot, type FleetNode, type FleetSnapshot, type NodeRuntime } from "@/lib/console/fleet";
import { formatDateTime, matchesQuery } from "@/lib/console/format";

const STATE_LABELS: Record<string, string> = {
  healthy: "健康", unhealthy: "异常", registering: "注册中", unknown: "未知", closed: "已关闭",
};
const SUPPORT_LABELS: Record<string, string> = {
  supported: "支持", unsupported: "不支持", unknown: "未知",
};

/** ModelsView reads Gateway-local backends and connected Agent models.
 * ModelsView 读取 Gateway 本机后端与已连接 Agent 的模型。 */
export function ModelsView() {
  const fleet = useResource<FleetSnapshot>({
    method: "GET", surface: "operator", path: "/operator/v1/nodes", parse: parseGatewaySnapshot,
    query: { gateway: "default" },
  });
  const [query, setQuery] = React.useState("");
  const [onlineOnly, setOnlineOnly] = React.useState(false);
  const { loading, reload } = fleet;

  React.useEffect(() => {
    if (loading) return;
    const timer = window.setInterval(() => {
      if (!document.hidden) reload();
    }, 15_000);
    return () => window.clearInterval(timer);
  }, [loading, reload]);

  const nodes = fleet.data?.nodes ?? [];
  const localRuntimes = fleet.data?.replicas[0]?.localRuntimes ?? [];
  const shownLocal = localRuntimes.filter(rt => matchesQuery([
    rt.id, rt.kind, rt.baseUrl, ...rt.models.map(model => model.id),
  ], query));
  const runtimes = [...localRuntimes, ...nodes.flatMap(node => node.runtimes)];
  const models = new Set(runtimes.flatMap(rt => rt.models.map(model => model.id)));
  const shown = nodes.filter(node => (!onlineOnly || node.live) && matchesQuery([
    node.nodeId, node.agentVersion, ...node.replicas,
    ...node.runtimes.flatMap(rt => [rt.id, rt.kind, rt.baseUrl, ...rt.models.map(model => model.id)]),
  ], query));

  return <div className="grid gap-4">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 className="font-heading text-lg font-semibold">当前 Gateway 的本地 AI 模型</h1>
        <p className="text-sm text-muted-foreground">显示此 Gateway 本机发现的 AI 服务，以及已连接 Agent 上报的后端与模型。</p>
      </div>
      <Button variant="outline" disabled={loading} onClick={reload} aria-busy={loading}>刷新连接与模型</Button>
    </div>
    <p className="text-xs text-muted-foreground">每 15 秒自动刷新，后台标签页暂停刷新。模型 ID 来自后端上报；调用名称与可调度范围请查看 <Link className="underline" href="/operator/routes">模型路由</Link>。</p>
    {fleet.error ? <ErrorState message={fleet.error} onRetry={reload} /> : loading || !fleet.data ?
      <LoadingState label="正在读取当前 Gateway 的 AI 连接与模型" rows={4} /> : fleet.data.partial ?
      <ErrorState message="无法读取当前 Gateway 的连接与模型，请确认 Gateway 正常运行。" onRetry={reload} /> : <>
        <div className="rounded-lg border px-4 py-3 text-sm">
          <p>当前 Gateway：<code className="break-all">{fleet.data.replicas[0]?.replicaId ?? "未上报"}</code></p>
          <p className="break-all text-xs text-muted-foreground">{fleet.data.replicas[0]?.endpoint} · 采集于 {formatDateTime(fleet.data.collectedAt)}</p>
        </div>
        <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Summary label="在线节点" value={`${nodes.filter(node => node.live).length} / ${nodes.length}`} />
          <Summary label="AI 后端" value={String(runtimes.length)} />
          <Summary label="健康后端（本机与在线节点）" value={String([...localRuntimes, ...nodes.filter(node => node.live).flatMap(node => node.runtimes)].filter(rt => rt.state === "healthy").length)} />
          <Summary label="发现的模型（ID 去重）" value={String(models.size)} />
        </dl>
        <div className="flex flex-wrap items-center gap-3">
          <Input className="max-w-sm" aria-label="筛选节点、后端或模型" placeholder="搜索节点、后端、地址或模型 ID" value={query} onChange={event => setQuery(event.target.value)} />
          <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={onlineOnly} onChange={event => setOnlineOnly(event.target.checked)} />仅显示在线节点</label>
          <span className="text-xs text-muted-foreground">显示 {shown.length} / 已采集 {nodes.length} 个节点</span>
        </div>
        <Card>
          <CardHeader>
            <CardTitle>Gateway 本机 AI 服务</CardTitle>
            <p className="text-xs text-muted-foreground">本 Gateway 进程所在环境的 Ollama / vLLM 服务。这里展示发现结果；通过 Gateway API 调用仍需接入 Agent。</p>
          </CardHeader>
          <CardContent className="grid gap-3">
            {shownLocal.length > 0 ? shownLocal.map(rt => <RuntimeCard key={rt.id} runtime={rt} />) :
              <p className="text-sm text-muted-foreground">{localRuntimes.length === 0 ? "尚未发现 Gateway 本机 AI 服务。请确认服务已启动并启用本机发现。" : "没有符合搜索条件的本机 AI 服务。"}</p>}
          </CardContent>
        </Card>
        <h2 className="text-sm font-semibold">已连接 Agent 的 AI 服务</h2>
        {shown.length === 0 ? <EmptyState
          title={nodes.length === 0 ? "当前 Gateway 尚无 AI 节点连接" : "没有符合筛选条件的节点"}
          description={nodes.length === 0 ? "请确认 Agent 已连接上方所示的 Gateway。" : "尝试清空搜索或取消仅显示在线节点。"}
        /> : shown.map(node => <ConnectionCard key={node.nodeId} node={node} />)}
      </>}
  </div>;
}

/** Summary labels counts within the collected snapshot.
 * Summary 标示本次采集快照内的计数。 */
function Summary({ label, value }: { label: string; value: string }) {
  return <div className="rounded-xl border p-4"><dt className="text-xs text-muted-foreground">{label}</dt><dd className="mt-1 text-xl font-semibold">{value}</dd></div>;
}

/** ConnectionCard groups local backends under their connected Agent.
 * ConnectionCard 按接入的 Agent 分组展示本地后端。 */
function ConnectionCard({ node }: { node: FleetNode }) {
  return <Card>
    <CardHeader>
      <CardTitle className="flex flex-wrap items-center gap-2">
        <code className="break-all text-sm">{node.nodeId}</code>
        <Badge variant={node.live ? "secondary" : "outline"}>{node.live ? "节点在线" : "节点离线"}</Badge>
        {node.maintenance ? <Badge variant="outline">维护中</Badge> : null}
        {node.draining ? <Badge variant="outline">正在退出</Badge> : null}
      </CardTitle>
      <dl className="grid gap-2 text-xs text-muted-foreground sm:grid-cols-3">
        <Fact label="Agent 版本" value={node.agentVersion || "未上报"} />
        <Fact label="最近心跳" value={formatDateTime(node.lastHeartbeat, "未上报")} />
        <Fact label="报告节点的 Gateway" value={node.replicas.join("、") || "未上报"} />
      </dl>
      {!node.live ? <p className="text-xs text-muted-foreground">节点已离线，下方后端与模型信息为最近一次上报。</p> : null}
    </CardHeader>
    <CardContent className="grid gap-3">
      {node.runtimes.length === 0 ? <p className="text-sm text-muted-foreground">尚未上报 AI 后端。{node.declaredRuntimeIds.length > 0 ? `已声明：${node.declaredRuntimeIds.join("、")}` : "等待 Agent 完成运行时发现。"}</p> :
        node.runtimes.map(rt => <RuntimeCard key={rt.id} runtime={rt} />)}
    </CardContent>
  </Card>;
}

/** RuntimeCard shows reported health and model capability evidence.
 * RuntimeCard 展示上报的健康状态与模型能力证据。 */
function RuntimeCard({ runtime: rt }: { runtime: NodeRuntime }) {
  return <section aria-label={`AI 后端 ${rt.id}`} className="min-w-0 rounded-lg border p-4">
    <div className="flex flex-wrap items-center gap-2">
      <h2 className="break-all font-mono text-sm font-semibold">{rt.id}</h2>
      <Badge variant="outline">{rt.kind || "未知类型"}</Badge>
      <Badge variant={rt.state === "healthy" ? "secondary" : "outline"}>{STATE_LABELS[rt.state] ?? rt.state}</Badge>
      <Badge variant="outline">{rt.identityVerified ? "身份已验证" : "身份未验证"}</Badge>
      <span className="text-xs text-muted-foreground">{rt.models.length} 个模型</span>
    </div>
    <dl className="my-3 grid gap-2 text-xs sm:grid-cols-2">
      <Fact label="服务地址" value={rt.baseUrl || (rt.kind === "codex" || rt.kind === "claude" ? "本机 CLI" : "未上报")} />
      <Fact label="后端版本" value={rt.version || "未上报"} />
      <Fact label="健康检查" value={`${rt.latencyMillis === null ? "延迟未上报" : `${rt.latencyMillis} ms`} · ${formatDateTime(rt.checkedAt, "尚未检查")}`} />
      <Fact label="模型发现时间" value={formatDateTime(rt.discoveredAt, "尚未发现")} />
    </dl>
    {rt.degraded.length > 0 ? <p className="mb-2 text-xs text-muted-foreground">降级：{rt.degraded.join("；")}</p> : null}
    {rt.models.length === 0 ? <p className="text-sm text-muted-foreground">尚未发现模型。</p> : <ul className="grid gap-2">
      {rt.models.map(model => <li key={model.id} className="min-w-0 rounded-md bg-muted/40 p-3">
        <code className="break-all text-sm">{model.id}</code>
        <div className="mt-2 flex flex-wrap gap-1">
          {Object.entries(model.capabilities).length === 0 ? <span className="text-xs text-muted-foreground">模型能力未上报</span> :
            Object.entries(model.capabilities).map(([capability, support]) => <Badge key={capability} variant="outline" className="h-auto max-w-full whitespace-normal break-all text-xs">{capability} · {SUPPORT_LABELS[support] ?? support}</Badge>)}
        </div>
      </li>)}
    </ul>}
  </section>;
}

/** Fact renders one reported value with its label.
 * Fact 为一项上报值显示标签。 */
function Fact({ label, value }: { label: string; value: string }) {
  return <div className="min-w-0"><dt className="text-muted-foreground">{label}</dt><dd className="break-all">{value}</dd></div>;
}

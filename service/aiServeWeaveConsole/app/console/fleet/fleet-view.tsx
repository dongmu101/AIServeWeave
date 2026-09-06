"use client";

import * as React from "react";

import { FleetFreshness } from "@/components/console/fleet-freshness";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { parseFleetSnapshot, type FleetNode, type FleetSnapshot } from "@/lib/console/fleet";
import { formatDateTime, matchesQuery } from "@/lib/console/format";

/**
 * The fleet view.
 *
 * Everything on this page is a report from an Agent about itself, relayed by a
 * replica: labels, resources and runtime inventory are all self-declared, and
 * the page says so rather than presenting them as facts the platform verified.
 * The one thing the platform decided is liveness, which is why it is the field
 * the list sorts and colours by.
 *
 * There is no paging: this endpoint returns the whole fleet in one document,
 * because the fleet is bounded by hardware rather than by usage. The filter is
 * local for the same reason, and the count says how many are shown out of how
 * many were reported — which is what a local filter may honestly claim.
 *
 * 机群视图。
 *
 * 本页上的一切都是 Agent 关于它自己的报告、由某个副本转达：标签、资源与运行时清单全部
 * 是自述的，页面明说了这一点，而不是把它们当作平台核实过的事实来呈现。平台真正判定的
 * 只有活性，这正是列表按它排序、按它着色的原因。
 *
 * 这里没有分页：该端点用一份文档返回整个机群，因为机群的规模由硬件决定而不是由用量决定。
 * 筛选出于同样的理由是本地的，而计数写明「在报告到的多少个之中显示了多少个」——那正是
 * 一个本地筛选可以诚实宣称的东西。
 */
const NO_SNAPSHOT: FleetNode[] = [];

export function FleetView() {
  const fleet = useResource<FleetSnapshot>({
    method: "GET",
    surface: "operator",
    path: "/operator/v1/nodes",
    parse: parseFleetSnapshot,
  });
  const [query, setQuery] = React.useState("");

  const nodes = fleet.data?.nodes ?? NO_SNAPSHOT;
  const shown = React.useMemo(
    () =>
      nodes.filter((node) =>
        matchesQuery(
          [
            node.nodeId,
            node.agentVersion,
            ...Object.entries(node.labels).map(([name, value]) => `${name}=${value}`),
            ...node.runtimes.map((rt) => `${rt.id} ${rt.kind}`),
          ],
          query
        )
      ),
    [nodes, query]
  );

  return (
    <div className="grid gap-4">
      <div>
        <h1 className="font-heading text-lg font-semibold">节点</h1>
        <p className="text-sm text-muted-foreground">
          运维视图：整个机群的节点是所有租户共用的基础设施，不属于任何一个租户。
          标签、硬件与运行时清单都是 Agent 自述的内容；平台判定的只有在线状态。
        </p>
      </div>

      {fleet.error ? (
        <ErrorState message={fleet.error} onRetry={fleet.reload} />
      ) : fleet.loading || !fleet.data ? (
        <LoadingState label="正在采集机群清单" rows={5} />
      ) : (
        <>
          <FleetFreshness
            collectedAt={fleet.data.collectedAt}
            replicas={fleet.data.replicas}
            partial={fleet.data.partial}
          />

          <div className="flex flex-wrap items-center gap-3">
            <Input
              className="max-w-xs"
              placeholder="筛选节点 ID、标签或运行时"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              aria-label="筛选已采集的节点"
            />
            <span className="text-xs text-muted-foreground">
              显示 {shown.length} / 已采集 {nodes.length} 个节点；筛选只作用于本次采集到的
              内容。
            </span>
          </div>

          {shown.length === 0 ? (
            <EmptyState
              title={nodes.length === 0 ? "没有节点连接到任何已配置的副本" : "没有符合筛选条件的节点"}
              description={
                nodes.length === 0
                  ? "Agent 只主动出站建连；节点不在这里，意味着它没有连上任何一个被询问的 Gateway 副本。"
                  : undefined
              }
            />
          ) : (
            <div className="grid gap-3">
              {shown.map((node) => (
                <NodeCard key={node.nodeId} node={node} />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}

/** NodeCard renders one node with its runtimes.
 *
 * NodeCard 渲染一个节点及其运行时。 */
function NodeCard({ node }: { node: FleetNode }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          <code className="font-mono text-sm">{node.nodeId}</code>
          <Badge variant={node.live ? "secondary" : "outline"}>
            {node.live ? "在线" : "离线"}
          </Badge>
          {node.draining ? <Badge variant="outline">正在退出</Badge> : null}
          {node.agentVersion ? (
            <span className="text-xs font-normal text-muted-foreground">
              Agent {node.agentVersion}
            </span>
          ) : null}
        </CardTitle>
      </CardHeader>
      <CardContent className="grid gap-4">
        <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-4">
          <Fact label="最近心跳" value={formatDateTime(node.lastHeartbeat, "无心跳")} />
          <Fact label="在途请求" value={String(node.inflightRequests)} />
          <Fact
            label="空闲槽位"
            value={
              Object.keys(node.idleSlots).length === 0
                ? "无"
                : Object.entries(node.idleSlots)
                    .map(([name, value]) => `${name} ${value}`)
                    .join(" · ")
            }
          />
          <Fact label="报告它的副本" value={node.replicas.join("、") || "未知"} />
          <Fact
            label="硬件"
            value={
              node.resources
                ? `${node.resources.cpuCores} 核 · ${node.resources.gpuCount} GPU · ${node.resources.os}/${node.resources.arch}`
                : "未上报"
            }
          />
          <Fact label="采集时刻" value={formatDateTime(node.observedAt)} />
          <div className="grid gap-1 sm:col-span-2">
            <dt className="text-xs text-muted-foreground">标签（Agent 自述）</dt>
            <dd className="flex flex-wrap gap-1">
              {Object.keys(node.labels).length === 0 ? (
                <span className="text-sm text-muted-foreground">无</span>
              ) : (
                Object.entries(node.labels).map(([name, value]) => (
                  <Badge key={name} variant="outline" className="font-mono text-xs">
                    {name}={value}
                  </Badge>
                ))
              )}
            </dd>
          </div>
        </dl>

        <div className="grid gap-2">
          <p className="text-xs text-muted-foreground">运行时</p>
          {node.runtimes.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              该节点尚未上报任何运行时清单
              {node.declaredRuntimeIds.length > 0
                ? `；它在握手时声明过 ${node.declaredRuntimeIds.join("、")}`
                : ""}
              。
            </p>
          ) : (
            <div className="grid gap-2">
              {node.runtimes.map((rt) => (
                <div key={rt.id} className="rounded-lg border px-3 py-2 text-sm">
                  <div className="flex flex-wrap items-center gap-2">
                    <code className="font-mono text-xs">{rt.id}</code>
                    <Badge variant="outline">{rt.kind || "未知类型"}</Badge>
                    <Badge
                      variant="outline"
                      className={rt.state === "healthy" ? undefined : "text-destructive"}
                    >
                      {rt.state}
                    </Badge>
                    {rt.identityVerified ? null : (
                      <Badge variant="outline">身份未验证</Badge>
                    )}
                    <span className="text-xs text-muted-foreground">
                      {rt.latencyMillis === null
                        ? "未完成过健康检查"
                        : `${rt.latencyMillis} ms · ${formatDateTime(rt.checkedAt)}`}
                    </span>
                  </div>
                  {rt.baseUrl ? (
                    <p className="mt-1 font-mono text-xs text-muted-foreground">
                      {rt.baseUrl}
                    </p>
                  ) : null}
                  {rt.errorSummary ? (
                    <p className="mt-1 text-xs text-destructive">{rt.errorSummary}</p>
                  ) : null}
                  {rt.degraded.length > 0 ? (
                    <p className="mt-1 text-xs text-muted-foreground">
                      降级：{rt.degraded.join("；")}
                    </p>
                  ) : null}
                  <p className="mt-1 text-xs text-muted-foreground">
                    模型：
                    {rt.models.length === 0
                      ? "未发现"
                      : rt.models.map((model) => model.id).join("、")}
                  </p>
                </div>
              ))}
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="text-sm break-all">{value}</dd>
    </div>
  );
}

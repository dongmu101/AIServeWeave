"use client";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";


import * as React from "react";

import { FleetFreshness } from "@/components/console/fleet-freshness";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Button } from "@/components/ui/button";
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

/** FleetView renders independently refreshed Gateway observations.
 * FleetView 渲染可独立刷新的 Gateway 观测。 */
export function FleetView({ title }: { title?: string }) {
  const {t } = useI18n();
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
        <h1 className="font-heading text-lg font-semibold">{title ?? t("tenant.nodes")}</h1>
        <p className="text-sm text-muted-foreground">
          <Message id="tenant.operationsViewNodesAcrossTheFleetAreShared" /></p>
      </div>

      <Button variant="outline" className="w-fit" disabled={fleet.loading} onClick={fleet.reload}><Message id="tenant.refreshLiveFleet" /></Button>

      {fleet.error ? (
        <ErrorState message={fleet.error} onRetry={fleet.reload} />
      ) : fleet.loading || !fleet.data ? (
        <LoadingState label={t("tenant.collectingFleetInventory")} rows={5} />
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
              placeholder={t("tenant.filterByNodeIdLabelOrRuntime")}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              aria-label={t("tenant.filterCollectedNodes")}
            />
            <span className="text-xs text-muted-foreground"><Message id="tenant.nodesShownCollectedFiltersApplyOnlyToThe" values={{ count: shown.length, count2: nodes.length }} /></span>
          </div>

          {shown.length === 0 ? (
            <EmptyState
              title={nodes.length === 0 ? t("tenant.noNodesAreConnectedToAnyConfiguredReplica") : t("tenant.noNodesMatchTheFilters")}
              description={
                nodes.length === 0
                  ? t("tenant.theAgentOnlyInitiatesOutboundConnectionsIfA")
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
  const { locale, t } = useI18n();
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          <code className="min-w-0 break-all font-mono text-sm">{node.nodeId}</code>
          <Badge variant={node.live ? "secondary" : "outline"}>
            {node.live ? t("tenant.online") : t("tenant.offline")}
          </Badge>
          <Badge variant="outline">{node.maintenance ? t("tenant.inMaintenanceObserved") : t("tenant.notInMaintenanceObserved")}</Badge>
          {node.draining ? <Badge variant="outline"><Message id="tenant.exiting" /></Badge> : null}
          {node.agentVersion ? (
            <span className="text-xs font-normal text-muted-foreground">
              Agent {node.agentVersion}
            </span>
          ) : null}
        </CardTitle>
      </CardHeader>
      <CardContent className="grid grid-cols-1 gap-4">
        <p className="text-xs text-muted-foreground"><Message id="tenant.theMaintenanceFlagComesFromOneGatewayObservation" /></p>
        <dl className="grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-4">
          <Fact label={t("tenant.lastHeartbeat")} value={formatDateTime(node.lastHeartbeat, t("tenant.noHeartbeat"), locale)} />
          <Fact label={t("tenant.inFlightRequests")} value={String(node.inflightRequests)} />
          <Fact
            label={t("tenant.freeSlots")}
            value={
              Object.keys(node.idleSlots).length === 0
                ? t("tenant.none")
                : Object.entries(node.idleSlots)
                    .map(([name, value]) => `${name} ${value}`)
                    .join(" · ")
            }
          />
          <Fact label={t("tenant.reportingReplicas")} value={node.replicas.join("、") || t("tenant.unknown")} />
          <Fact
            label={t("tenant.hardware")}
            value={
              node.resources
                ? t("tenant.cpuCoresGpus", { cpuCores: node.resources.cpuCores, gpuCount: node.resources.gpuCount, os: node.resources.os, arch: node.resources.arch })
                : t("tenant.notReported")
            }
          />
          <Fact label={t("tenant.collectedAt")} value={formatDateTime(node.observedAt, undefined, locale)} />
          <div className="grid gap-1 sm:col-span-2">
            <dt className="text-xs text-muted-foreground"><Message id="tenant.labelsAgentReported" /></dt>
            <dd className="flex flex-wrap gap-1">
              {Object.keys(node.labels).length === 0 ? (
                <span className="text-sm text-muted-foreground"><Message id="tenant.none" /></span>
              ) : (
                Object.entries(node.labels).map(([name, value]) => (
                  <Badge key={name} variant="outline" className="h-auto max-w-full whitespace-normal break-all font-mono text-xs">
                    {name}={value}
                  </Badge>
                ))
              )}
            </dd>
          </div>
        </dl>

        <div className="grid gap-2">
          <p className="text-xs text-muted-foreground"><Message id="tenant.runtimes" /></p>
          {node.runtimes.length === 0 ? (
            <p className="text-sm text-muted-foreground"><Message id="tenant.thisNodeHasNotReportedAnyRuntimeInventory_eca6ac" values={{ value: node.declaredRuntimeIds.length > 0
                ? t("tenant.itDeclaredDuringTheHandshake", { value: node.declaredRuntimeIds.join("、") })
                : "" }} /></p>
          ) : (
            <div className="grid gap-2">
              {node.runtimes.map((rt) => (
                <div key={rt.id} className="rounded-lg border px-3 py-2 text-sm">
                  <div className="flex flex-wrap items-center gap-2">
                    <code className="font-mono text-xs">{rt.id}</code>
                    <Badge variant="outline">{rt.kind || t("tenant.unknownType")}</Badge>
                    <Badge
                      variant="outline"
                      className={rt.state === "healthy" ? undefined : "text-destructive"}
                    >
                      {rt.state}
                    </Badge>
                    {rt.identityVerified ? null : (
                      <Badge variant="outline"><Message id="tenant.identityNotVerified" /></Badge>
                    )}
                    <span className="text-xs text-muted-foreground">
                      {rt.latencyMillis === null
                        ? t("tenant.noHealthCheckCompletedYet")
                        : `${rt.latencyMillis} ms · ${formatDateTime(rt.checkedAt, undefined, locale)}`}
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
                    <p className="mt-1 text-xs text-muted-foreground"><Message id="tenant.degraded_0dfb3a" values={{ value: rt.degraded.join("；") }} /></p>
                  ) : null}
                  <p className="mt-1 text-xs text-muted-foreground"><Message id="tenant.models_298940" values={{ value: rt.models.length === 0
                      ? t("tenant.notFound")
                      : rt.models.map((model) => model.id).join("、") }} /></p>
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

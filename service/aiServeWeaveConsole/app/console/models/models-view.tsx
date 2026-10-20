"use client";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";



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

const STATE_LABELS = (t: Translator): Record<string, string> => ({
  healthy: t("tenant.healthy"), unhealthy: t("tenant.unhealthy"), registering: t("tenant.registering"), unknown: t("tenant.unknown"), closed: t("tenant.closed"),
});
const SUPPORT_LABELS = (t: Translator): Record<string, string> => ({
  supported: t("tenant.supported"), unsupported: t("tenant.notSupported"), unknown: t("tenant.unknown"),
});

/** ModelsView reads Gateway-local backends and connected Agent models.
 * ModelsView 读取 Gateway 本机后端与已连接 Agent 的模型。 */
export function ModelsView() {
  const { locale, t } = useI18n();
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
        <h1 className="font-heading text-lg font-semibold"><Message id="tenant.localAiModelsOnTheCurrentGateway" /></h1>
        <p className="text-sm text-muted-foreground"><Message id="tenant.aiServicesDiscoveredOnThisGatewayPlusBackends" /></p>
      </div>
      <Button variant="outline" disabled={loading} onClick={reload} aria-busy={loading}><Message id="tenant.refreshConnectionsModels" /></Button>
    </div>
    <p className="text-xs text-muted-foreground"><Message id="tenant.refreshesEvery15SecondsPausingInBackgroundTabs_6e72a0" values={{ link: <Link className="underline" href="/operator/routes"><Message id="tenant.modelRoutes" /></Link> }} /></p>
    {fleet.error ? <ErrorState message={fleet.error} onRetry={reload} /> : loading || !fleet.data ?
      <LoadingState label={t("tenant.loadingAiConnectionsAndModelsFromTheCurrent")} rows={4} /> : fleet.data.partial ?
      <ErrorState message={t("tenant.unableToLoadConnectionsAndModelsFromThe")} onRetry={reload} /> : <>
        <div className="rounded-lg border px-4 py-3 text-sm">
          <p><Message id="tenant.currentGateway_696df6" values={{ code: <code className="break-all">{fleet.data.replicas[0]?.replicaId ?? t("tenant.notReported")}</code> }} /></p>
          <p className="break-all text-xs text-muted-foreground"><Message id="tenant.collectedAt_c5ddb4" values={{ endpoint: fleet.data.replicas[0]?.endpoint, value: formatDateTime(fleet.data.collectedAt, undefined, locale) }} /></p>
        </div>
        <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Summary label={t("tenant.onlineNodes")} value={`${nodes.filter(node => node.live).length} / ${nodes.length}`} />
          <Summary label={t("tenant.aiBackends")} value={String(runtimes.length)} />
          <Summary label={t("tenant.healthyBackendsLocalAndOnlineNodes")} value={String([...localRuntimes, ...nodes.filter(node => node.live).flatMap(node => node.runtimes)].filter(rt => rt.state === "healthy").length)} />
          <Summary label={t("tenant.discoveredModelsUniqueIds")} value={String(models.size)} />
        </dl>
        <div className="flex flex-wrap items-center gap-3">
          <Input className="max-w-sm" aria-label={t("tenant.filterNodesBackendsOrModels")} placeholder={t("tenant.searchNodesBackendsAddressesOrModelIds")} value={query} onChange={event => setQuery(event.target.value)} />
          <label className="flex items-center gap-2 text-sm"><Message id="tenant.showOnlineNodesOnly_0b337a" values={{ content: <input type="checkbox" checked={onlineOnly} onChange={event => setOnlineOnly(event.target.checked)} /> }} /></label>
          <span className="text-xs text-muted-foreground"><Message id="tenant.nodesShownCollected" values={{ count: shown.length, count2: nodes.length }} /></span>
        </div>
        <Card>
          <CardHeader>
            <CardTitle><Message id="tenant.localAiServicesOnGateway" /></CardTitle>
            <p className="text-xs text-muted-foreground"><Message id="tenant.ollamaVllmServicesInTheEnvironmentRunningThis" /></p>
          </CardHeader>
          <CardContent className="grid gap-3">
            {shownLocal.length > 0 ? shownLocal.map(rt => <RuntimeCard key={rt.id} runtime={rt} />) :
              <p className="text-sm text-muted-foreground">{localRuntimes.length === 0 ? t("tenant.noLocalAiServicesDiscoveredOnGatewayYet") : t("tenant.noLocalAiServicesMatchYourSearch")}</p>}
          </CardContent>
        </Card>
        <h2 className="text-sm font-semibold"><Message id="tenant.aiServicesOnConnectedAgents" /></h2>
        {shown.length === 0 ? <EmptyState
          title={nodes.length === 0 ? t("tenant.noAiNodesAreConnectedToTheCurrent") : t("tenant.noNodesMatchTheFilters")}
          description={nodes.length === 0 ? t("tenant.checkThatTheAgentIsConnectedToThe") : t("tenant.tryClearingYourSearchOrTurningOffShow")}
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
  const { locale, t } = useI18n();
  return <Card>
    <CardHeader>
      <CardTitle className="flex flex-wrap items-center gap-2">
        <code className="break-all text-sm">{node.nodeId}</code>
        <Badge variant={node.live ? "secondary" : "outline"}>{node.live ? t("tenant.nodeOnline") : t("tenant.nodeOffline")}</Badge>
        {node.maintenance ? <Badge variant="outline"><Message id="tenant.inMaintenance" /></Badge> : null}
        {node.draining ? <Badge variant="outline"><Message id="tenant.exiting" /></Badge> : null}
      </CardTitle>
      <dl className="grid gap-2 text-xs text-muted-foreground sm:grid-cols-3">
        <Fact label={t("tenant.agentVersion")} value={node.agentVersion || t("tenant.notReported")} />
        <Fact label={t("tenant.lastHeartbeat")} value={formatDateTime(node.lastHeartbeat, t("tenant.notReported"), locale)} />
        <Fact label={t("tenant.gatewaysReportingThisNode")} value={node.replicas.join("、") || t("tenant.notReported")} />
      </dl>
      {!node.live ? <p className="text-xs text-muted-foreground"><Message id="tenant.thisNodeIsOfflineBackendAndModelInformation" /></p> : null}
    </CardHeader>
    <CardContent className="grid gap-3">
      {node.runtimes.length === 0 ? <p className="text-sm text-muted-foreground"><Message id="tenant.noAiBackendsReportedYet_bd0042" values={{ value: node.declaredRuntimeIds.length > 0 ? t("tenant.declared", { value: node.declaredRuntimeIds.join("、") }) : t("tenant.waitingForTheAgentToCompleteRuntimeDiscovery") }} /></p> :
        node.runtimes.map(rt => <RuntimeCard key={rt.id} runtime={rt} />)}
    </CardContent>
  </Card>;
}

/** RuntimeCard shows reported health and model capability evidence.
 * RuntimeCard 展示上报的健康状态与模型能力证据。 */
function RuntimeCard({ runtime: rt }: { runtime: NodeRuntime }) {
  const { locale, t } = useI18n();
  return <section aria-label={t("tenant.aiBackend", { id: rt.id })} className="min-w-0 rounded-lg border p-4">
    <div className="flex flex-wrap items-center gap-2">
      <h2 className="break-all font-mono text-sm font-semibold">{rt.id}</h2>
      <Badge variant="outline">{rt.kind || t("tenant.unknownType")}</Badge>
      <Badge variant={rt.state === "healthy" ? "secondary" : "outline"}>{STATE_LABELS(t)[rt.state] ?? rt.state}</Badge>
      <Badge variant="outline">{rt.identityVerified ? t("tenant.identityVerified") : t("tenant.identityNotVerified")}</Badge>
      <span className="text-xs text-muted-foreground"><Message id="tenant.models_4678e8" values={{ count: rt.models.length }} /></span>
    </div>
    <dl className="my-3 grid gap-2 text-xs sm:grid-cols-2">
      <Fact label={t("tenant.serviceAddress")} value={rt.baseUrl || (rt.kind === "codex" || rt.kind === "claude" ? t("tenant.localCli") : t("tenant.notReported"))} />
      <Fact label={t("tenant.backendVersion")} value={rt.version || t("tenant.notReported")} />
      <Fact label={t("tenant.healthCheck")} value={`${rt.latencyMillis === null ? t("tenant.latencyNotReported") : `${rt.latencyMillis} ms`} · ${formatDateTime(rt.checkedAt, t("tenant.notCheckedYet"), locale)}`} />
      <Fact label={t("tenant.modelsDiscoveredAt")} value={formatDateTime(rt.discoveredAt, t("tenant.notDiscoveredYet"), locale)} />
    </dl>
    {rt.degraded.length > 0 ? <p className="mb-2 text-xs text-muted-foreground"><Message id="tenant.degraded_0dfb3a" values={{ value: rt.degraded.join("；") }} /></p> : null}
    {rt.models.length === 0 ? <p className="text-sm text-muted-foreground"><Message id="tenant.noModelsDiscoveredYet" /></p> : <ul className="grid gap-2">
      {rt.models.map(model => <li key={model.id} className="min-w-0 rounded-md bg-muted/40 p-3">
        <code className="break-all text-sm">{model.id}</code>
        <div className="mt-2 flex flex-wrap gap-1">
          {Object.entries(model.capabilities).length === 0 ? <span className="text-xs text-muted-foreground"><Message id="tenant.modelCapabilitiesNotReported" /></span> :
            Object.entries(model.capabilities).map(([capability, support]) => <Badge key={capability} variant="outline" className="h-auto max-w-full whitespace-normal break-all text-xs">{capability} · {SUPPORT_LABELS(t)[support] ?? support}</Badge>)}
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

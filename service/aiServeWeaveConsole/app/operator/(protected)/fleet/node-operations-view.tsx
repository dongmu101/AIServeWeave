"use client";
import { MessageError } from "@/lib/i18n/problem";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";

import * as React from "react";
import Link from "next/link";
import { FleetView } from "@/app/console/fleet/fleet-view";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { nodePathSegment } from "@/lib/console/node-path";
import { parseNodeAccess, parseNodeAccessList, tenantNodeOwner } from "@/lib/console/node-access";
import { NODE_QUERY_PARAM } from "@/app/operator/(protected)/node-providers/node-providers-view";

import { formatDateTime, matchesQuery } from "@/lib/console/format";
import { nodeActionLabels, nodeActionRequest, parseNodeStates, type NodeAction } from "@/lib/console/node-ops";

const EFFECTS = (t: Translator): Record<NodeAction, string> => ({
  approve: t("operator.clearThePendingApprovalFlagTheNodeMust"),
  disable: t("operator.markThisNodeAsDisabledRejectingFutureRegistration"),
  enable: t("operator.clearTheDisabledFlagThisDoesNotClear"),
  maintenance: t("operator.askGatewaysToStopSendingNewRequestsTo"),
  resume: t("operator.clearTheMaintenanceFlagTheNodeMustAlso"),
  delete: t("operator.deleteNodeEffect"),
});

/** NodeOperationsView keeps desired state and observed liveness independently readable.
 * NodeOperationsView 让期望状态与观测活性可独立读取。 */
export function NodeOperationsView() {
  const { locale, t } = useI18n();
  const registry = useResource({ method: "GET", surface: "operator", path: "/operator/v1/nodes/states", parse: parseNodeStates });
  const run = useConsoleRequest();
  const sharing = useResource({ method: "GET", surface: "operator", path: "/operator/v1/node-access", parse: parseNodeAccessList });
  const [shareOnApprove, setShareOnApprove] = React.useState(false);
  const [approving, setApproving] = React.useState(false);
  const [query, setQuery] = React.useState("");
  const [selected, setSelected] = React.useState<{ nodeId: string; action: NodeAction } | null>(null);
  const [sharingTarget, setSharingTarget] = React.useState<{ nodeId: string; mode: "private" | "shared"; replacesDedicated: boolean } | null>(null);
  const selectionOpen = selected !== null || sharingTarget !== null;
  const [fleetRevision, setFleetRevision] = React.useState(0);
  const [notice, setNotice] = React.useState<React.ReactNode>(null);
  const shown = registry.data?.filter((node) => matchesQuery([node.nodeId], query)) ?? [];

  return <div className="grid gap-8">
    <section className="grid gap-4" aria-labelledby="registry-title">
      <div>
        <h1 id="registry-title" className="font-heading text-xl font-semibold tracking-tight"><Message id="operator.nodeManagement" /></h1>
        <p className="text-sm text-muted-foreground"><Message id="operator.desiredStateRecordedByTheRegistryIncludingNodes" /></p>
      </div>
      <div className="flex flex-wrap gap-3">
        <Input className="max-w-xs" value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("operator.filterRegistryNodeIds")} aria-label={t("operator.filterRegistryNodeIds")} />
        <Button variant="outline" disabled={registry.loading || sharing.loading} onClick={() => { registry.reload(); sharing.reload(); }}><Message id="operator.refreshRegistryState" /></Button>
      </div>
      {notice ? <p role="status" className="text-sm">{notice}</p> : null}
      {registry.error ? <ErrorState message={registry.error} onRetry={registry.reload} /> : registry.loading ? <LoadingState label={t("operator.loadingRegistryState")} /> : shown.length === 0 ? <EmptyState title={registry.data?.length ? t("tenant.noNodesMatchTheFilters") : t("operator.noNodeRecordsInTheRegistryYet")} /> : <div className="grid gap-3">
        {shown.map((node) => <div key={node.nodeId} className="grid gap-3 rounded-xl border bg-card p-4">
          <div className="flex flex-wrap items-center gap-2">
            <code className="min-w-0 break-all text-sm">{node.nodeId}</code>
            <Badge variant="outline">{node.pendingApproval ? t("operator.pendingApproval") : t("operator.approved")}</Badge>
            <Badge variant={node.disabled ? "destructive" : "outline"}>{node.disabled ? t("operator.disabled") : t("operator.notDisabled")}</Badge>
            <Badge variant="outline">{node.maintenance ? t("operator.inMaintenanceDesired") : t("operator.maintenanceNotRequested")}</Badge>
            {tenantNodeOwner(node.nodeId) === null && sharing.data ? <Badge variant="outline">{t(sharing.data.find(entry => entry.nodeId === node.nodeId)?.mode === "shared" ? "operator.sharedNode" : sharing.data.find(entry => entry.nodeId === node.nodeId)?.mode === "dedicated" ? "operator.dedicatedNode" : "operator.privateNode")}</Badge> : null}
          </div>
          {!nodePathSegment(node.nodeId) ? <p className="text-sm text-muted-foreground"><Message id="operator.thisNodeIdContainsPathSeparatorsOrControl" /></p> : null}
          <p className="text-xs text-muted-foreground"><Message id="operator.firstRegisteredLastRegistered" values={{ value: formatDateTime(node.firstSeenAt, undefined, locale), value2: formatDateTime(node.lastSeenAt, undefined, locale) }} /></p>
          <div className="flex flex-wrap gap-2">
            {(node.pendingApproval ? ["approve" as const] : []).map((action) => <Button key={action} variant="outline" disabled={selectionOpen || !nodePathSegment(node.nodeId)} onClick={() => { setShareOnApprove(sharing.data?.find(entry => entry.nodeId === node.nodeId)?.mode === "shared"); setSelected({ nodeId: node.nodeId, action }); }}>{nodeActionLabels(locale)[action]}</Button>)}
            {([node.disabled ? "enable" : "disable", node.maintenance ? "resume" : "maintenance"] as NodeAction[]).map((action) => <Button key={action} variant="outline" disabled={selectionOpen || !nodePathSegment(node.nodeId)} onClick={() => { setShareOnApprove(sharing.data?.find(entry => entry.nodeId === node.nodeId)?.mode === "shared"); setSelected({ nodeId: node.nodeId, action }); }}>{nodeActionLabels(locale)[action]}</Button>)}
            {tenantNodeOwner(node.nodeId) === null ? <Button variant="outline"
              disabled={selectionOpen || sharing.loading || sharing.error !== null || !nodePathSegment(node.nodeId)}
              onClick={() => {
                const mode = sharing.data?.find(entry => entry.nodeId === node.nodeId)?.mode ?? "private";
                setSharingTarget({ nodeId: node.nodeId, mode: mode === "shared" ? "private" : "shared", replacesDedicated: mode === "dedicated" });
              }}><Message id={sharing.data?.find(entry => entry.nodeId === node.nodeId)?.mode === "shared" ? "operator.disableNodeSharing" : "operator.enableNodeSharing"} /></Button> : null}
            {nodePathSegment(node.nodeId) && tenantNodeOwner(node.nodeId) === null ? <Button variant="outline" render={<Link href={`/operator/node-providers?${new URLSearchParams({ [NODE_QUERY_PARAM]: node.nodeId })}`} />}><Message id="operator.nodeProvidersManage" /></Button> : null}
            <Button variant="destructive" disabled={selectionOpen || !nodePathSegment(node.nodeId)} onClick={() => setSelected({ nodeId: node.nodeId, action: "delete" })}>{nodeActionLabels(locale).delete}</Button>
          </div>
        </div>)}
      </div>}
    </section>
    {sharing.error ? <ErrorState message={sharing.error} onRetry={sharing.reload} /> : null}
    <Link className="text-sm underline" href="/operator/node-access"><Message id="operator.sharingSettings" /></Link>
    <FleetView key={fleetRevision} title={t("operator.liveFleetGatewayObservations")} />
    <ConfirmDialog open={sharingTarget !== null} onOpenChange={open => { if (!open) setSharingTarget(null); }}
      title={t(sharingTarget?.mode === "private" ? "operator.disableNodeSharing" : "operator.enableNodeSharing")}
      confirmLabel={t("operator.confirmAction")} destructive={sharingTarget?.mode === "private"}
      description={sharingTarget ? <>
        <span className="break-all font-mono">{sharingTarget.nodeId}</span>：
        <Message id={sharingTarget.mode === "shared" ? "operator.makeNodeSharedConfirm" : "operator.stopNodeSharingConfirm"} />
        {sharingTarget.replacesDedicated ? <> <Message id="operator.replaceDedicatedForSharing" /></> : null}
      </> : ""}
      onConfirm={async () => {
        if (!sharingTarget) return;
        setNotice(null);
        try {
          await run({ method: "PUT", surface: "operator", path: `/operator/v1/node-access/${encodeURIComponent(sharingTarget.nodeId)}`,
            body: { mode: sharingTarget.mode, tenants: [] }, parse: parseNodeAccess });
          setNotice(<Message id="operator.nodeSharingSaved" />);
        } catch (failure) {
          throw new MessageError("operator.theWriteResultForIsUnconfirmedRefreshStatus", {}, failure);
        } finally {
          sharing.reload();
        }
      }} />
    <ConfirmDialog open={selected !== null} onOpenChange={(open) => { if (!open) setSelected(null); }} title={selected ? nodeActionLabels(locale)[selected.action] : t("operator.nodeActions")} confirmLabel={t("operator.confirmAction")} destructive={selected?.action === "disable" || selected?.action === "delete"} description={selected ? <><span className="break-all font-mono">{selected.nodeId}</span>：{EFFECTS(t)[selected.action]} <Message id="operator.statePropagationTakesTime" />{selected.action === "approve" && tenantNodeOwner(selected.nodeId) === null ? <span className="mt-3 grid gap-2"><label className="flex items-center gap-2"><input type="checkbox" checked={shareOnApprove} disabled={approving} onChange={event => setShareOnApprove(event.target.checked)} /><Message id="operator.shareWithTenants" /></label><span><Message id="operator.nodeSharingHint" /></span></span> : null}</> : ""} onConfirm={async () => {
      if (!selected) return;
      setNotice(null);
      setApproving(true);
      try {
        await run(nodeActionRequest(selected.nodeId, selected.action, shareOnApprove));
        setFleetRevision((revision) => revision + 1);
        setNotice(<Message id={"operator.theRegistryAcceptedTheChangeApplicationOnAll"} values={{ value: nodeActionLabels(locale)[selected.action] }} />);
      } catch (failure) {
        throw new MessageError("operator.theWriteResultForIsUnconfirmedRefreshStatus", {  }, failure);
      } finally {
        registry.reload();
        sharing.reload();
        setApproving(false);
      }
    }} />
  </div>;
}

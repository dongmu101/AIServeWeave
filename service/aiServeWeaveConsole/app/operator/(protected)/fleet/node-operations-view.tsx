"use client";

import * as React from "react";
import Link from "next/link";
import { ChevronRightIcon, CircleCheckIcon, InfoIcon, PlusIcon, RefreshCwIcon, SearchIcon, Share2Icon, TriangleAlertIcon } from "lucide-react";
import { cn } from "cn";

import { STATUS_TONES, FleetNodeCard } from "./fleet-node-card";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { FleetFreshness } from "@/components/console/fleet-freshness";
import { EmptyState, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { useResource, type Resource } from "@/components/console/use-resource";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Button, buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { parseFleetSnapshot } from "@/lib/console/fleet";
import {
  FLEET_STATUS_FILTERS,
  countByStatus,
  matchesFleetQuery,
  matchesStatus,
  mergeFleetBoard,
  type FleetStatusFilter,
} from "@/lib/console/fleet-board";
import { parseNodeAccess, parseNodeAccessList, tenantNodeOwner } from "@/lib/console/node-access";
import { parseNodeBilling, parseNodeBillingList } from "@/lib/console/node-billing";
import { nodeActionLabels, nodeActionRequest, parseNodeStates, type NodeAction } from "@/lib/console/node-ops";
import { MessageError } from "@/lib/i18n/problem";
import type { Translator } from "@/lib/i18n/translate";

const EFFECTS = (t: Translator): Record<NodeAction, string> => ({
  approve: t("operator.clearThePendingApprovalFlagTheNodeMust"),
  disable: t("operator.markThisNodeAsDisabledRejectingFutureRegistration"),
  enable: t("operator.clearTheDisabledFlagThisDoesNotClear"),
  maintenance: t("operator.askGatewaysToStopSendingNewRequestsTo"),
  resume: t("operator.clearTheMaintenanceFlagTheNodeMustAlso"),
  delete: t("operator.deleteNodeEffect"),
});

const TILE_DOTS: Record<FleetStatusFilter, string | null> = {
  all: null,
  pending: STATUS_TONES.attention.dot,
  online: STATUS_TONES.online.dot,
  offline: STATUS_TONES.offline.dot,
  maintenance: STATUS_TONES.maintenance.dot,
  disabled: STATUS_TONES.critical.dot,
};

type FleetScope = "all" | "cloud" | "dedicated";

/**
 * useSettled keeps a resource's last good value while it reloads, so a refresh
 * does not blank the board, and drops it as soon as a read fails, so a stale
 * answer is never shown beside an error.
 *
 * useSettled 在资源重新读取期间保留上一次成功的值，刷新不会让面板清空；读取一旦失败
 * 立即丢弃，过期答案不会与错误并列显示。
 */
function useSettled<T>(resource: Resource<T>): T | null {
  const [kept, setKept] = React.useState<T | null>(null);
  const next = resource.data ?? (resource.loading ? kept : null);
  if (next !== kept) setKept(next);
  return next;
}

/** NodeOperationsView shows each machine once, with Registry intent and Gateway
 * observation side by side in its own fields; the three sources load, fail and
 * refresh independently.
 * NodeOperationsView 每台机器只出现一次，Registry 期望与 Gateway 观测各占自己的字段
 * 并列显示；三份来源各自加载、失败与刷新。 */
export function NodeOperationsView() {
  const { locale, t } = useI18n();
  const registry = useResource({ method: "GET", surface: "operator", path: "/operator/v1/nodes/states", parse: parseNodeStates });
  const sharing = useResource({ method: "GET", surface: "operator", path: "/operator/v1/node-access", parse: parseNodeAccessList });
  const fleet = useResource({ method: "GET", surface: "operator", path: "/operator/v1/nodes", parse: parseFleetSnapshot });
  const billing = useResource({ method: "GET", surface: "operator", path: "/operator/v1/node-billing", parse: parseNodeBillingList });
  const run = useConsoleRequest();
  const desired = useSettled(registry);
  const access = useSettled(sharing);
  const snapshot = useSettled(fleet);
  const freeNodes = useSettled(billing);

  const [query, setQuery] = React.useState("");
  const [scope, setScope] = React.useState<FleetScope>("all");
  const [status, setStatus] = React.useState<FleetStatusFilter>("all");
  const [shareOnApprove, setShareOnApprove] = React.useState(false);
  const [approving, setApproving] = React.useState(false);
  const [selected, setSelected] = React.useState<{ nodeId: string; action: NodeAction } | null>(null);
  const [sharingTarget, setSharingTarget] = React.useState<{ nodeId: string; mode: "private" | "shared"; replacesDedicated: boolean } | null>(null);
  const [billingTarget, setBillingTarget] = React.useState<{ nodeId: string; free: boolean } | null>(null);
  const [notice, setNotice] = React.useState<React.ReactNode>(null);
  const selectionOpen = selected !== null || sharingTarget !== null || billingTarget !== null;

  const board = React.useMemo(() => mergeFleetBoard(desired, snapshot?.nodes ?? null, access), [desired, snapshot, access]);
  const scopedNodes = React.useMemo(() => {
    if (scope === "all") return board;
    return board.filter((node) => {
      const owner = tenantNodeOwner(node.nodeId);
      if (owner !== null) return scope === "dedicated" && owner !== "";
      return node.access === (scope === "cloud" ? "shared" : "dedicated");
    });
  }, [board, scope]);
  const counts = React.useMemo(() => countByStatus(scopedNodes), [scopedNodes]);
  const shown = scopedNodes.filter((node) => matchesStatus(node, status) && matchesFleetQuery(node, query));
  const scopeReady = scope === "all" || access !== null;
  const refreshing = registry.loading || sharing.loading || fleet.loading || billing.loading;
  const settled = (desired !== null || registry.error !== null) && (snapshot !== null || fleet.error !== null);
  const labels = nodeActionLabels(locale);

  function refresh() {
    registry.reload();
    sharing.reload();
    fleet.reload();
    billing.reload();
  }

  function requestAction(nodeId: string, action: NodeAction) {
    setShareOnApprove(access?.find((entry) => entry.nodeId === nodeId)?.mode === "shared");
    setSelected({ nodeId, action });
  }

  function requestSharing(nodeId: string) {
    const mode = access?.find((entry) => entry.nodeId === nodeId)?.mode ?? "private";
    setSharingTarget({ nodeId, mode: mode === "shared" ? "private" : "shared", replacesDedicated: mode === "dedicated" });
  }

  return (
    <Tabs value={scope} onValueChange={(value) => {
      if (value === "all" || value === "cloud" || value === "dedicated") setScope(value);
    }} className="gap-6">
      <header className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="grid min-w-0 flex-1 basis-80 gap-1">
          <h1 className="font-heading text-xl font-semibold tracking-tight"><Message id="operator.nodeManagement" /></h1>
          <p className="max-w-3xl text-sm text-muted-foreground"><Message id="operator.fleetIntro" /></p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Link className={buttonVariants({ variant: "outline", size: "sm" })} href="/operator/agent-enrollment">
            <PlusIcon /><Message id="operator.agentEnrollment" />
          </Link>
          <Link className={buttonVariants({ variant: "outline", size: "sm" })} href="/operator/node-access">
            <Share2Icon /><Message id="operator.sharingSettings" />
          </Link>
          <Button size="sm" disabled={refreshing} onClick={refresh}>
            <RefreshCwIcon className={cn(refreshing && settled ? "animate-spin" : undefined)} /><Message id="common.refresh" />
          </Button>
        </div>
      </header>

      <TabsList aria-label={t("operator.nodeManagement")} className="h-auto! w-full flex-wrap justify-start sm:w-fit">
        <TabsTrigger value="all" className="flex-none px-3"><Message id="tenant.modelScopeAll" /></TabsTrigger>
        <TabsTrigger value="cloud" className="flex-none px-3"><Message id="tenant.modelScopeCloud" /></TabsTrigger>
        <TabsTrigger value="dedicated" className="flex-none px-3"><Message id="tenant.modelScopeDedicated" /></TabsTrigger>
      </TabsList>

      {!settled ? (
        <TabsContent value={scope}><LoadingState label={t("operator.loadingRegistryState")} rows={4} /></TabsContent>
      ) : (
        <TabsContent value={scope} className="grid gap-6">
          <div role="group" aria-label={t("tenant.filterByStatus")} className="grid grid-cols-3 gap-2 sm:grid-cols-6">
            {FLEET_STATUS_FILTERS.map((filter) => {
              const unknown = !scopeReady || (snapshot === null && (filter === "online" || filter === "offline"));
              const active = status === filter;
              const dot = TILE_DOTS[filter];
              return (
                <button
                  key={filter}
                  type="button"
                  aria-pressed={active}
                  onClick={() => setStatus(active ? "all" : filter)}
                  className={cn(
                    "grid min-w-0 gap-1 rounded-xl border bg-card px-3 py-2.5 text-left shadow-xs transition-colors outline-none hover:bg-muted/60 focus-visible:ring-3 focus-visible:ring-ring/50",
                    active ? "border-foreground/50 ring-1 ring-foreground/15" : undefined,
                  )}
                >
                  <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
                    {dot ? <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full", dot)} /> : null}
                    <span className="min-w-0 truncate">{tileLabel(filter, t)}</span>
                  </span>
                  <span className="font-heading text-xl leading-none font-semibold tabular-nums">{unknown ? "—" : counts[filter]}</span>
                </button>
              );
            })}
          </div>

          <div className="grid gap-3">
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
              <div className="relative w-full sm:w-80">
                <SearchIcon aria-hidden className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  className="pl-8"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder={t("tenant.filterByNodeIdLabelOrRuntime")}
                  aria-label={t("tenant.filterByNodeIdLabelOrRuntime")}
                />
              </div>
              {scopeReady ? <span className="text-xs text-muted-foreground tabular-nums">
                <Message id="operator.fleetShownCount" values={{ count: shown.length, total: scopedNodes.length }} />
              </span> : null}
            </div>
            {snapshot ? <FleetFreshness collectedAt={snapshot.collectedAt} replicas={snapshot.replicas} partial={snapshot.partial} note={false} /> : null}
            <details className="group text-xs text-muted-foreground">
              <summary className="flex w-fit cursor-pointer list-none items-center gap-1.5 rounded-md font-medium outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
                <InfoIcon aria-hidden className="size-3.5" />
                <Message id="operator.fleetAboutData" />
                <ChevronRightIcon aria-hidden className="size-3.5 transition-transform group-open:rotate-90" />
              </summary>
              <ul className="mt-2 grid max-w-4xl list-disc gap-1.5 rounded-lg bg-muted/60 py-3 pr-4 pl-8 leading-relaxed">
                <li><Message id="operator.desiredStateRecordedByTheRegistryIncludingNodes" /></li>
                <li><Message id="tenant.operationsViewNodesAcrossTheFleetAreShared" /></li>
                <li><Message id="tenant.theMaintenanceFlagComesFromOneGatewayObservation" /></li>
                <li><Message id="common.eachReplicaKnowsOnlyTheNodesConnectedTo" /></li>
                <li><Message id="tenant.cliQuotaNote" /></li>
              </ul>
            </details>
          </div>

          {notice ? (
            <p role="status" className="flex items-start gap-2 rounded-lg border border-emerald-500/30 bg-emerald-500/5 px-3 py-2 text-sm">
              <CircleCheckIcon aria-hidden className="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
              <span>{notice}</span>
            </p>
          ) : null}
          {registry.error ? <SourceAlert title={t("operator.registryState")} message={registry.error} onRetry={registry.reload} /> : null}
          {fleet.error ? <SourceAlert title={t("operator.liveFleetGatewayObservations")} message={fleet.error} onRetry={fleet.reload} /> : null}
          {sharing.error ? <SourceAlert title={t("operator.sharingSettings")} message={sharing.error} onRetry={sharing.reload} /> : null}
          {billing.error ? <SourceAlert title={t("operator.nodeBilling")} message={billing.error} onRetry={billing.reload} /> : null}

          {shown.length > 0 ? (
            <ul className="grid gap-3">
              {shown.map((node) => (
                <li key={node.nodeId}>
                  <FleetNodeCard
                    node={node}
                    busy={selectionOpen}
                    sharingReady={access !== null && !sharing.loading}
                    free={freeNodes === null || billing.loading ? null : freeNodes.has(node.nodeId)}
                    onAction={(action) => requestAction(node.nodeId, action)}
                    onSharing={() => requestSharing(node.nodeId)}
                    onBilling={() => setBillingTarget({ nodeId: node.nodeId, free: !(freeNodes?.has(node.nodeId) ?? false) })}
                  />
                </li>
              ))}
            </ul>
          ) : !scopeReady ? null : board.length > 0 ? (
            <EmptyState title={t("tenant.noNodesMatchTheFilters")} />
          ) : desired !== null && snapshot !== null ? (
            <EmptyState
              title={t("operator.noNodeRecordsInTheRegistryYet")}
              description={t("tenant.theAgentOnlyInitiatesOutboundConnectionsIfA")}
              action={<Link className={buttonVariants({ size: "sm", variant: "outline" })} href="/operator/agent-enrollment"><PlusIcon /><Message id="operator.agentEnrollment" /></Link>}
            />
          ) : null}
        </TabsContent>
      )}

      <ConfirmDialog
        open={sharingTarget !== null}
        onOpenChange={(open) => { if (!open) setSharingTarget(null); }}
        title={t(sharingTarget?.mode === "private" ? "operator.disableNodeSharing" : "operator.enableNodeSharing")}
        confirmLabel={t("operator.confirmAction")}
        destructive={sharingTarget?.mode === "private"}
        description={sharingTarget ? <>
          <span className="font-mono break-all">{sharingTarget.nodeId}</span>：
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
        }}
      />
      <ConfirmDialog
        open={billingTarget !== null}
        onOpenChange={(open) => { if (!open) setBillingTarget(null); }}
        title={t(billingTarget?.free ? "operator.makeNodeFree" : "operator.makeNodeCharged")}
        confirmLabel={t("operator.confirmAction")}
        description={billingTarget ? <>
          <span className="font-mono break-all">{billingTarget.nodeId}</span>：
          <Message id={billingTarget.free ? "operator.makeNodeFreeConfirm" : "operator.makeNodeChargedConfirm"} />
        </> : ""}
        onConfirm={async () => {
          if (!billingTarget) return;
          setNotice(null);
          try {
            await run({ method: "PUT", surface: "operator", path: `/operator/v1/node-billing/${encodeURIComponent(billingTarget.nodeId)}`,
              body: { free: billingTarget.free }, parse: parseNodeBilling });
            setNotice(<Message id="operator.nodeBillingSaved" />);
          } catch (failure) {
            throw new MessageError("operator.theWriteResultForIsUnconfirmedRefreshStatus", {}, failure);
          } finally {
            billing.reload();
          }
        }}
      />
      <ConfirmDialog
        open={selected !== null}
        onOpenChange={(open) => { if (!open) setSelected(null); }}
        title={selected ? labels[selected.action] : t("operator.nodeActions")}
        confirmLabel={t("operator.confirmAction")}
        destructive={selected?.action === "disable" || selected?.action === "delete"}
        description={selected ? <>
          <span className="font-mono break-all">{selected.nodeId}</span>：{EFFECTS(t)[selected.action]} <Message id="operator.statePropagationTakesTime" />
          {selected.action === "approve" && tenantNodeOwner(selected.nodeId) === null ? (
            <span className="mt-3 grid gap-2">
              <label className="flex items-center gap-2">
                <input type="checkbox" checked={shareOnApprove} disabled={approving} onChange={(event) => setShareOnApprove(event.target.checked)} />
                <Message id="operator.shareWithTenants" />
              </label>
              <span><Message id="operator.nodeSharingHint" /></span>
            </span>
          ) : null}
        </> : ""}
        onConfirm={async () => {
          if (!selected) return;
          setNotice(null);
          setApproving(true);
          try {
            await run(nodeActionRequest(selected.nodeId, selected.action, shareOnApprove));
            fleet.reload();
            setNotice(<Message id="operator.theRegistryAcceptedTheChangeApplicationOnAll" values={{ value: labels[selected.action] }} />);
          } catch (failure) {
            throw new MessageError("operator.theWriteResultForIsUnconfirmedRefreshStatus", {}, failure);
          } finally {
            registry.reload();
            sharing.reload();
            setApproving(false);
          }
        }}
      />
    </Tabs>
  );
}

function tileLabel(filter: FleetStatusFilter, t: Translator): string {
  switch (filter) {
    case "all": return t("common.all");
    case "pending": return t("operator.pendingApproval");
    case "online": return t("tenant.online");
    case "offline": return t("tenant.offline");
    case "maintenance": return t("tenant.inMaintenance");
    case "disabled": return t("operator.disabled");
  }
}

/** SourceAlert names which of the three reads failed, so a missing source is
 * never mistaken for an empty one.
 * SourceAlert 写明三份读取中哪一份失败，缺失的来源不会被误认为空。 */
function SourceAlert({ title, message, onRetry }: { title: string; message: string; onRetry: () => void }) {
  return (
    <div role="alert" className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm">
      <TriangleAlertIcon aria-hidden className="size-4 shrink-0 text-destructive" />
      <span className="min-w-0 flex-1 text-destructive">
        <span className="font-medium">{title}</span>
        <span className="mx-1.5 text-destructive/50">·</span>
        {message}
      </span>
      <Button size="sm" variant="outline" onClick={onRetry}><Message id="common.retry" /></Button>
    </div>
  );
}

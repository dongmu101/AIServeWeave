"use client";

import Link from "next/link";
import { EllipsisIcon } from "lucide-react";
import { cn } from "cn";

import { NODE_QUERY_PARAM } from "@/app/operator/(protected)/node-providers/node-providers-view";
import { CliQuota } from "@/components/console/cli-quota";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { FleetNode, NodeLoad, NodeRuntime } from "@/lib/console/fleet";
import type { FleetBoardNode, Liveness } from "@/lib/console/fleet-board";
import { formatDateTime } from "@/lib/console/format";
import { tenantNodeOwner } from "@/lib/console/node-access";
import { nodeActionLabels, type NodeAction } from "@/lib/console/node-ops";
import { nodePathSegment } from "@/lib/console/node-path";
import { formatBytes, formatNumber } from "@/lib/i18n/format";
import type { Locale } from "@/lib/i18n/locale";
import type { Translator } from "@/lib/i18n/translate";

/**
 * Status tones shared by the badges and the status tiles. Only exceptional
 * states get a badge: a node that is approved, enabled and not in maintenance
 * shows none, so the badges that do appear are the ones worth reading.
 *
 * 徽章与状态卡片共用的状态色。只有异常状态才有徽章：已审批、未禁用、未维护的节点不显示
 * 任何徽章，因此出现的徽章都是值得一读的。
 */
export const STATUS_TONES = {
  online: { dot: "bg-emerald-500", badge: "border-emerald-500/30 bg-emerald-500/10 text-emerald-800 dark:text-emerald-300" },
  attention: { dot: "bg-amber-500", badge: "border-amber-500/30 bg-amber-500/10 text-amber-800 dark:text-amber-300" },
  maintenance: { dot: "bg-sky-500", badge: "border-sky-500/30 bg-sky-500/10 text-sky-800 dark:text-sky-300" },
  critical: { dot: "bg-destructive", badge: "" },
  offline: { dot: "bg-muted-foreground/45", badge: "text-muted-foreground" },
} as const;

/** FleetNodeCard renders one machine: Registry intent and its actions in the
 * header, the Gateway observation in the body, registration times in the footer.
 * FleetNodeCard 渲染一台机器：页头是 Registry 期望与操作，主体是 Gateway 观测，页脚是登记时间。 */
export function FleetNodeCard({
  node,
  busy,
  sharingReady,
  onAction,
  onSharing,
}: {
  node: FleetBoardNode;
  /** busy disables every write while a confirmation is open. / 确认框打开时 busy 禁用全部写操作。 */
  busy: boolean;
  /** sharingReady is false while the sharing policy is loading or failed. / 共享策略加载中或读取失败时为 false。 */
  sharingReady: boolean;
  onAction: (action: NodeAction) => void;
  onSharing: () => void;
}) {
  const { locale, t } = useI18n();
  const { desired, observed } = node;
  const addressable = nodePathSegment(node.nodeId);
  const owner = tenantNodeOwner(node.nodeId);
  const labels = nodeActionLabels(locale);
  return (
    <Card className="gap-0 py-0">
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3 p-4">
        <div className="grid min-w-0 flex-1 basis-72 gap-1.5">
          <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5">
            <h2 className="mr-1 min-w-0 font-mono text-sm font-semibold break-all">{node.nodeId}</h2>
            <LivenessBadge liveness={node.liveness} />
            <StatusBadges node={node} owner={owner} />
          </div>
          {observed ? <ObservedMeta node={observed} /> : null}
        </div>
        {desired ? (
          <div className="flex flex-wrap items-center gap-1.5">
            {desired.pendingApproval ? (
              <Button size="sm" disabled={busy || !addressable} onClick={() => onAction("approve")}>{labels.approve}</Button>
            ) : null}
            {desired.disabled ? (
              <Button size="sm" variant="outline" disabled={busy || !addressable} onClick={() => onAction("enable")}>{labels.enable}</Button>
            ) : null}
            <Button size="sm" variant="outline" disabled={busy || !addressable} onClick={() => onAction(desired.maintenance ? "resume" : "maintenance")}>
              {labels[desired.maintenance ? "resume" : "maintenance"]}
            </Button>
            {owner === null ? (
              <Button size="sm" variant="outline" disabled={busy || !sharingReady || !addressable} onClick={onSharing}>
                <Message id={node.access === "shared" ? "operator.disableNodeSharing" : "operator.enableNodeSharing"} />
              </Button>
            ) : null}
            {owner === null && addressable ? (
              <Link className={buttonVariants({ size: "sm", variant: "outline" })} href={`/operator/node-providers?${new URLSearchParams({ [NODE_QUERY_PARAM]: node.nodeId })}`}>
                <Message id="operator.nodeProvidersManage" />
              </Link>
            ) : null}
            <DropdownMenu>
              <DropdownMenuTrigger
                aria-label={t("operator.nodeActions")}
                disabled={busy || !addressable}
                render={<Button size="icon-sm" variant="ghost" />}
              >
                <EllipsisIcon />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-40" aria-label={t("operator.nodeActions")}>
                {desired.disabled ? null : (
                  <>
                    <DropdownMenuItem onClick={() => onAction("disable")}>{labels.disable}</DropdownMenuItem>
                    <DropdownMenuSeparator />
                  </>
                )}
                <DropdownMenuItem variant="destructive" onClick={() => onAction("delete")}>{labels.delete}</DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        ) : null}
      </div>

      {desired && !addressable ? (
        <p className="border-t px-4 py-2.5 text-sm text-muted-foreground"><Message id="operator.thisNodeIdContainsPathSeparatorsOrControl" /></p>
      ) : null}

      {observed ? (
        <ObservedBody node={observed} />
      ) : node.liveness === "unobserved" ? (
        <p className="border-t px-4 py-3 text-sm text-muted-foreground"><Message id="operator.notObservedHint" /></p>
      ) : null}

      {desired || observed ? (
        <div className="flex flex-wrap gap-x-4 gap-y-1 border-t bg-muted/40 px-4 py-2 text-xs text-muted-foreground">
          {desired ? (
            <span>
              <Message
                id="operator.firstRegisteredLastRegistered"
                values={{ value: formatDateTime(desired.firstSeenAt, undefined, locale), value2: formatDateTime(desired.lastSeenAt, undefined, locale) }}
              />
            </span>
          ) : null}
          {observed ? <span>{t("tenant.collectedAt")} {formatDateTime(observed.observedAt, undefined, locale)}</span> : null}
        </div>
      ) : null}
    </Card>
  );
}

/** LivenessBadge states the Gateway's verdict in words beside its colour.
 * LivenessBadge 在颜色旁用文字说明 Gateway 的判定。 */
function LivenessBadge({ liveness }: { liveness: Liveness }) {
  const { t } = useI18n();
  if (liveness === "unknown") return null;
  const look = {
    online: { label: t("tenant.online"), dot: STATUS_TONES.online.dot, badge: STATUS_TONES.online.badge },
    offline: { label: t("tenant.offline"), dot: STATUS_TONES.offline.dot, badge: STATUS_TONES.offline.badge },
    unobserved: { label: t("operator.notObservedByGateway"), dot: "border border-muted-foreground/60", badge: "border-dashed text-muted-foreground" },
  }[liveness];
  return (
    <Badge variant="outline" className={look.badge}>
      <span aria-hidden className={cn("size-1.5 rounded-full", look.dot)} />
      {look.label}
    </Badge>
  );
}

/** StatusBadges lists only the states that need reading. Maintenance names its
 * source when intent and observation disagree.
 * StatusBadges 只列出需要阅读的状态；期望与观测不一致时，维护徽章写明来源。 */
function StatusBadges({ node, owner }: { node: FleetBoardNode; owner: string | null }) {
  const { t } = useI18n();
  const { desired, observed } = node;
  const wanted = desired?.maintenance === true;
  const seen = observed?.maintenance === true;
  const maintenance = wanted && seen ? t("tenant.inMaintenance") : wanted ? t("operator.inMaintenanceDesired") : seen ? t("tenant.inMaintenanceObserved") : null;
  const pressure = observed?.live && observed.load?.pressure ? observed.load : null;
  return (
    <>
      {desired?.pendingApproval ? <Badge variant="outline" className={STATUS_TONES.attention.badge}>{t("operator.pendingApproval")}</Badge> : null}
      {desired?.disabled ? <Badge variant="destructive">{t("operator.disabled")}</Badge> : null}
      {maintenance ? (
        <Badge variant="outline" className={STATUS_TONES.maintenance.badge} title={seen ? t("tenant.theMaintenanceFlagComesFromOneGatewayObservation") : undefined}>
          {maintenance}
        </Badge>
      ) : null}
      {observed?.draining ? <Badge variant="secondary"><Message id="tenant.exiting" /></Badge> : null}
      {pressure ? (
        <Badge variant="destructive">
          {[t("tenant.underPressure"), ...pressure.pressureReasons.map((reason) => (reason === "cpu" ? t("tenant.pressureReasonCpu") : t("tenant.pressureReasonMemory")))].join(" · ")}
        </Badge>
      ) : null}
      {owner !== null ? (
        <Badge variant="outline" className="text-muted-foreground" title={owner}>{t("operator.tenantOwnedNode")}</Badge>
      ) : node.access !== null ? (
        <Badge variant="outline" className={node.access === "private" ? "text-muted-foreground" : undefined}>
          {t(node.access === "shared" ? "operator.sharedNode" : node.access === "dedicated" ? "operator.dedicatedNode" : "operator.privateNode")}
        </Badge>
      ) : null}
      {node.inRegistry === false ? <Badge variant="outline" className="border-dashed text-muted-foreground">{t("operator.notInRegistry")}</Badge> : null}
    </>
  );
}

/** ObservedMeta is the one-line identity an Agent reported: version, last
 * heartbeat and the replicas that hold its connection.
 * ObservedMeta 是 Agent 上报的一行身份信息：版本、最近心跳与持有其连接的副本。 */
function ObservedMeta({ node }: { node: FleetNode }) {
  const { locale, t } = useI18n();
  const parts = [
    node.agentVersion ? `Agent ${node.agentVersion}` : null,
    `${t("tenant.lastHeartbeat")} ${formatDateTime(node.lastHeartbeat, t("tenant.noHeartbeat"), locale)}`,
    `${t("tenant.reportingReplicas")} ${node.replicas.join("、") || t("tenant.unknown")}`,
  ].filter((part): part is string => part !== null);
  return (
    <p className="flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
      {parts.map((part) => <span key={part} className="break-all">{part}</span>)}
    </p>
  );
}

/** ObservedBody lays resources beside the runtimes that use them.
 * ObservedBody 把资源与使用这些资源的运行时并排呈现。 */
function ObservedBody({ node }: { node: FleetNode }) {
  const { locale, t } = useI18n();
  const labels = Object.entries(node.labels);
  return (
    <div className="grid gap-5 border-t p-4 lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)] lg:gap-8">
      <div className="grid content-start gap-4">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
          <Fact label={t("tenant.inFlightRequests")} value={formatNumber(node.inflightRequests, locale)} />
          <Fact
            label={t("tenant.freeSlots")}
            value={Object.keys(node.idleSlots).length === 0 ? t("tenant.none") : Object.entries(node.idleSlots).map(([name, value]) => `${name} ${value}`).join(" · ")}
          />
          <Fact
            className="col-span-2"
            label={t("tenant.hardware")}
            value={node.resources ? t("tenant.cpuCoresGpus", { cpuCores: node.resources.cpuCores, gpuCount: node.resources.gpuCount, os: node.resources.os, arch: node.resources.arch }) : t("tenant.notReported")}
          />
        </dl>
        {node.load !== null || node.live ? <LoadMeters load={node.load} live={node.live} /> : null}
        {labels.length > 0 ? (
          <div className="grid gap-1.5">
            <p className="text-xs text-muted-foreground"><Message id="tenant.labelsAgentReported" /></p>
            <div className="flex flex-wrap gap-1">
              {labels.map(([name, value]) => (
                <Badge key={name} variant="outline" className="h-auto max-w-full font-mono font-normal break-all whitespace-normal">{name}={value}</Badge>
              ))}
            </div>
          </div>
        ) : null}
      </div>
      <div className="grid content-start gap-4">
        <Runtimes node={node} />
        {node.cliUsage.length > 0 ? (
          <div className="grid gap-1.5">
            <p className="text-xs text-muted-foreground" title={t("tenant.cliQuotaNote")}><Message id="tenant.cliQuota" /></p>
            <div className="grid gap-2 sm:grid-cols-2">
              {node.cliUsage.map((account) => (
                <div key={`${account.kind}/${account.runtimeId}`} className="grid gap-1.5 rounded-lg border px-3 py-2">
                  <code className="font-mono text-xs font-medium">{account.runtimeId || account.kind}</code>
                  <CliQuota usage={account} now={Date.parse(node.observedAt)} />
                </div>
              ))}
            </div>
          </div>
        ) : null}
      </div>
    </div>
  );
}

/** LoadMeters draws CPU and memory use against their limits. The figure is
 * always printed beside the bar, so colour is never the only signal; values the
 * Agent did not measure are omitted rather than drawn as zero.
 * LoadMeters 画出 CPU 与内存相对上限的用量。数值始终印在条旁，颜色从不是唯一信号；
 * Agent 未测到的值被省略，而不是画成零。 */
function LoadMeters({ load, live }: { load: NodeLoad | null; live: boolean }) {
  const { locale, t } = useI18n();
  const flagged = (reason: "cpu" | "memory") => live && load !== null && load.pressure && load.pressureReasons.includes(reason);
  const memoryKnown = load !== null && load.memoryAvailableBytes !== null && load.memoryTotalBytes !== null && load.memoryTotalBytes > 0;
  const notes = load === null ? [] : [
    memoryKnown ? t("tenant.memoryAvailable", { available: formatBytes(load.memoryAvailableBytes!, locale), total: formatBytes(load.memoryTotalBytes!, locale) }) : null,
    load.loadAverage1m !== null ? t("tenant.loadAverage1m", { value: formatNumber(load.loadAverage1m, locale, { maximumFractionDigits: 2 }) }) : null,
    load.admissionWaiting > 0 ? t("tenant.admissionWaiting", { value: load.admissionWaiting }) : null,
  ].filter((note): note is string => note !== null);
  const hasMeter = load !== null && (load.cpuUtilization !== null || memoryKnown);
  return (
    <div className="grid gap-1.5">
      <p className="text-xs text-muted-foreground"><Message id="tenant.liveLoad" /></p>
      {hasMeter ? (
        <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-2 text-xs">
          {load.cpuUtilization !== null ? (
            <Meter
              label={t("tenant.pressureReasonCpu")}
              ratio={load.cpuUtilization}
              figure={`${load.cpuEstimated ? "≈" : ""}${wholePercent(load.cpuUtilization, locale)}`}
              flagged={flagged("cpu")}
            />
          ) : null}
          {memoryKnown ? (
            <Meter
              label={t("tenant.pressureReasonMemory")}
              ratio={1 - load.memoryAvailableBytes! / load.memoryTotalBytes!}
              figure={wholePercent(1 - load.memoryAvailableBytes! / load.memoryTotalBytes!, locale)}
              flagged={flagged("memory")}
            />
          ) : null}
        </div>
      ) : notes.length === 0 ? (
        <p className="text-sm text-muted-foreground"><Message id="tenant.loadNotReported" /></p>
      ) : null}
      {notes.length > 0 ? <p className="text-xs text-muted-foreground">{notes.join(" · ")}</p> : null}
    </div>
  );
}

/** Meter is one row of a meter list: name, bar and figure. The track is a
 * lighter step of the fill's own hue so the state reads across the whole bar.
 * Meter 是一行用量条：名称、条与数值。底轨是填充色同一色相的浅阶，状态在整条上都可读。 */
function Meter({ label, ratio, figure, flagged }: { label: string; ratio: number; figure: string; flagged: boolean }) {
  const level = flagged || ratio >= 0.9 ? "critical" : ratio >= 0.75 ? "warning" : "normal";
  const [track, fill] = {
    normal: ["bg-foreground/10", "bg-foreground/55"],
    warning: ["bg-amber-500/20", "bg-amber-500"],
    critical: ["bg-destructive/15", "bg-destructive"],
  }[level];
  const percent = Math.min(100, Math.max(0, ratio * 100));
  return (
    <>
      <span className="text-muted-foreground">{label}</span>
      <div
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(percent)}
        aria-valuetext={figure}
        className={cn("h-1.5 overflow-hidden rounded-full", track)}
      >
        <div className={cn("h-full rounded-full", fill)} style={{ width: `${percent}%` }} />
      </div>
      <span className={cn("text-right tabular-nums", level === "critical" ? "font-medium text-destructive" : undefined)}>{figure}</span>
    </>
  );
}

/** Runtimes lists what the Agent reported running, one compact row each.
 * Runtimes 列出 Agent 上报正在运行的服务，每个一行紧凑记录。 */
function Runtimes({ node }: { node: FleetNode }) {
  const { locale, t } = useI18n();
  return (
    <div className="grid gap-1.5">
      <p className="text-xs text-muted-foreground">
        <Message id="tenant.runtimes" />
        {node.runtimes.length > 0 ? <span className="tabular-nums"> · {node.runtimes.length}</span> : null}
      </p>
      {node.runtimes.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          <Message
            id="tenant.thisNodeHasNotReportedAnyRuntimeInventory_eca6ac"
            values={{ value: node.declaredRuntimeIds.length > 0 ? t("tenant.itDeclaredDuringTheHandshake", { value: node.declaredRuntimeIds.join("、") }) : "" }}
          />
        </p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {node.runtimes.map((runtime) => <RuntimeRow key={runtime.id} runtime={runtime} t={t} locale={locale} />)}
        </ul>
      )}
    </div>
  );
}

function RuntimeRow({ runtime, t, locale }: { runtime: NodeRuntime; t: Translator; locale: Locale }) {
  const tone = runtime.state === "healthy" ? "healthy" : runtime.state === "degraded" ? "degraded" : "failing";
  return (
    <li className="grid gap-1 px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full", { healthy: STATUS_TONES.online.dot, degraded: STATUS_TONES.attention.dot, failing: STATUS_TONES.critical.dot }[tone])} />
        <code className="min-w-0 font-mono text-xs font-medium break-all">{runtime.id}</code>
        <Badge variant="outline" className="font-normal">{runtime.kind || t("tenant.unknownType")}</Badge>
        <span className={cn("text-xs", { healthy: "text-muted-foreground", degraded: "font-medium text-amber-700 dark:text-amber-400", failing: "font-medium text-destructive" }[tone])}>
          {runtime.state}
        </span>
        {runtime.identityVerified ? null : <Badge variant="outline" className="font-normal text-muted-foreground"><Message id="tenant.identityNotVerified" /></Badge>}
        <span className="ml-auto text-xs text-muted-foreground tabular-nums">
          {runtime.latencyMillis === null ? t("tenant.noHealthCheckCompletedYet") : `${runtime.latencyMillis} ms · ${formatDateTime(runtime.checkedAt, undefined, locale)}`}
        </span>
      </div>
      {runtime.baseUrl ? <p className="font-mono text-xs break-all text-muted-foreground">{runtime.baseUrl}</p> : null}
      {runtime.errorSummary ? <p className="text-xs text-destructive">{runtime.errorSummary}</p> : null}
      {runtime.degraded.length > 0 ? (
        <p className="text-xs text-muted-foreground"><Message id="tenant.degraded_0dfb3a" values={{ value: runtime.degraded.join("；") }} /></p>
      ) : null}
      <p className="text-xs text-muted-foreground">
        <Message id="tenant.models_298940" values={{ value: runtime.models.length === 0 ? t("tenant.notFound") : runtime.models.map((model) => model.id).join("、") }} />
      </p>
    </li>
  );
}

/** wholePercent prints a meter figure without decimals; the bar carries the detail.
 * wholePercent 以整数百分比印出用量数值，细节由条形承担。 */
function wholePercent(ratio: number, locale: Locale): string {
  return formatNumber(ratio, locale, { style: "percent", maximumFractionDigits: 0 });
}

function Fact({ label, value, className }: { label: string; value: string; className?: string }) {
  return (
    <div className={cn("grid min-w-0 gap-0.5", className)}>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="text-sm font-medium break-all">{value}</dd>
    </div>
  );
}

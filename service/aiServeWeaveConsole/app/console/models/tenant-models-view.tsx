"use client";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";



import * as React from "react";
import { createColumnHelper, tableFeatures, useTable } from "@tanstack/react-table";
import { DataTable } from "@/components/console/data-table";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { parseTenantModels, type CliUsage, type CliUsageWindow, type TenantModelCatalog } from "@/lib/console/fleet";
import { formatDateTime, matchesQuery } from "@/lib/console/format";

type Service = TenantModelCatalog["services"][number];
const PAGE_SIZE = 25;
const STATES = (t: Translator): Record<string, string> => ({ healthy: t("tenant.healthy"), unhealthy: t("tenant.unhealthy"), registering: t("tenant.registering"), unknown: t("tenant.unknown"), closed: t("tenant.closed") });
const SUPPORT = (t: Translator): Record<string, string> => ({ supported: t("tenant.supported"), unsupported: t("tenant.notSupported"), unknown: t("tenant.unknown") });
const features = tableFeatures({});
type AgentGroup = { agentId: string; services: Service[] };
const helper = createColumnHelper<typeof features, Service>();
const agentHelper = createColumnHelper<typeof features, AgentGroup>();

/** TenantModelsView groups Agent reports into parent rows with service subtables.
 * TenantModelsView 按 Agent 主行和服务子表分组展示上报清单。 */
export function TenantModelsView() {
  const { locale, t } = useI18n();
  const catalog = useResource({ method: "GET", path: "/admin/v1/models", parse: parseTenantModels });
  const [query, setQuery] = React.useState("");
  const [onlineOnly, setOnlineOnly] = React.useState(false);
  const [page, setPage] = React.useState(0);
  const [selected, setSelected] = React.useState<Service | null>(null);
  const { loading, reload } = catalog;
  const refresh = React.useCallback(() => { setSelected(null); reload(); }, [reload]);
  React.useEffect(() => {
    if (loading || selected) return;
    const timer = window.setInterval(() => { if (!document.hidden) refresh(); }, 15_000);
    return () => window.clearInterval(timer);
  }, [loading, refresh, selected]);
  const services = catalog.data?.services ?? [];
  const shown = services.filter(service => (!onlineOnly || service.nodeOnline) && matchesQuery([
    service.agentId, service.serviceId, service.kind, service.version, ...service.models.map(model => model.id),
  ], query));
  const grouped = new Map<string, AgentGroup>();
  for (const service of shown) {
    let agent = grouped.get(service.agentId);
    if (!agent) {
      agent = { agentId: service.agentId, services: [] };
      grouped.set(service.agentId, agent);
    }
    agent.services.push(service);
  }
  const agents = [...grouped.values()];
  const currentPage = Math.min(page, Math.max(0, Math.ceil(agents.length / PAGE_SIZE) - 1));
  const rows = agents.slice(currentPage * PAGE_SIZE, (currentPage + 1) * PAGE_SIZE);
  const columns = React.useMemo(() => agentHelper.columns([
    agentHelper.accessor("agentId", {
      header: "Agent",
      cell: info => <div className="grid max-w-48 gap-2 whitespace-normal">
        <code className="break-all text-xs">{info.getValue()}</code>
        <Badge className="w-fit" variant={info.row.original.services[0].nodeOnline ? "secondary" : "outline"}>{info.row.original.services[0].nodeOnline ? t("tenant.agentOnline") : t("tenant.agentOffline")}</Badge>
        <span className="text-xs text-muted-foreground"><Message id="tenant.aiServices_3f8e07" values={{ count: info.row.original.services.length }} /></span>
      </div>,
    }),
    agentHelper.display({ id: "services", header: t("tenant.aiServices_a7836c"), cell: info => <AgentServices agent={info.row.original} collectedAt={catalog.data?.collectedAt ?? ""} onSelect={setSelected} /> }),
  ]), [catalog.data?.collectedAt, t]);
  const table = useTable({ features, columns, data: rows, getRowId: agent => agent.agentId });

  return <div className="grid gap-4">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div><h1 className="font-heading text-lg font-semibold"><Message id="tenant.agentAiServicesModels" /></h1><p className="text-sm text-muted-foreground"><Message id="tenant.oneRecordPerAgentWithAiServicesAnd" /></p></div>
      <Button variant="outline" disabled={loading} onClick={refresh}><Message id="tenant.refreshServicesModels" /></Button>
    </div>
    <p className="text-xs text-muted-foreground"><Message id="tenant.refreshesEvery15SecondsPausingInBackgroundTabs_e8c2ce" /></p>
    {catalog.error ? <ErrorState message={catalog.error} onRetry={refresh} /> : loading || !catalog.data ?
      <LoadingState label={t("tenant.loadingAgentAiServicesAndModels")} rows={3} /> : catalog.data.partial ?
      <ErrorState message={t("tenant.unableToLoadAgentAiServicesAndModels")} onRetry={refresh} /> : <>
      <p className="text-xs text-muted-foreground"><Message id="tenant.collectedAtAgentsReportingServicesAiServicesModels" values={{ value: formatDateTime(catalog.data.collectedAt, undefined, locale), size: new Set(services.map(service => service.agentId)).size, count: services.length, size2: new Set(services.flatMap(service => service.models.map(model => model.id))).size }} /></p>
      <div className="flex flex-wrap items-center gap-3">
        <Input className="max-w-sm" aria-label={t("tenant.searchAiServicesOrModels")} placeholder={t("tenant.searchAgentsServiceIdsTypesOrModels")} value={query} onChange={event => { setQuery(event.target.value); setPage(0); }} />
        <label className="flex items-center gap-2 text-sm"><Message id="tenant.showOnlineAgentsOnly_56e957" values={{ content: <input type="checkbox" checked={onlineOnly} onChange={event => { setOnlineOnly(event.target.checked); setPage(0); }} /> }} /></label>
      </div>
      {shown.length === 0 ? <EmptyState title={services.length === 0 ? t("tenant.noAgentsHaveReportedAiServicesYet") : t("tenant.noServicesOrModelsMatchYourSearch")} description={services.length === 0 ? t("tenant.checkThatTheAgentIsConnectedToA") : undefined} /> : <>
        <DataTable table={table} caption={t("tenant.agentAiServiceList")} />
        <ListPager page={currentPage} count={agents.length} onChange={setPage} label={t("tenant.agentPagination")} />
      </>}
    </>}
    <Dialog open={selected !== null} onOpenChange={open => { if (!open) setSelected(null); }}>
      {selected ? <ServiceModels key={JSON.stringify([selected.agentId, selected.serviceId])} service={selected} /> : null}
    </Dialog>
  </div>;
}

/** AgentServices renders a bounded subtable owned by one Agent row.
 * AgentServices 在一个 Agent 主行内展示有界的服务子表。 */
function AgentServices({ agent, collectedAt, onSelect }: { agent: AgentGroup; collectedAt: string; onSelect: (service: Service) => void }) {
  const { t } = useI18n();
  const [page, setPage] = React.useState(0);
  const currentPage = Math.min(page, Math.max(0, Math.ceil(agent.services.length / PAGE_SIZE) - 1));
  // The catalogue's own collection time, so "already reset" is judged against the data and not the render clock.
  // 使用目录自己的采集时间，让「已重置」依据数据而不是渲染时的时钟来判断。
  const now = Date.parse(collectedAt);
  const columns = React.useMemo(() => helper.columns([
    helper.accessor("serviceId", { header: t("tenant.serviceIdType"), cell: info => <div className="max-w-48 whitespace-normal break-all"><code className="text-xs">{info.getValue()}</code><div className="text-xs text-muted-foreground">{info.row.original.kind}</div></div> }),
    helper.accessor("state", { header: t("tenant.healthStatus"), cell: info => <Badge variant="outline">{STATES(t)[info.getValue()] ?? info.getValue()}</Badge> }),
    helper.accessor("version", { header: t("tenant.version"), cell: info => <span className="inline-block max-w-32 whitespace-normal break-all">{info.getValue() || t("tenant.notReported")}</span> }),
    helper.accessor("models", { header: t("tenant.models_98fd0c"), cell: info => <span className="text-xs"><Message id="tenant.models_4678e8" values={{ count: info.getValue().length }} /></span> }),
    helper.accessor("cliUsage", { header: t("tenant.cliQuota"), cell: info => <CliQuota usage={info.getValue()} now={now} /> }),
    helper.display({ id: "details", header: t("tenant.details"), cell: info => <Button variant="outline" size="sm" onClick={() => onSelect(info.row.original)}><Message id="tenant.viewModels" /></Button> }),
  ]), [onSelect, now, t]);
  const table = useTable({ features, columns, data: agent.services.slice(currentPage * PAGE_SIZE, (currentPage + 1) * PAGE_SIZE), getRowId: service => service.serviceId });
  return <div className="grid min-w-0 gap-2">
    <DataTable table={table} caption={t("tenant.aiServicesOn", { agentId: agent.agentId })} />
    {agent.services.some(service => service.cliUsage) ? <p className="text-xs text-muted-foreground">{t("tenant.cliQuotaNote")}</p> : null}
    {agent.services.length > PAGE_SIZE ? <ListPager page={currentPage} count={agent.services.length} onChange={setPage} label={t("tenant.servicePaginationFor", { agentId: agent.agentId })} /> : null}
  </div>;
}

const WINDOW_NAMES: Record<string, Parameters<Translator>[0]> = {
  five_hour: "tenant.cliQuotaFiveHour", seven_day: "tenant.cliQuotaSevenDay",
  seven_day_sonnet: "tenant.cliQuotaSevenDaySonnet", seven_day_opus: "tenant.cliQuotaSevenDayOpus",
  primary: "tenant.cliQuotaPrimary", secondary: "tenant.cliQuotaSecondary",
};
const STATUS_TEXT: Record<string, Parameters<Translator>[0]> = {
  pending: "tenant.cliQuotaPending", unauthorized: "tenant.cliQuotaUnauthorized",
  unavailable: "tenant.cliQuotaUnavailable", error: "tenant.cliQuotaError",
};

/** CliQuota shows the subscription quota of the CLI a service runs: used and
 * remaining percentages per window, when each resets, and when it was checked.
 * A service without a reported quota shows a dash, never a zero.
 * CliQuota 展示服务所运行 CLI 的订阅额度：每个窗口的已用与剩余比例、重置时间与查询时间。
 * 没有上报额度的服务显示破折号，绝不显示零。 */
function CliQuota({ usage, now }: { usage: CliUsage | null; now: number }) {
  const { locale, t } = useI18n();
  if (!usage) return <span className="text-xs text-muted-foreground">—</span>;
  if (usage.status !== "ready" || usage.windows.length === 0) {
    const key = STATUS_TEXT[usage.status] ?? "tenant.cliQuotaError";
    return <span className="text-xs text-muted-foreground">{t(key)}</span>;
  }
  const percent = (value: number) => `${value.toLocaleString(locale, { maximumFractionDigits: 1 })}%`;
  const render = (window: CliUsageWindow) => {
    const expired = window.resetsAt !== null && Date.parse(window.resetsAt) <= now;
    const key = WINDOW_NAMES[window.name];
    return <li key={`${window.bucket}/${window.name}`} className="grid gap-0.5">
      <span className="font-medium">{key ? t(key) : window.name}{window.bucket ? ` · ${window.bucket}` : ""}</span>
      {window.durationMinutes > 0 ? <span className="text-muted-foreground">{t("tenant.cliQuotaMinutesWindow", { minutes: window.durationMinutes.toLocaleString(locale) })}</span> : null}
      {expired ? <span className="text-muted-foreground">{t("tenant.cliQuotaWaitingNextCheck")}</span> :
        <span>{t("tenant.cliQuotaUsedRemaining", { used: percent(window.usedPercent), remaining: percent(Math.max(0, 100 - window.usedPercent)) })}</span>}
      {window.resetsAt ? <span className="text-muted-foreground">{t("tenant.cliQuotaResets", { time: formatDateTime(window.resetsAt, undefined, locale) })}</span> : null}
    </li>;
  };
  return <div className="grid min-w-40 gap-2 whitespace-normal text-xs">
    <ul className="grid gap-2">{usage.windows.map(render)}</ul>
    {usage.checkedAt ? <span className="text-muted-foreground">{t("tenant.cliQuotaChecked", { time: formatDateTime(usage.checkedAt, undefined, locale) })}</span> : null}
  </div>;
}

/** ServiceModels paginates one service's models in a separate detail window.
 * ServiceModels 在独立详情窗口中分页展示一个服务的模型。 */
function ServiceModels({ service }: { service: Service }) {
  const {t } = useI18n();
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(0);
  const models = service.models.filter(model => matchesQuery([model.id], query));
  const currentPage = Math.min(page, Math.max(0, Math.ceil(models.length / PAGE_SIZE) - 1));
  return <DialogContent className="sm:max-w-2xl">
    <DialogHeader><DialogTitle><Message id="tenant.modelDetails" /></DialogTitle><DialogDescription className="break-all">Agent {service.agentId} · {service.serviceId} · {service.kind}</DialogDescription></DialogHeader>
    {!service.nodeOnline ? <p className="text-xs text-muted-foreground"><Message id="tenant.thisAgentIsOfflineTheFollowingServiceAnd" /></p> : null}
    <Input aria-label={t("tenant.searchModelsInThisService")} placeholder={t("tenant.searchModelIds")} value={query} onChange={event => { setQuery(event.target.value); setPage(0); }} />
    {models.length === 0 ? <p className="text-sm text-muted-foreground">{service.models.length === 0 ? t("tenant.noModelsDiscoveredYet") : t("tenant.noModelsMatchYourSearch")}</p> : <ul className="grid gap-2">{models.slice(currentPage * PAGE_SIZE, (currentPage + 1) * PAGE_SIZE).map(model => <li key={model.id} className="min-w-0 rounded-md bg-muted/40 p-3">
      <code className="break-all text-sm">{model.id}</code>
      <div className="mt-2 flex flex-wrap gap-1">{Object.entries(model.capabilities).map(([capability, support]) => <Badge key={capability} variant="outline" className="h-auto max-w-full whitespace-normal break-all text-xs">{capability} · {SUPPORT(t)[support] ?? support}</Badge>)}</div>
    </li>)}</ul>}
    <ListPager page={currentPage} count={models.length} onChange={setPage} label={t("tenant.modelPagination")} />
  </DialogContent>;
}

/** ListPager bounds rendered rows within the collected snapshot.
 * ListPager 限制本次采集快照中同时渲染的行数。 */
function ListPager({ page, count, onChange, label }: { page: number; count: number; onChange: (page: number) => void; label: string }) {
  const pages = Math.max(1, Math.ceil(count / PAGE_SIZE));
  return <nav aria-label={label} className="flex flex-wrap items-center justify-between gap-3">
    <p className="text-xs text-muted-foreground"><Message id="tenant.pageOfMatchingRecordsPerPage" values={{ value: page + 1, pages: pages, count: count, PAGE_SIZE: PAGE_SIZE }} /></p>
    <div className="flex gap-2"><Button variant="outline" size="sm" disabled={page === 0} onClick={() => onChange(page - 1)}><Message id="tenant.previousPage" /></Button><Button variant="outline" size="sm" disabled={page + 1 >= pages} onClick={() => onChange(page + 1)}><Message id="tenant.nextPage" /></Button></div>
  </nav>;
}

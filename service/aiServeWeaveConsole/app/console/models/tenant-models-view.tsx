"use client";

import * as React from "react";
import { createColumnHelper, tableFeatures, useTable } from "@tanstack/react-table";
import { DataTable } from "@/components/console/data-table";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { parseTenantModels, type TenantModelCatalog } from "@/lib/console/fleet";
import { formatDateTime, matchesQuery } from "@/lib/console/format";

type Service = TenantModelCatalog["services"][number];
const PAGE_SIZE = 25;
const STATES: Record<string, string> = { healthy: "健康", unhealthy: "异常", registering: "注册中", unknown: "未知", closed: "已关闭" };
const SUPPORT: Record<string, string> = { supported: "支持", unsupported: "不支持", unknown: "未知" };
const features = tableFeatures({});
const helper = createColumnHelper<typeof features, Service>();

/** TenantModelsView lists Agent reports with bounded service and model pages.
 * TenantModelsView 以有界的服务和模型分页展示 Agent 上报清单。 */
export function TenantModelsView() {
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
  const currentPage = Math.min(page, Math.max(0, Math.ceil(shown.length / PAGE_SIZE) - 1));
  const rows = shown.slice(currentPage * PAGE_SIZE, (currentPage + 1) * PAGE_SIZE);
  const columns = React.useMemo(() => helper.columns([
    helper.accessor("agentId", { header: "Agent", cell: info => <code className="inline-block max-w-48 whitespace-normal break-all text-xs">{info.getValue()}</code> }),
    helper.accessor("nodeOnline", { header: "连接状态", cell: info => <Badge variant={info.getValue() ? "secondary" : "outline"}>{info.getValue() ? "Agent 在线" : "Agent 离线"}</Badge> }),
    helper.accessor("serviceId", { header: "AI 服务", cell: info => <div className="max-w-48 whitespace-normal break-all"><code className="text-xs">{info.getValue()}</code><div className="text-xs text-muted-foreground">{info.row.original.kind}</div></div> }),
    helper.accessor("state", { header: "健康状态", cell: info => <Badge variant="outline">{STATES[info.getValue()] ?? info.getValue()}</Badge> }),
    helper.accessor("version", { header: "版本", cell: info => <span className="inline-block max-w-32 whitespace-normal break-all">{info.getValue() || "未上报"}</span> }),
    helper.accessor("models", { header: "模型", cell: info => <div className="max-w-64 whitespace-normal break-all text-xs"><span>{info.getValue().length} 个模型</span><p className="mt-1 text-muted-foreground">{info.getValue().slice(0, 3).map(model => model.id).join("、") || "尚未发现模型"}{info.getValue().length > 3 ? " …" : ""}</p></div> }),
    helper.display({ id: "details", header: "详情", cell: info => <Button variant="outline" size="sm" onClick={() => setSelected(info.row.original)}>查看模型</Button> }),
  ]), []);
  const table = useTable({ features, columns, data: rows, getRowId: service => JSON.stringify([service.agentId, service.serviceId]) });

  return <div className="grid gap-4">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div><h1 className="font-heading text-lg font-semibold">Agent AI 服务与模型</h1><p className="text-sm text-muted-foreground">按 Agent 和 AI 服务分行展示连接状态、后端版本及上报模型。</p></div>
      <Button variant="outline" disabled={loading} onClick={refresh}>刷新服务与模型</Button>
    </div>
    <p className="text-xs text-muted-foreground">每 15 秒自动刷新，后台标签页或查看模型详情时暂停刷新。离线 Agent 的数据为最近一次上报；API 调用名称与可调度范围取决于模型路由。</p>
    {catalog.error ? <ErrorState message={catalog.error} onRetry={refresh} /> : loading || !catalog.data ?
      <LoadingState label="正在读取 Agent AI 服务与模型" rows={3} /> : catalog.data.partial ?
      <ErrorState message="暂时无法读取 Agent AI 服务与模型，请稍后重试。" onRetry={refresh} /> : <>
      <p className="text-xs text-muted-foreground">采集于 {formatDateTime(catalog.data.collectedAt)} · {new Set(services.map(service => service.agentId)).size} 个已上报服务的 Agent · {services.length} 个 AI 服务 · {new Set(services.flatMap(service => service.models.map(model => model.id))).size} 个模型（ID 去重）</p>
      <div className="flex flex-wrap items-center gap-3">
        <Input className="max-w-sm" aria-label="搜索 AI 服务或模型" placeholder="搜索 Agent、服务 ID、类型或模型" value={query} onChange={event => { setQuery(event.target.value); setPage(0); }} />
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={onlineOnly} onChange={event => { setOnlineOnly(event.target.checked); setPage(0); }} />仅显示在线 Agent</label>
      </div>
      {shown.length === 0 ? <EmptyState title={services.length === 0 ? "尚无 Agent 上报 AI 服务" : "没有符合搜索条件的服务或模型"} description={services.length === 0 ? "请确认 Agent 已连接到 Gateway，并已配置或发现本地 AI 后端。" : undefined} /> : <>
        <DataTable table={table} caption="Agent AI 服务列表" />
        <ListPager page={currentPage} count={shown.length} onChange={setPage} label="AI 服务分页" />
      </>}
    </>}
    <Dialog open={selected !== null} onOpenChange={open => { if (!open) setSelected(null); }}>
      {selected ? <ServiceModels key={JSON.stringify([selected.agentId, selected.serviceId])} service={selected} /> : null}
    </Dialog>
  </div>;
}

/** ServiceModels paginates one service's models in a separate detail window.
 * ServiceModels 在独立详情窗口中分页展示一个服务的模型。 */
function ServiceModels({ service }: { service: Service }) {
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(0);
  const models = service.models.filter(model => matchesQuery([model.id], query));
  const currentPage = Math.min(page, Math.max(0, Math.ceil(models.length / PAGE_SIZE) - 1));
  return <DialogContent className="sm:max-w-2xl">
    <DialogHeader><DialogTitle>模型详情</DialogTitle><DialogDescription className="break-all">Agent {service.agentId} · {service.serviceId} · {service.kind}</DialogDescription></DialogHeader>
    {!service.nodeOnline ? <p className="text-xs text-muted-foreground">Agent 已离线，以下为最近一次上报的服务与模型信息。</p> : null}
    <Input aria-label="搜索服务内模型" placeholder="搜索模型 ID" value={query} onChange={event => { setQuery(event.target.value); setPage(0); }} />
    {models.length === 0 ? <p className="text-sm text-muted-foreground">{service.models.length === 0 ? "尚未发现模型。" : "没有符合搜索条件的模型。"}</p> : <ul className="grid gap-2">{models.slice(currentPage * PAGE_SIZE, (currentPage + 1) * PAGE_SIZE).map(model => <li key={model.id} className="min-w-0 rounded-md bg-muted/40 p-3">
      <code className="break-all text-sm">{model.id}</code>
      <div className="mt-2 flex flex-wrap gap-1">{Object.entries(model.capabilities).map(([capability, support]) => <Badge key={capability} variant="outline" className="h-auto max-w-full whitespace-normal break-all text-xs">{capability} · {SUPPORT[support] ?? support}</Badge>)}</div>
    </li>)}</ul>}
    <ListPager page={currentPage} count={models.length} onChange={setPage} label="模型分页" />
  </DialogContent>;
}

/** ListPager bounds rendered rows within the collected snapshot.
 * ListPager 限制本次采集快照中同时渲染的行数。 */
function ListPager({ page, count, onChange, label }: { page: number; count: number; onChange: (page: number) => void; label: string }) {
  const pages = Math.max(1, Math.ceil(count / PAGE_SIZE));
  return <nav aria-label={label} className="flex flex-wrap items-center justify-between gap-3">
    <p className="text-xs text-muted-foreground">第 {page + 1} / {pages} 页 · 筛选结果 {count} 条 · 每页 {PAGE_SIZE} 条</p>
    <div className="flex gap-2"><Button variant="outline" size="sm" disabled={page === 0} onClick={() => onChange(page - 1)}>上一页</Button><Button variant="outline" size="sm" disabled={page + 1 >= pages} onClick={() => onChange(page + 1)}>下一页</Button></div>
  </nav>;
}

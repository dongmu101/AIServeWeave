"use client";
import { ErrorMessage } from "@/components/i18n/error-message";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import * as React from "react";
import { toast } from "sonner";

import { Pager } from "@/components/console/pager";
import { EmptyState, ErrorState, FormError, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { parseTenants, type Tenant } from "@/lib/console/contract";
import { parseFleetSnapshot } from "@/lib/console/fleet";
import { formatDateTime, matchesQuery } from "@/lib/console/format";
import { MAX_TENANTS_PER_NODE, mergeNodeRows, normalizeTenants, parseNodeAccess, parseNodeAccessList, tenantNodeOwner, validId, type NodeAccess, type NodeRow } from "@/lib/console/node-access";

const PAGE_SIZE = 25;

/** NodeAccessView lists nodes and edits the tenants one node is dedicated to. / NodeAccessView 列出节点，并编辑某台节点专属的租户。 */
export function NodeAccessView() {
  const { t } = useI18n();
  const fleet = useResource({ method: "GET", surface: "operator", path: "/operator/v1/nodes", parse: parseFleetSnapshot });
  const bindings = useResource({ method: "GET", surface: "operator", path: "/operator/v1/node-access", parse: parseNodeAccessList });
  const [saved, setSaved] = React.useState<Record<string, NodeAccess>>({});
  const [query, setQuery] = React.useState("");
  const [manual, setManual] = React.useState("");
  const [selected, setSelected] = React.useState<string | null>(null);
  const [names, setNames] = React.useState<Record<string, string>>({});

  const rows = React.useMemo(() => {
    const merged = new Map((bindings.data ?? []).map((binding) => [binding.nodeId, binding]));
    for (const entry of Object.values(saved)) merged.set(entry.nodeId, entry);
    const current = [...merged.values()].filter((entry) => entry.tenants.length > 0);
    const listed = mergeNodeRows((fleet.data?.nodes ?? []).map((node) => ({ nodeId: node.nodeId, live: node.live })), current);
    if (selected && !listed.some((row) => row.nodeId === selected)) listed.push({ nodeId: selected, live: null, tenants: [] });
    return listed;
  }, [bindings.data, fleet.data, saved, selected]);
  const shown = rows.filter((row) => matchesQuery([row.nodeId, ...row.tenants], query));
  const selectedRow = rows.find((row) => row.nodeId === selected) ?? null;
  const manualValid = validId(manual.trim());

  return <div className="grid gap-4">
    <div><h1 className="font-heading text-lg font-semibold"><Message id="operator.nodeAccess" /></h1><p className="text-sm text-muted-foreground"><Message id="operator.nodeAccessDescription" /></p></div>
    <Card><CardContent className="grid gap-4 pt-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
      <div className="grid content-start gap-3">
        <Input aria-label={t("operator.filterNodeAccess")} placeholder={t("operator.filterNodeAccess")} value={query} onChange={(e) => setQuery(e.target.value)} />
        {fleet.error ? <ErrorState message={fleet.error} onRetry={fleet.reload} /> : null}
        {bindings.error ? <ErrorState message={bindings.error} onRetry={bindings.reload} /> : bindings.loading || fleet.loading ? <LoadingState label={t("operator.loading")} rows={3} /> : shown.length === 0 ? <EmptyState title={t("operator.noNodesForAccess")} /> : <ul className="grid gap-2">{shown.map((row) => <li key={row.nodeId}>
          <NodeButton row={row} active={row.nodeId === selected} onSelect={() => setSelected(row.nodeId)} />
        </li>)}</ul>}
        <form className="flex gap-2" onSubmit={(e) => { e.preventDefault(); if (manualValid) { setSelected(manual.trim()); setManual(""); } }}>
          <Input aria-label={t("operator.nodeIdNotYetConnected")} placeholder={t("operator.nodeIdNotYetConnected")} value={manual} onChange={(e) => setManual(e.target.value)} />
          <Button type="submit" variant="outline" disabled={!manualValid}><Message id="operator.openNode" /></Button>
        </form>
      </div>
      {selectedRow && tenantNodeOwner(selectedRow.nodeId) !== null ? <TenantOwnedNode row={selectedRow} /> : selectedRow ? <BindingEditor key={selectedRow.nodeId} row={selectedRow} names={names} onNames={(found) => setNames((prior) => ({ ...prior, ...found }))} onSaved={(entry) => setSaved((prior) => ({ ...prior, [entry.nodeId]: entry }))} /> : <p className="text-sm text-muted-foreground"><Message id="operator.selectANodeFirst" /></p>}
    </CardContent></Card>
  </div>;
}

function NodeButton({ row, active, onSelect }: { row: NodeRow; active: boolean; onSelect: () => void }) {
  const { t } = useI18n();
  return <Button variant={active ? "secondary" : "ghost"} className="h-auto w-full justify-start whitespace-normal text-left" aria-current={active ? "true" : undefined} onClick={onSelect}>
    <span className="grid min-w-0 gap-1">
      <code className="break-all text-sm">{row.nodeId}</code>
      <span className="flex flex-wrap gap-1">
        {tenantNodeOwner(row.nodeId) !== null ? <Badge variant="default">{t("operator.tenantOwnedNode")}</Badge> : <Badge variant={row.tenants.length > 0 ? "default" : "outline"}>{row.tenants.length > 0 ? t("operator.dedicatedToCount", { count: row.tenants.length }) : t("operator.sharedNode")}</Badge>}
        {row.live === null ? <Badge variant="outline">{t("operator.notInFleet")}</Badge> : row.live ? null : <Badge variant="destructive">{t("operator.nodeOffline")}</Badge>}
      </span>
    </span>
  </Button>;
}

/** TenantOwnedNode shows a node a tenant enrolled itself, which no binding may open to others. / TenantOwnedNode 展示商户自己接入的节点，任何绑定都不能把它开放给别人。 */
function TenantOwnedNode({ row }: { row: NodeRow }) {
  const owner = tenantNodeOwner(row.nodeId) || "—";
  return <Card><CardHeader><CardTitle className="flex flex-wrap items-center gap-2 text-base"><code className="break-all">{row.nodeId}</code><Badge>{<Message id="operator.tenantOwnedNode" />}</Badge></CardTitle></CardHeader><CardContent>
    <p className="text-sm text-muted-foreground"><Message id="operator.tenantOwnedNodeHint" values={{ tenant: owner }} /></p>
  </CardContent></Card>;
}

/** BindingEditor edits one node's tenant list and replaces it whole. / BindingEditor 编辑一台节点的租户列表并整体替换。 */
function BindingEditor({ row, names, onNames, onSaved }: { row: NodeRow; names: Record<string, string>; onNames: (found: Record<string, string>) => void; onSaved: (entry: NodeAccess) => void }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [draft, setDraft] = React.useState<string[] | null>(null);
  const [last, setLast] = React.useState<NodeAccess | null>(null);
  const [query, setQuery] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const tenants = usePagedResource<Tenant>({ path: "/operator/v1/tenants", surface: "operator", filters: { q: query }, pageSize: PAGE_SIZE, parse: parseTenants });
  const items = tenants.items;
  React.useEffect(() => { if (items && items.length > 0) onNames(Object.fromEntries(items.map((tenant) => [tenant.id, tenant.name]))); }, [items, onNames]);
  const bound = draft ?? row.tenants;
  const full = bound.length >= MAX_TENANTS_PER_NODE;

  async function save() {
    setError(null);
    let next: string[];
    try { next = normalizeTenants(bound); } catch (failure) { setError(<ErrorMessage error={failure} />); return; }
    if (next.length === 0 && row.tenants.length > 0 && !window.confirm(t("operator.makeNodeSharedConfirm"))) return;
    setPending(true);
    try {
      const result = await run({ method: "PUT", surface: "operator", path: `/operator/v1/node-access/${encodeURIComponent(row.nodeId)}`, body: { tenants: next }, parse: parseNodeAccess });
      setLast(result); onSaved(result); setDraft(null); toast.success(<Message id="operator.nodeAccessSaved" />);
    } catch (failure) { setError(<ErrorMessage error={failure} />); }
    finally { setPending(false); }
  }

  return <Card><CardHeader><CardTitle className="flex flex-wrap items-center gap-2 text-base"><code className="break-all">{row.nodeId}</code><Badge variant="outline">{bound.length === 0 ? t("operator.sharedNode") : t("operator.dedicatedToCount", { count: bound.length })}</Badge></CardTitle></CardHeader><CardContent className="grid gap-4">
    <p className="text-sm text-muted-foreground"><Message id="operator.nodeAccessEditorHint" /></p>
    {last?.updatedAt ? <p className="text-xs text-muted-foreground"><Message id="operator.lastUpdated" values={{ actor: last.updatedBy ?? "—", time: formatDateTime(last.updatedAt, undefined, locale) }} /></p> : null}
    <div className="grid gap-2"><span className="text-sm font-medium"><Message id="operator.boundTenants" /></span>
      {bound.length === 0 ? <p className="text-sm text-muted-foreground"><Message id="operator.noBoundTenants" /></p> : <ul className="flex flex-wrap gap-2">{bound.map((id) => <li key={id}>
        <Button size="sm" variant="secondary" disabled={pending} aria-label={t("operator.removeTenantFromNode", { name: names[id] || id })} onClick={() => setDraft(bound.filter((x) => x !== id))}>
          <span className="break-all">{names[id] || id}</span><span aria-hidden="true">×</span>
        </Button>
      </li>)}</ul>}
    </div>
    <div className="grid content-start gap-2">
      <Input aria-label={t("operator.searchTenants")} placeholder={t("operator.searchTenants")} value={query} onChange={(e) => setQuery(e.target.value)} />
      {tenants.error ? <ErrorState message={tenants.error} onRetry={tenants.reload} /> : tenants.loading ? <LoadingState label={t("operator.loadingTenants")} rows={2} /> : <>
        {(items ?? []).length === 0 ? <p className="text-sm text-muted-foreground"><Message id="operator.noTenants" /></p> : <ul className="grid gap-1">{(items ?? []).map((tenant) => <li key={tenant.id} className="flex items-center gap-2">
          <span className="grid min-w-0 flex-1 gap-0.5"><span className="break-all text-sm">{tenant.name || tenant.id}</span><code className="break-all text-xs text-muted-foreground">{tenant.id}</code></span>
          {tenant.status !== "active" ? <Badge variant="destructive">{tenant.status}</Badge> : null}
          <Button size="sm" variant="outline" disabled={pending || full || bound.includes(tenant.id)} onClick={() => setDraft([...bound, tenant.id])}><Message id="operator.addTenantToNode" /></Button>
        </li>)}</ul>}
        <Pager resource={tenants} loadedCount={items?.length ?? 0} />
      </>}
      {full ? <p className="text-xs text-muted-foreground"><Message id="operator.tenantsPerNodeLimit" values={{ count: MAX_TENANTS_PER_NODE }} /></p> : null}
    </div>
    <div className="flex flex-wrap gap-2"><Button disabled={pending || draft === null} onClick={save}><Message id="operator.saveNodeAccess" /></Button><Button variant="outline" disabled={pending || draft === null} onClick={() => { setDraft(null); setError(null); }}><Message id="operator.discardNodeAccessChanges" /></Button></div>
    <FormError message={error} />
  </CardContent></Card>;
}

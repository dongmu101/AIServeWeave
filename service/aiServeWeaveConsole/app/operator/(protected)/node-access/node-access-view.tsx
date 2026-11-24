"use client";

import * as React from "react";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { Pager } from "@/components/console/pager";
import { EmptyState, ErrorState, FormError, LoadingState } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { useResource } from "@/components/console/use-resource";
import { ErrorMessage } from "@/components/i18n/error-message";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { parseTenants, type Tenant } from "@/lib/console/contract";
import { parseFleetSnapshot } from "@/lib/console/fleet";
import { formatDateTime, matchesQuery } from "@/lib/console/format";
import { MAX_TENANTS_PER_NODE, mergeNodeRows, normalizeTenants, parseNodeAccess, parseNodeAccessList, tenantNodeOwner, validId, type NodeAccess, type NodeAccessMode, type NodeRow } from "@/lib/console/node-access";

const PAGE_SIZE = 25;

const MODES = {
  private: { label: "operator.privateNode", hint: "operator.nodeAccessPrivateHint" },
  shared: { label: "operator.sharedNode", hint: "operator.nodeAccessSharedHint" },
  dedicated: { label: "operator.dedicatedNode", hint: "operator.nodeAccessDedicatedHint" },
} as const;

type AccessDraft = Pick<NodeAccess, "mode" | "tenants">;

export function NodeAccessView() {
  const { t } = useI18n();
  const fleet = useResource({ method: "GET", surface: "operator", path: "/operator/v1/nodes", parse: parseFleetSnapshot });
  const bindings = useResource({ method: "GET", surface: "operator", path: "/operator/v1/node-access", parse: parseNodeAccessList });
  const [saved, setSaved] = React.useState<Record<string, NodeAccess>>({});
  const [query, setQuery] = React.useState("");
  const [manual, setManual] = React.useState("");
  const [selected, setSelected] = React.useState<string | null>(null);
  const [switchTo, setSwitchTo] = React.useState<string | null>(null);
  const [draft, setDraft] = React.useState<AccessDraft | null>(null);
  const [pending, setPending] = React.useState(false);
  const [names, setNames] = React.useState<Record<string, string>>({});
  const editor = React.useRef<HTMLDivElement>(null);
  const rememberNames = React.useCallback((found: Record<string, string>) => {
    setNames((prior) => ({ ...prior, ...found }));
  }, []);

  const policies = React.useMemo(() => {
    const merged = new Map((bindings.data ?? []).map((binding) => [binding.nodeId, binding]));
    for (const entry of Object.values(saved)) merged.set(entry.nodeId, entry);
    return merged;
  }, [bindings.data, saved]);

  const rows = React.useMemo(() => {
    const listed = mergeNodeRows(
      (fleet.data?.nodes ?? []).map((node) => ({ nodeId: node.nodeId, live: node.live })),
      [...policies.values()],
    );
    if (selected && !listed.some((row) => row.nodeId === selected)) {
      listed.push({ mode: "private", nodeId: selected, live: null, tenants: [] });
    }
    return listed;
  }, [fleet.data, policies, selected]);

  const shown = rows.filter((row) => matchesQuery([row.nodeId, ...row.tenants.flatMap((id) => [id, names[id] ?? ""])], query));
  const selectedRow = rows.find((row) => row.nodeId === selected) ?? null;
  const dirty = draft !== null && selectedRow !== null && (
    draft.mode !== selectedRow.mode ||
    (draft.mode === "dedicated" && (draft.tenants.length !== selectedRow.tenants.length ||
      !draft.tenants.every((id) => selectedRow.tenants.includes(id))))
  );
  const ready = bindings.data !== null;
  const manualValid = validId(manual.trim());

  React.useEffect(() => {
    if (selected && ready) editor.current?.focus();
  }, [selected, ready]);

  function openNode(nodeId: string) {
    setDraft(null);
    setSelected(nodeId);
    setManual("");
  }

  function selectNode(nodeId: string) {
    if (!ready || pending) return;
    if (nodeId === selected) {
      setManual("");
      editor.current?.focus();
    } else if (dirty) {
      setSwitchTo(nodeId);
    } else {
      openNode(nodeId);
    }
  }

  return (
    <div className="grid gap-4">
      <div>
        <h1 className="font-heading text-xl font-semibold tracking-tight"><Message id="operator.sharingSettings" /></h1>
        <p className="text-sm text-muted-foreground"><Message id="operator.nodeAccessDescription" /></p>
      </div>
      <div className="grid min-w-0 items-start gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
        <Card className="min-w-0">
          <CardContent className="grid gap-3">
            <Input aria-label={t("operator.filterNodeAccess")} placeholder={t("operator.filterNodeAccess")} value={query} onChange={(event) => setQuery(event.target.value)} />
            {fleet.error ? <ErrorState message={fleet.error} onRetry={fleet.reload} /> : null}
            {bindings.error ? <ErrorState message={bindings.error} onRetry={bindings.reload} />
              : bindings.loading || (fleet.loading && shown.length === 0) ? <LoadingState label={t("operator.loading")} rows={3} />
              : shown.length > 0 ? (
                <ul className="grid max-h-64 gap-1 overflow-y-auto overscroll-contain lg:max-h-[65vh]">
                  {shown.map((row) => (
                    <li key={row.nodeId}>
                      <Button variant={row.nodeId === selected ? "secondary" : "ghost"} className="h-auto w-full justify-start whitespace-normal p-3 text-left" aria-current={row.nodeId === selected ? "true" : undefined} disabled={pending} onClick={() => selectNode(row.nodeId)}>
                        <span className="grid min-w-0 gap-1.5">
                          <code className="break-all text-sm">{row.nodeId}</code>
                          <span className="flex flex-wrap gap-1">
                            <AccessBadge row={row} />
                            {fleet.data ? <Badge variant={row.live === false ? "destructive" : "outline"}>{t(row.live === null ? "operator.notInFleet" : row.live ? "tenant.online" : "operator.nodeOffline")}</Badge> : null}
                          </span>
                        </span>
                      </Button>
                    </li>
                  ))}
                </ul>
              ) : rows.length > 0 ? <EmptyState title={t("tenant.noNodesMatchTheFilters")} />
              : fleet.error ? null : <EmptyState title={t("operator.noNodesForAccess")} />}
            <form className="flex min-w-0 gap-2 border-t pt-3" onSubmit={(event) => {
              event.preventDefault();
              if (manualValid) selectNode(manual.trim());
            }}>
              <Input className="min-w-0" aria-label={t("operator.nodeIdNotYetConnected")} placeholder={t("operator.nodeIdNotYetConnected")} value={manual} disabled={!ready || pending} onChange={(event) => setManual(event.target.value)} />
              <Button type="submit" variant="outline" disabled={!ready || pending || !manualValid}><Message id="operator.openNode" /></Button>
            </form>
          </CardContent>
        </Card>

        <div ref={editor} tabIndex={-1} className="min-w-0 scroll-mt-20 rounded-xl focus-visible:outline-2 focus-visible:outline-ring">
          {ready && selectedRow ? (
            tenantNodeOwner(selectedRow.nodeId) !== null ? <TenantOwnedNode row={selectedRow} />
              : <BindingEditor
                  key={selectedRow.nodeId}
                  row={selectedRow}
                  policy={policies.get(selectedRow.nodeId)}
                  draft={draft ?? selectedRow}
                  dirty={dirty}
                  pending={pending}
                  names={names}
                  onNames={rememberNames}
                  onDraft={setDraft}
                  onPending={setPending}
                  onSaved={(entry) => {
                    setSaved((prior) => ({ ...prior, [entry.nodeId]: entry }));
                    setDraft(null);
                  }}
                />
          ) : ready ? (
            <Card><CardContent><EmptyState title={t("operator.selectANodeFirst")} /></CardContent></Card>
          ) : null}
        </div>
      </div>

      <ConfirmDialog
        open={switchTo !== null}
        onOpenChange={(open) => { if (!open) setSwitchTo(null); }}
        title={t("operator.nodeAccessUnsaved")}
        description={switchTo ? t("operator.nodeAccessSwitchConfirm", { node: switchTo }) : ""}
        confirmLabel={t("operator.discardNodeAccessChanges")}
        onConfirm={async () => { if (switchTo) openNode(switchTo); }}
      />
    </div>
  );
}

function AccessBadge({ row }: { row: NodeRow }) {
  const { t } = useI18n();
  if (tenantNodeOwner(row.nodeId) !== null) return <Badge>{t("operator.tenantOwnedNode")}</Badge>;
  return <Badge variant={row.mode === "private" ? "outline" : "secondary"}>{row.mode === "dedicated"
    ? t("operator.dedicatedToCount", { count: row.tenants.length })
    : t(MODES[row.mode].label)}</Badge>;
}

function TenantOwnedNode({ row }: { row: NodeRow }) {
  return (
    <Card>
      <CardHeader><CardTitle className="flex flex-wrap items-center gap-2 text-base"><code className="break-all">{row.nodeId}</code><AccessBadge row={row} /></CardTitle></CardHeader>
      <CardContent><p className="text-sm text-muted-foreground"><Message id="operator.tenantOwnedNodeHint" values={{ tenant: tenantNodeOwner(row.nodeId) || "—" }} /></p></CardContent>
    </Card>
  );
}

function BindingEditor({ row, policy, draft, dirty, pending, names, onNames, onDraft, onPending, onSaved }: {
  row: NodeRow;
  policy?: NodeAccess;
  draft: AccessDraft;
  dirty: boolean;
  pending: boolean;
  names: Record<string, string>;
  onNames: (found: Record<string, string>) => void;
  onDraft: (draft: AccessDraft | null) => void;
  onPending: (pending: boolean) => void;
  onSaved: (entry: NodeAccess) => void;
}) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [error, setError] = React.useState<unknown>(null);
  const [confirmSave, setConfirmSave] = React.useState(false);
  const { mode, tenants } = draft;
  const removesBindings = row.mode === "dedicated" && mode !== "dedicated";
  const canSave = dirty && (mode !== "dedicated" || tenants.length > 0);

  function edit(next: AccessDraft | null) {
    setError(null);
    onDraft(next);
  }

  async function save() {
    if (pending || !canSave) return;
    onPending(true);
    try {
      const result = await run({
        method: "PUT",
        surface: "operator",
        path: `/operator/v1/node-access/${encodeURIComponent(row.nodeId)}`,
        body: { mode, tenants: mode === "dedicated" ? normalizeTenants(tenants) : [] },
        parse: parseNodeAccess,
      });
      onSaved(result);
      toast.success(<Message id="operator.nodeSharingSaved" />);
    } finally {
      onPending(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="break-all text-base"><code>{row.nodeId}</code></CardTitle>
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground"><Message id="operator.nodeAccessCurrent" /></span>
          <AccessBadge row={row} />
        </div>
        {policy?.updatedAt ? <p className="text-xs text-muted-foreground"><Message id="operator.lastUpdated" values={{ actor: policy.updatedBy ?? "—", time: formatDateTime(policy.updatedAt, undefined, locale) }} /></p> : null}
      </CardHeader>
      <CardContent>
        <form className="grid gap-4" onSubmit={async (event) => {
          event.preventDefault();
          if (pending || !canSave) return;
          setError(null);
          if (mode === "shared" || removesBindings) {
            setConfirmSave(true);
            return;
          }
          try { await save(); } catch (failure) { setError(failure); }
        }}>
          <fieldset className="grid min-w-0 gap-2" disabled={pending}>
            <legend className="mb-2 text-sm font-medium"><Message id="operator.nodeAccessMode" /></legend>
            <div className="grid gap-2 sm:grid-cols-3">
              {(Object.keys(MODES) as NodeAccessMode[]).map((option) => (
                <label key={option} className="flex min-h-10 cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm has-checked:border-primary has-checked:bg-accent has-disabled:cursor-default has-disabled:opacity-60">
                  <input type="radio" name="access-mode" value={option} checked={mode === option} onChange={() => edit({ mode: option, tenants })} className="shrink-0 accent-primary" />
                  <span className="min-w-0 wrap-break-word"><Message id={MODES[option].label} /></span>
                </label>
              ))}
            </div>
            <p className="text-sm text-muted-foreground"><Message id={MODES[mode].hint} /></p>
          </fieldset>

          {mode === "dedicated" ? (
            <TenantPicker tenants={tenants} names={names} pending={pending} onNames={onNames} onChange={(next) => edit({ mode, tenants: next })} />
          ) : null}

          <div className="grid gap-2 border-t pt-3">
            {dirty ? <p role="status" className="text-sm text-muted-foreground"><Message id="operator.nodeAccessUnsaved" /></p> : null}
            <div className="flex flex-wrap gap-2">
              <SubmitButton pending={pending && !confirmSave} disabled={pending || !canSave}><Message id="operator.saveNodeAccess" /></SubmitButton>
              <Button type="button" variant="outline" disabled={pending || !dirty} onClick={() => edit(null)}><Message id="operator.discardNodeAccessChanges" /></Button>
            </div>
            <FormError message={error === null ? null : <ErrorMessage error={error} />} />
          </div>
        </form>
      </CardContent>
      <ConfirmDialog
        open={confirmSave}
        onOpenChange={setConfirmSave}
        title={t(mode === "shared" ? "operator.enableNodeSharing" : "operator.saveNodeAccess")}
        description={<>
          <code className="block break-all">{row.nodeId}</code>
          <Message id={mode === "shared" ? "operator.makeNodeSharedConfirm" : MODES.private.hint} />
          {removesBindings ? <> <Message id="operator.nodeAccessRemoveBindings" /></> : null}
        </>}
        confirmLabel={t("operator.confirmAction")}
        onConfirm={save}
      />
    </Card>
  );
}

function TenantPicker({ tenants, names, pending, onNames, onChange }: {
  tenants: string[];
  names: Record<string, string>;
  pending: boolean;
  onNames: (found: Record<string, string>) => void;
  onChange: (tenants: string[]) => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = React.useState("");
  const results = usePagedResource<Tenant>({ path: "/operator/v1/tenants", surface: "operator", filters: { q: query.trim() }, pageSize: PAGE_SIZE, parse: parseTenants });
  const items = results.items;
  React.useEffect(() => {
    if (items?.length) onNames(Object.fromEntries(items.map((tenant) => [tenant.id, tenant.name])));
  }, [items, onNames]);
  const full = tenants.length >= MAX_TENANTS_PER_NODE;

  function toggle(id: string) {
    onChange(tenants.includes(id) ? tenants.filter((tenant) => tenant !== id) : [...tenants, id]);
  }

  return (
    <div className="grid gap-3">
      <div className="grid gap-2">
        <p className="text-sm font-medium"><Message id="operator.boundTenants" /></p>
        {tenants.length === 0 ? <p className="text-sm text-muted-foreground"><Message id="operator.noBoundTenants" /></p> : (
          <ul className="flex flex-wrap gap-2">
            {tenants.map((id) => (
              <li key={id} className="min-w-0 max-w-full">
                <Button type="button" size="sm" variant="secondary" className="h-auto max-w-full whitespace-normal text-left" disabled={pending} aria-label={t("operator.removeTenantFromNode", { name: names[id] || id })} onClick={() => toggle(id)}>
                  <span className="min-w-0 break-all">{names[id] || id}</span><span aria-hidden="true">×</span>
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <Input aria-label={t("operator.searchTenants")} placeholder={t("operator.searchTenants")} value={query} disabled={pending} onChange={(event) => setQuery(event.target.value)} />
      {results.error ? <ErrorState message={results.error} onRetry={results.reload} /> : results.loading ? <LoadingState label={t("operator.loadingTenants")} rows={2} /> : (
        <>
          {items?.length ? (
            <ul className="grid max-h-64 gap-1 overflow-y-auto overscroll-contain rounded-md border p-1">
              {items.map((tenant) => {
                const checked = tenants.includes(tenant.id);
                return (
                  <li key={tenant.id}>
                    <label className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 hover:bg-accent has-checked:bg-accent has-disabled:cursor-default">
                      <input type="checkbox" checked={checked} disabled={pending || (full && !checked)} onChange={() => toggle(tenant.id)} className="shrink-0 accent-primary" />
                      <span className="grid min-w-0 flex-1 gap-0.5">
                        <span className="break-all text-sm">{tenant.name || tenant.id}</span>
                        {tenant.name ? <code className="break-all text-xs text-muted-foreground">{tenant.id}</code> : null}
                      </span>
                      {tenant.status !== "active" ? <Badge variant="destructive">{tenant.status}</Badge> : null}
                    </label>
                  </li>
                );
              })}
            </ul>
          ) : <p className="text-sm text-muted-foreground"><Message id="operator.noTenants" /></p>}
          <Pager resource={results} loadedCount={items?.length ?? 0} />
        </>
      )}
      {full ? <p className="text-xs text-muted-foreground"><Message id="operator.tenantsPerNodeLimit" values={{ count: MAX_TENANTS_PER_NODE }} /></p> : null}
    </div>
  );
}

"use client";
import { draftWriteProblem } from "@/lib/i18n/problem";

import { ErrorMessage } from "@/components/i18n/error-message";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { createRouteReadGate } from "@/lib/console/route-read-gate";
import {ApiError } from "@/lib/console/errors";
import {importRouteFile, parseRoutes, parseRouteSnapshot, parseRouteHistory, parseRouteStatus, parseRouteValidation, type ModelRoute, type RouteSnapshot, type RouteHistory, type RouteStatus } from "@/lib/console/model-routes";

const path = "/operator/v1/routes";

/** RoutesView edits local drafts and explicitly publishes immutable revisions. / RoutesView 编辑本地草稿并显式发布不可变版本。 */
export function RoutesView() {
  const { t } = useI18n();
  const call = useConsoleRequest();
  const reads = React.useRef(createRouteReadGate());
  const draftInitialized = React.useRef(false);
  const [current, setCurrent] = React.useState<RouteSnapshot | null>(null);
  const [draft, setDraft] = React.useState<ModelRoute[]>([]);
  const [history, setHistory] = React.useState<RouteHistory>({ items: [] });
  const [selected, setSelected] = React.useState<RouteSnapshot | null>(null);
  const [status, setStatus] = React.useState<RouteStatus | null>(null);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const [verdict, setVerdict] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [confirm, setConfirm] = React.useState<"publish" | "rollback" | "replace" | null>(null);
  const [compareDraft, setCompareDraft] = React.useState(true);
  const [statusError, setStatusError] = React.useState(false);

  React.useEffect(() => {
    const gate = reads.current;
    const acceptCurrent = gate.begin("current"), acceptHistory = gate.begin("history"), acceptStatus = gate.begin("status");
    call({ method: "GET", surface: "operator", path, parse: parseRouteSnapshot }).then((value) => {
      if (acceptCurrent()) {
        setCurrent(value);
        if (!draftInitialized.current) { setDraft(parseRoutes(value.routes)); draftInitialized.current = true; }
      }
    }).catch((e) => { if (acceptCurrent()) setError(<ErrorMessage error={e} />); });
    call({ method: "GET", surface: "operator", path: `${path}/history`, query: { limit: "20" }, parse: parseRouteHistory }).then((value) => { if (acceptHistory()) setHistory(value); }).catch((e) => { if (acceptHistory()) setError(<ErrorMessage error={e} />); });
    call({ method: "GET", surface: "operator", path: `${path}/status`, parse: parseRouteStatus }).then((value) => { if (acceptStatus()) setStatus(value); }).catch(() => { if (acceptStatus()) setStatusError(true); });
    return () => gate.invalidate();
  }, [call]);

  function edit(next: ModelRoute[]) { setDraft(next); setVerdict(null); }
  function routeChange(index: number, route: ModelRoute) { edit(draft.map((item, i) => i === index ? route : item)); }
  async function action(work: () => Promise<void>) {
    setBusy(true); setError(null);
    try { await work(); } catch (e) { setError(<ErrorMessage error={e} />); } finally { setBusy(false); }
  }
  async function refreshCurrent() {
    const accept = reads.current.begin("current");
    try {
      const value = await call({ method: "GET", surface: "operator", path, parse: parseRouteSnapshot });
      if (accept()) {
        setCurrent(value); setVerdict(null);
        if (!draftInitialized.current) { setDraft(parseRoutes(value.routes)); draftInitialized.current = true; }
      }
    } catch (e) { if (accept()) throw e; }
  }
  async function refreshStatus() {
    const accept = reads.current.begin("status");
    setStatus(null); setStatusError(false);
    try {
      const value = await call({ method: "GET", surface: "operator", path: `${path}/status`, parse: parseRouteStatus });
      if (accept()) setStatus(value);
    } catch { if (accept()) setStatusError(true); }
  }
  async function loadHistory(before?: number) {
    const accept = reads.current.begin("history");
    try {
      const value = await call({ method: "GET", surface: "operator", path: `${path}/history`, query: { limit: "20", ...(before ? { before: String(before) } : {}) }, parse: parseRouteHistory });
      if (accept()) setHistory(value);
    } catch (e) { if (accept()) throw e; }
  }
  async function write() {
    if (!current) return;
    if (confirm === "replace") { edit(parseRoutes(current.routes)); return; }
    setBusy(true);
    reads.current.invalidate(); setStatus(null); setStatusError(false);
    try {
      const result = await call({ method: "POST", surface: "operator", path: `${path}/${confirm}`, body: confirm === "publish" ? { expected_revision: current.revision, routes: parseRoutes(draft) } : { expected_revision: current.revision, revision: selected!.revision }, parse: parseRouteSnapshot });
      setCurrent(result); setVerdict(null); setStatus(null);
      if (confirm === "publish") setDraft(parseRoutes(result.routes));
      void refreshStatus();
      void loadHistory().catch((e) => setError(<ErrorMessage error={e} />));
    } catch (e) {
      const problem = draftWriteProblem(e); setError(<ErrorMessage error={problem} />); throw problem;
    } finally { setBusy(false); }
  }

  return <div className="space-y-6">
    <div><h1 className="text-2xl font-semibold"><Message id="tenant.modelRoutes" /></h1><p className="text-sm text-muted-foreground"><Message id="operator.aliasesRuntimeModelsAndNodeSelectorsDraftsAre" /></p></div>
    {error && <p role="alert" className="text-destructive">{error}</p>}
    <section className="space-y-3 rounded-lg border p-4">
      <h2 className="font-semibold"><Message id="operator.currentRelease_aa08f8" values={{ value: current ? current.revision === 0 ? t("operator.notPublishedYet") : t("operator.version", { revision: current.revision }) : t("operator.loading") }} /></h2>
      {current && <p className="break-all text-xs text-muted-foreground">{current.digest || t("operator.noDigest")} · {current.revision > 0 ? current.created_at : t("operator.noPublicationTime")}</p>}
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" disabled={busy} onClick={() => action(refreshCurrent)}><Message id="operator.reloadCurrentVersionKeepDraft" /></Button>
        <Button variant="outline" disabled={busy || !current} onClick={() => setConfirm("replace")}><Message id="operator.replaceDraftWithCurrentVersion" /></Button>
      </div>
    </section>
    <section className="space-y-4 rounded-lg border p-4">
      <h2 className="font-semibold"><Message id="operator.localDraftAliases" values={{ count: draft.length }} /></h2>
      <fieldset disabled={busy || !current} className="space-y-4">
        <label className="grid gap-1 text-sm"><Message id="operator.importExistingJsonFilesUpTo1Mib_a1ba92" values={{ content: <input type="file" accept=".json,application/json" multiple onChange={(event) => {
          const files = Array.from(event.target.files ?? []); event.target.value = "";
          if (files.length) void action(async () => {
            const imported: ModelRoute[] = []; let bytes = 0;
            for (const file of files) { bytes += file.size; if (bytes > (1 << 20)) throw new ApiError("invalid"); imported.push(...await importRouteFile(file)); }
            edit(parseRoutes(imported));
          });
        }} /> }} /></label>
        {draft.map((route, index) => <div key={index} className="space-y-3 rounded-md border p-3">
          <div className="flex items-end gap-2"><label className="grid flex-1 gap-1 text-sm"><Message id="operator.modelAlias_cff727" values={{ content: <Input value={route.model} onChange={(e) => routeChange(index, { ...route, model: e.target.value })} /> }} /></label><Button variant="outline" onClick={() => edit(draft.filter((_, i) => i !== index))}><Message id="operator.deleteAlias" /></Button></div>
          {route.targets.map((target, targetIndex) => {
            const update = (next: typeof target) => routeChange(index, { ...route, targets: route.targets.map((item, i) => i === targetIndex ? next : item) });
            return <div key={targetIndex} className="space-y-2 rounded bg-muted/30 p-3">
              <div className="grid gap-2 md:grid-cols-4"><label className="grid gap-1 text-sm"><Message id="operator.runtimeModel_198c4a" values={{ content: <Input value={target.runtime_model} onChange={(e) => update({ ...target, runtime_model: e.target.value })} /> }} /></label><label className="grid gap-1 text-sm"><Message id="operator.priorityLowerFirst_f3f29e" values={{ content: <Input type="number" step="1" value={target.priority ?? 0} onChange={(e) => update({ ...target, priority: Number(e.target.value) })} /> }} /></label><label className="grid gap-1 text-sm"><Message id="operator.weight0IsTreatedAs1_3d0783" values={{ content: <Input type="number" min="0" step="1" value={target.weight ?? 0} onChange={(e) => update({ ...target, weight: Number(e.target.value) })} /> }} /></label><label className="grid gap-1 text-sm"><Message id="operator.minimumVramBytes0ForUnlimited_0dbeb1" values={{ content: <Input type="number" min="0" step="1" value={target.min_gpu_memory_bytes ?? 0} onChange={(e) => update({ ...target, min_gpu_memory_bytes: Number(e.target.value) })} /> }} /></label></div>
              <p className="text-sm"><Message id="operator.nodeSelectorAllLabelsMustMatchAnEmpty" /></p>
              {Object.entries(target.node_selector ?? {}).map(([key, value], selectorIndex) => <div className="flex gap-2" key={selectorIndex}><Input aria-label={t("operator.labelName")} value={key} onChange={(e) => { if (e.target.value !== key && Object.hasOwn(target.node_selector ?? {}, e.target.value)) { setError(<Message id={"operator.labelNamesMustBeUniqueTheOriginalLabel"} />); return; } update({ ...target, node_selector: Object.fromEntries(Object.entries(target.node_selector ?? {}).map((entry, i) => i === selectorIndex ? [e.target.value, value] : entry)) }); }} /><Input aria-label={t("operator.labelValue")} value={value} onChange={(e) => update({ ...target, node_selector: { ...target.node_selector, [key]: e.target.value } })} /><Button variant="outline" onClick={() => update({ ...target, node_selector: Object.fromEntries(Object.entries(target.node_selector ?? {}).filter(([name]) => name !== key)) })}><Message id="operator.deleteLabel" /></Button></div>)}
              <div className="flex gap-2"><Button variant="outline" onClick={() => { let key = "label"; while (Object.hasOwn(target.node_selector ?? {}, key)) key += "_"; update({ ...target, node_selector: { ...target.node_selector, [key]: "" } }); }}><Message id="operator.addLabel" /></Button><Button variant="outline" onClick={() => routeChange(index, { ...route, targets: route.targets.filter((_, i) => i !== targetIndex) })}><Message id="operator.deleteTarget" /></Button></div>
            </div>;
          })}
          <Button variant="outline" disabled={route.targets.length >= 100} onClick={() => routeChange(index, { ...route, targets: [...route.targets, { runtime_model: "", priority: 0, weight: 0 }] })}><Message id="operator.addTarget" /></Button>
        </div>)}
        <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={draft.length >= 1000} onClick={() => edit([...draft, { model: "", targets: [{ runtime_model: "" }] }])}><Message id="operator.addAlias" /></Button><Button variant="outline" onClick={() => action(async () => { setVerdict(null); setVerdict(await call({ method: "POST", surface: "operator", path: `${path}/validate`, body: { routes: parseRoutes(draft) }, parse: parseRouteValidation })); })}><Message id="operator.validateDraft" /></Button><Button disabled={!verdict} onClick={() => setConfirm("publish")}><Message id="operator.publishDraft" /></Button></div>
      </fieldset>
      {verdict && <p className="break-all text-sm"><Message id="operator.validationPassedNotPublishedYet" values={{ verdict: verdict }} /></p>}
    </section>
    <section className="space-y-3 rounded-lg border p-4"><div className="flex items-center justify-between"><h2 className="font-semibold"><Message id="operator.historyComparison" /></h2><Button variant="outline" disabled={busy} onClick={() => action(() => loadHistory())}><Message id="operator.latestVersion" /></Button></div>
      {history.items.length === 0 && <p className="text-sm text-muted-foreground"><Message id="operator.noHistoryVersionsLoadedYet" /></p>}
      <div className="flex flex-wrap gap-2">{history.items.map((item) => <Button key={item.revision} variant={selected?.revision === item.revision ? "default" : "outline"} disabled={busy} onClick={() => action(async () => setSelected(await call({ method: "GET", surface: "operator", path: `${path}/revisions/${item.revision}`, parse: parseRouteSnapshot })))}>v{item.revision} · {item.created_at}{item.rollback_of ? t("operator.rolledBackFromV", { rollback_of: item.rollback_of }) : ""}</Button>)}</div>
      {history.next_before && <Button variant="outline" disabled={busy} onClick={() => action(() => loadHistory(history.next_before))}><Message id="operator.olderVersions" /></Button>}
      {selected && <><div className="flex flex-wrap items-center gap-3"><p><Message id="operator.compareV_fb2a78" values={{ revision: selected.revision, actor_id: selected.actor_id }} /></p><label className="text-sm"><Message id="operator.compareWithDraftUncheckToUseCurrentRelease" values={{ content: <input type="checkbox" checked={compareDraft} onChange={(e) => setCompareDraft(e.target.checked)} /> }} /></label><Button variant="outline" disabled={busy || !current} onClick={() => setConfirm("rollback")}><Message id="operator.rollBackToV_b03299" values={{ revision: selected.revision }} /></Button></div><div className="grid gap-3 md:grid-cols-2"><div><h3><Message id="operator.historyV_76d5d0" values={{ revision: selected.revision }} /></h3><pre className="max-h-96 overflow-auto rounded bg-muted p-3 text-xs">{JSON.stringify(selected.routes, null, 2)}</pre></div><div><h3>{compareDraft ? t("operator.localDraft_8107cc") : t("operator.currentV", { value: current?.revision ?? "—" })}</h3><pre className="max-h-96 overflow-auto rounded bg-muted p-3 text-xs">{JSON.stringify(compareDraft ? draft : current?.routes, null, 2)}</pre></div></div></>}
    </section>
    <section className="space-y-3 rounded-lg border p-4"><div className="flex items-center justify-between"><h2 className="font-semibold"><Message id="operator.replicaApplicationStatus" /></h2><Button variant="outline" disabled={busy} onClick={() => action(refreshStatus)}><Message id="operator.refreshLive" /></Button></div>
      <p>{statusError ? t("operator.unableToConfirmReplicaStatusIncomplete") : status ? status.complete && current?.revision === status.desired_revision && current.digest === status.desired_digest ? t("operator.thisCheckAllReplicasHaveAppliedTheDesired") : t("operator.notAllReplicasHaveBeenConfirmedMissingFleet") : t("operator.noLiveConfirmationYet")}</p>
      {status && <><p className="break-all text-xs"><Message id="operator.desiredVChecked" values={{ desired_revision: status.desired_revision, desired_digest: status.desired_digest, checked_at: status.checked_at }} /></p><div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr><th><Message id="operator.replicaAddress" /></th><th><Message id="operator.mode" /></th><th><Message id="operator.versionDigest" /></th><th><Message id="operator.appliedCheckedAt" /></th><th><Message id="operator.observation" /></th></tr></thead><tbody>{status.replicas.map((row, i) => <tr key={`${row.endpoint}-${i}`} className="border-t"><td className="p-2">{row.replica_id ?? t("tenant.unknown")}<br />{row.endpoint}</td><td>{row.mode ?? t("tenant.unknown")}</td><td className="max-w-xs break-all">{row.revision ?? t("tenant.unknown")}<br />{row.digest ?? t("operator.noDigest")}</td><td>{row.applied_at ?? t("tenant.unknown")}<br />{row.checked_at ?? row.generated_at ?? t("tenant.unknown")}</td><td>{row.error ? t("operator.failedToLoad") : row.mode === "controlplane" && row.revision === status.desired_revision && row.digest === status.desired_digest ? t("operator.match") : t("operator.mismatch")}</td></tr>)}</tbody></table></div></>}
    </section>
    <ConfirmDialog open={confirm !== null} onOpenChange={(open) => { if (!open) setConfirm(null); }} title={confirm === "publish" ? t("operator.publishRoutingDraft") : confirm === "rollback" ? t("operator.rollBackToVersion", { revision: selected?.revision ?? "—" }) : t("operator.replaceLocalDraft")} description={confirm === "replace" ? t("operator.theCurrentReleaseWillReplaceTheLocalDraft") : t("operator.createANewVersionBasedOnTheCurrent", { revision: current?.revision ?? "—", value: confirm === "rollback" ? t("operator.theLocalDraftWillBeKept") : "" })} onConfirm={write} />
  </div>;
}

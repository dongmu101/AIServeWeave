"use client";
import { draftWriteProblem } from "@/lib/i18n/problem";

import { ErrorMessage } from "@/components/i18n/error-message";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { ComfyGraphPreview } from "@/components/console/comfy-graph-preview";
import { ComfyEditor, type ComfyEditorHandle } from "@/components/console/comfy-editor";
import { parseGraphText, type GraphBindings, type ParsedGraphText } from "@/lib/console/comfy-graph";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { createRouteReadGate } from "@/lib/console/route-read-gate";
import {ApiError } from "@/lib/console/errors";
import {importTemplateManifestFile, parseTemplateContent, parseTemplateSnapshot, parseTemplateSummaries, parseTemplateHistory, parseTemplateStatus, parseTemplateValidation, parseVisibleTenantIDs, type TemplateContent, type TemplateInput, type TemplateOutput, type TemplateSnapshot, type TemplateSummary, type TemplateHistory, type TemplateStatus
} from "@/lib/console/workflow-templates";

const base = "/operator/v1/workflow-templates";
const emptyDraft: TemplateContent = { description: "", inputs: [], outputs: [], dependencies: {}, graph: {} };

/** WorkflowTemplatesView creates, edits and publishes workflow templates (P03).
 *
 * Unlike routes, which is one platform-wide draft, this page manages many
 * independently versioned documents — the template id picker at the top is
 * what routes-view.tsx does not need.
 *
 * WorkflowTemplatesView 创建、编辑并发布工作流模板（P03）。
 *
 * 与路由那一份平台级草稿不同，本页管理的是多份各自独立版本化的文档——顶部的模板 id
 * 选择器正是 routes-view.tsx 不需要的东西。
 */
export function WorkflowTemplatesView() {
  const { t } = useI18n();
  const call = useConsoleRequest();
  const reads = React.useRef(createRouteReadGate());
  const [list, setList] = React.useState<TemplateSummary[]>([]);
  const [listError, setListError] = React.useState<React.ReactNode>(null);
  const [templateID, setTemplateID] = React.useState("");
  const [current, setCurrent] = React.useState<TemplateSnapshot | null>(null);
  const [notFound, setNotFound] = React.useState(false);
  const [draft, setDraft] = React.useState<TemplateContent>(emptyDraft);
  const [graphText, setGraphText] = React.useState("{}");
  const [visibleTenantsText, setVisibleTenantsText] = React.useState("");
  const [history, setHistory] = React.useState<TemplateHistory>({ items: [] });
  const [selected, setSelected] = React.useState<TemplateSnapshot | null>(null);
  const [status, setStatus] = React.useState<TemplateStatus | null>(null);
  const [statusError, setStatusError] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const [verdict, setVerdict] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [confirm, setConfirm] = React.useState<"publish" | "rollback" | "replace" | null>(null);
  const [compareDraft, setCompareDraft] = React.useState(true);
  const [compareView, setCompareView] = React.useState<"graph" | "json">("graph");
  // workflowGraphText is the graph text draft.workflow was saved or exported
  // together with. Once the textarea no longer matches it, the layout
  // describes a different graph and is left out of the publication.
  // workflowGraphText 是 draft.workflow 被保存或导出时配套的图文本。文本框一旦与之
  // 不再一致，布局描述的就是另一张图，发布时不再携带它。
  const [workflowGraphText, setWorkflowGraphText] = React.useState<string | null>(null);
  const [editorOpen, setEditorOpen] = React.useState(false);
  const [editorEdited, setEditorEdited] = React.useState(false);
  const editor = React.useRef<ComfyEditorHandle>(null);
  const layoutCurrent = draft.workflow !== undefined && workflowGraphText === graphText;

  // The preview reads a deferred copy of the textarea, so a 4 MiB graph is re-parsed and re-laid-out off the typing path.
  // 预览读取文本框的延迟副本，使 4 MiB 的图在输入路径之外重新解析与布局。
  const deferredGraphText = React.useDeferredValue(graphText);
  const draftGraph = React.useMemo(() => parseGraphText(deferredGraphText), [deferredGraphText]);
  const draftBindings = React.useMemo<GraphBindings>(() => ({ inputs: draft.inputs, outputs: draft.outputs ?? [] }), [draft.inputs, draft.outputs]);
  const selectedGraph = React.useMemo<ParsedGraphText | null>(() => selected ? { ok: true, graph: selected.graph } : null, [selected]);
  const selectedBindings = React.useMemo<GraphBindings>(() => ({ inputs: selected?.inputs ?? [], outputs: selected?.outputs ?? [] }), [selected]);
  const currentGraph = React.useMemo<ParsedGraphText>(() => current ? { ok: true, graph: current.graph } : { ok: true, graph: {} }, [current]);
  const currentBindings = React.useMemo<GraphBindings>(() => ({ inputs: current?.inputs ?? [], outputs: current?.outputs ?? [] }), [current]);
  const compareGraph = compareDraft ? draftGraph : currentGraph;

  const refreshList = React.useCallback(async () => {
    try {
      const value = await call({ method: "GET", surface: "operator", path: base, parse: parseTemplateSummaries });
      setList(value);
    } catch (e) { setListError(<ErrorMessage error={e} />); }
  }, [call]);
  // Called directly with .then/.catch rather than through refreshList: an
  // effect that hands a lint rule a reference to a function which itself
  // calls setState reads as "setState synchronously in an effect body" even
  // though the actual state update happens after refreshList's own await —
  // routes-view.tsx's mount effect avoids the same false positive the same
  // way, by calling the request inline instead of through a named callback.
  //
  // 这里直接用 .then/.catch 而不是经 refreshList：把一个自身会调用 setState 的
  // 函数引用交给 effect，会被某条 lint 规则读成"在 effect 体内同步调用
  // setState"，即便真正的状态更新发生在 refreshList 自己的 await 之后——
  // routes-view.tsx 的挂载 effect 用同样的办法避开了同一个误报：直接内联发起
  // 请求，而不是经过一个具名回调。
  React.useEffect(() => {
    call({ method: "GET", surface: "operator", path: base, parse: parseTemplateSummaries })
      .then((value) => setList(value))
      .catch((e) => setListError(<ErrorMessage error={e} />));
  }, [call]);

  async function action(work: () => Promise<void>) {
    setBusy(true); setError(null);
    try { await work(); } catch (e) { setError(<ErrorMessage error={e} />); } finally { setBusy(false); }
  }

  function loadDraftFrom(snapshot: TemplateSnapshot) {
    setDraftContent(snapshot);
    setVisibleTenantsText((snapshot.visible_tenant_ids ?? []).join(", "));
  }
  function setDraftContent(content: TemplateContent) {
    const text = JSON.stringify(content.graph, null, 2);
    setDraft({ description: content.description ?? "", inputs: content.inputs, outputs: content.outputs ?? [], dependencies: content.dependencies ?? {}, graph: content.graph, ...(content.workflow ? { workflow: content.workflow } : {}) });
    setGraphText(text);
    setWorkflowGraphText(content.workflow ? text : null);
    setEditorEdited(false);
    if (editorOpen) void loadEditor(content.workflow ? { kind: "workflow", text: JSON.stringify(content.workflow) } : { kind: "api", text: JSON.stringify(content.graph) });
  }
  async function loadEditor(source?: Parameters<ComfyEditorHandle["load"]>[0]) {
    const handle = editor.current;
    if (!handle) return;
    try {
      await handle.load(source ?? (layoutCurrent ? { kind: "workflow", text: JSON.stringify(draft.workflow) } : { kind: "api", text: graphText }));
      setEditorEdited(false);
    } catch { setError(<Message id={"operator.unableToLoadTheDraftIntoTheEditor"} />); }
  }
  async function exportEditor() {
    const handle = editor.current;
    if (!handle) return;
    const exported = await handle.exportGraph();
    if (exported.missing.length) {
      setError(<Message id={"operator.theGraphContainsNodesOutsideTheEditorCatalog"} values={{ value: exported.missing.slice(0, 10).join("、"), value2: exported.missing.length > 10 ? <Message id="operator.andOthers" /> : "" }} />);
      return;
    }
    const text = JSON.stringify(exported.graph, null, 2);
    setDraft({ ...draft, graph: exported.graph, workflow: exported.workflow });
    setGraphText(text);
    setWorkflowGraphText(text);
    setEditorEdited(false);
    setVerdict(null);
  }

  async function selectTemplate(id: string) {
    setTemplateID(id); setCurrent(null); setNotFound(false); setSelected(null); setStatus(null); setStatusError(false); setVerdict(null); setError(null);
    // The editor is closed rather than left showing the previous template, whose graph an export would otherwise copy into this one.
    // 关闭编辑器而不是让它继续显示上一个模板——否则一次导出会把那张图复制进这个模板。
    setDraft(emptyDraft); setGraphText("{}"); setWorkflowGraphText(null); setEditorEdited(false); setEditorOpen(false); setVisibleTenantsText("");
    if (!id.trim()) return;
    reads.current.invalidate();
    const acceptCurrent = reads.current.begin("current"), acceptHistory = reads.current.begin("history"), acceptStatus = reads.current.begin("status");
    call({ method: "GET", surface: "operator", path: `${base}/${encodeURIComponent(id)}`, parse: parseTemplateSnapshot }).then((value) => {
      if (acceptCurrent()) { setCurrent(value); loadDraftFrom(value); }
    }).catch((e) => {
      if (!acceptCurrent()) return;
      if (e instanceof ApiError && e.kind === "not_found") setNotFound(true); else setError(<ErrorMessage error={e} />);
    });
    call({ method: "GET", surface: "operator", path: `${base}/${encodeURIComponent(id)}/history`, query: { limit: "20" }, parse: parseTemplateHistory }).then((value) => { if (acceptHistory()) setHistory(value); }).catch(() => {});
    call({ method: "GET", surface: "operator", path: `${base}/status`, parse: parseTemplateStatus }).then((value) => { if (acceptStatus()) setStatus(value); }).catch(() => { if (acceptStatus()) setStatusError(true); });
  }
  async function refreshStatus() {
    const accept = reads.current.begin("status");
    setStatus(null); setStatusError(false);
    try {
      const value = await call({ method: "GET", surface: "operator", path: `${base}/status`, parse: parseTemplateStatus });
      if (accept()) setStatus(value);
    } catch { if (accept()) setStatusError(true); }
  }
  async function loadHistory(before?: number) {
    const accept = reads.current.begin("history");
    try {
      const value = await call({ method: "GET", surface: "operator", path: `${base}/${encodeURIComponent(templateID)}/history`, query: { limit: "20", ...(before ? { before: String(before) } : {}) }, parse: parseTemplateHistory });
      if (accept()) setHistory(value);
    } catch (e) { if (accept()) throw e; }
  }
  function currentContentForValidation(): TemplateContent {
    let graph: unknown;
    try { graph = JSON.parse(graphText); } catch { throw new ApiError("invalid"); }
    return parseTemplateContent({ ...draft, graph, workflow: layoutCurrent ? draft.workflow : undefined });
  }
  async function write() {
    setBusy(true); setError(null);
    if (confirm === "replace") { if (current) loadDraftFrom(current); setBusy(false); setConfirm(null); return; }
    reads.current.invalidate(); setStatus(null); setStatusError(false);
    try {
      const expected = current?.revision ?? 0;
      const result = await call({
        method: "POST", surface: "operator",
        path: `${base}/${encodeURIComponent(templateID)}/${confirm}`,
        body: confirm === "publish"
          ? { expected_revision: expected, content: currentContentForValidation(), visible_tenant_ids: parseVisibleTenantIDs(visibleTenantsText.split(",").map((s) => s.trim()).filter(Boolean)) }
          : { expected_revision: expected, revision: selected!.revision },
        parse: parseTemplateSnapshot,
      });
      setCurrent(result); setNotFound(false); loadDraftFrom(result); setVerdict(null);
      void refreshStatus(); void refreshList();
      void loadHistory().catch((e) => setError(<ErrorMessage error={e} />));
    } catch (e) {
      const problem = draftWriteProblem(e); setError(<ErrorMessage error={problem} />); throw problem;
    } finally { setBusy(false); setConfirm(null); }
  }

  function updateInput(index: number, next: TemplateInput) { setDraft({ ...draft, inputs: draft.inputs.map((item, i) => i === index ? next : item) }); }
  function updateOutput(index: number, next: TemplateOutput) { setDraft({ ...draft, outputs: (draft.outputs ?? []).map((item, i) => i === index ? next : item) }); }

  return <div className="space-y-6">
    <div><h1 className="text-2xl font-semibold"><Message id="operator.workflowTemplates" /></h1><p className="text-sm text-muted-foreground"><Message id="operator.createPublishAndRollBackWorkflowTemplatesEach" /></p></div>
    {error && <p role="alert" className="text-destructive">{error}</p>}
    <section className="space-y-3 rounded-lg border p-4">
      <h2 className="font-semibold"><Message id="operator.selectTemplate" /></h2>
      {listError && <p className="text-destructive text-sm">{listError}</p>}
      <div className="flex flex-wrap gap-2">{list.map((item) => <Button key={item.template_id} className="h-auto max-w-full whitespace-normal break-all" variant={templateID === item.template_id ? "default" : "outline"} disabled={busy} onClick={() => void action(() => selectTemplate(item.template_id))}>{item.template_id} · v{item.revision}</Button>)}</div>
      <div className="flex items-end gap-2"><label className="grid flex-1 gap-1 text-sm"><Message id="operator.templateIdEditIfItExistsOtherwiseCreate_183780" values={{ content: <Input value={templateID} onChange={(e) => setTemplateID(e.target.value)} /> }} /></label><Button variant="outline" disabled={busy || !templateID.trim()} onClick={() => void action(() => selectTemplate(templateID))}><Message id="operator.load" /></Button></div>
      {notFound && <p className="text-sm text-muted-foreground"><Message id="operator.thisIdHasNotBeenPublishedYetThe" /></p>}
    </section>
    {templateID.trim() !== "" && <>
      <section className="space-y-3 rounded-lg border p-4">
        <h2 className="font-semibold"><Message id="operator.currentRelease_aa08f8" values={{ value: current ? t("operator.version", { revision: current.revision }) : notFound ? t("operator.notCreatedYet") : t("operator.loading") }} /></h2>
        {current && <p className="break-all text-xs text-muted-foreground">{current.digest} · {current.created_at} · {current.actor_id}</p>}
        <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={busy || !current} onClick={() => setConfirm("replace")}><Message id="operator.replaceDraftWithCurrentVersion" /></Button></div>
      </section>
      <section className="space-y-4 rounded-lg border p-4">
        <h2 className="font-semibold"><Message id="operator.localDraft_8107cc" /></h2>
        <fieldset disabled={busy} className="space-y-4">
          <label className="grid gap-1 text-sm"><Message id="operator.description_7a1904" values={{ content: <Input value={draft.description ?? ""} onChange={(e) => setDraft({ ...draft, description: e.target.value })} /> }} /></label>
          <label className="grid gap-1 text-sm"><Message id="operator.visibleTenantIdsCommaSeparatedBlankForAll_c8eca5" values={{ content: <Input value={visibleTenantsText} onChange={(e) => setVisibleTenantsText(e.target.value)} /> }} /></label>
          <label className="grid gap-1 text-sm"><Message id="operator.importAnExistingTemplateManifestJsonTheFormat" values={{ content: <input type="file" accept=".json,application/json" onChange={(event) => {
              const file = event.target.files?.[0]; event.target.value = "";
              if (file) void action(async () => {
                setDraftContent(await importTemplateManifestFile(file));
              });
            }} /> }} /></label>
          <label className="grid gap-1 text-sm"><Message id="operator.comfyuiGraphApiFormatJsonNeverExposedTo_7e711a" values={{ content: <textarea className="min-h-48 w-full rounded-lg border border-input bg-transparent px-2.5 py-1.5 font-mono text-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50" value={graphText} onChange={(e) => setGraphText(e.target.value)} /> }} /></label>
          {draft.workflow !== undefined && !layoutCurrent && <p className="text-sm text-amber-700 dark:text-amber-400"><Message id="operator.theGraphJsonHasBeenEditedInThe" /></p>}
          <details className="space-y-2" open={editorOpen} onToggle={(e) => setEditorOpen(e.currentTarget.open)}><summary className="cursor-pointer text-sm font-medium"><Message id="operator.visualEditorOfficialComfyuiFrontendEditingOnlyNo" /></summary>
            {editorOpen && <>
              <div className="flex flex-wrap items-center gap-2">
                <Button type="button" variant="outline" onClick={() => void loadEditor()}><Message id="operator.loadFromDraft" /></Button>
                <Button type="button" onClick={() => void action(exportEditor)}><Message id="operator.exportToDraft" /></Button>
                <Button type="button" variant="outline" onClick={() => void action(async () => { await editor.current?.toggleAppMode(); })}><Message id="operator.toggleAppMode" /></Button>
                <span className="text-xs text-muted-foreground">{editorEdited ? t("operator.theEditorHasChangesThatHaveNotBeen") : layoutCurrent ? t("operator.theDraftIncludesALayoutMatchingTheGraph") : t("operator.theDraftHasNoLayoutAfterExportThe")}</span>
              </div>
              <ComfyEditor ref={editor} onReady={() => void loadEditor()} onEdit={() => setEditorEdited(true)} />
              <p className="text-xs text-muted-foreground"><Message id="operator.theNodeCatalogIncludesOnlyComfyuiCoreNodes" /></p>
            </>}
          </details>
          <details open className="space-y-2"><summary className="cursor-pointer text-sm font-medium"><Message id="operator.nodeGraphPreviewReadOnlyUpdatesLiveWith" /></summary>
            <ComfyGraphPreview graph={draftGraph} bindings={draftBindings} />
          </details>
          <div className="space-y-2"><h3 className="font-medium"><Message id="tenant.inputs" /></h3>
            {draft.inputs.map((input, index) => <div key={index} className="grid gap-2 rounded bg-muted/30 p-3 md:grid-cols-3">
              <label className="grid gap-1 text-xs"><Message id="operator.name" values={{ content: <Input value={input.name} onChange={(e) => updateInput(index, { ...input, name: e.target.value })} /> }} /></label>
              <label className="grid gap-1 text-xs"><Message id="operator.node" values={{ content: <Input value={input.node} onChange={(e) => updateInput(index, { ...input, node: e.target.value })} /> }} /></label>
              <label className="grid gap-1 text-xs"><Message id="operator.field_87f1ec" values={{ content: <Input value={input.field} onChange={(e) => updateInput(index, { ...input, field: e.target.value })} /> }} /></label>
              <label className="grid gap-1 text-xs"><Message id="operator.type" values={{ content: <select className="h-8 rounded-lg border border-input bg-transparent px-2 text-sm" value={input.type} onChange={(e) => updateInput(index, { ...input, type: e.target.value as TemplateInput["type"] })}><option value="string">string</option><option value="integer">integer</option><option value="number">number</option><option value="boolean">boolean</option></select> }} /></label>
              <label className="flex items-center gap-2 text-xs"><Message id="operator.required" values={{ content: <input type="checkbox" checked={input.required} onChange={(e) => updateInput(index, { ...input, required: e.target.checked })} /> }} /></label>
              <Button variant="outline" onClick={() => setDraft({ ...draft, inputs: draft.inputs.filter((_, i) => i !== index) })}><Message id="operator.deleteInput" /></Button>
            </div>)}
            <Button variant="outline" disabled={draft.inputs.length >= 100} onClick={() => setDraft({ ...draft, inputs: [...draft.inputs, { name: "", node: "", field: "", type: "string", required: false }] })}><Message id="operator.addInput" /></Button>
          </div>
          <div className="space-y-2"><h3 className="font-medium"><Message id="operator.outputsDeclareOutputNodesChecksOnlyThatNodes" /></h3>
            {(draft.outputs ?? []).map((output, index) => <div key={index} className="grid gap-2 rounded bg-muted/30 p-3 md:grid-cols-3">
              <label className="grid gap-1 text-xs"><Message id="operator.name" values={{ content: <Input value={output.name} onChange={(e) => updateOutput(index, { ...output, name: e.target.value })} /> }} /></label>
              <label className="grid gap-1 text-xs"><Message id="operator.node" values={{ content: <Input value={output.node} onChange={(e) => updateOutput(index, { ...output, node: e.target.value })} /> }} /></label>
              <label className="grid gap-1 text-xs"><Message id="operator.kindEGImageVideo_8281eb" values={{ content: <Input value={output.type} onChange={(e) => updateOutput(index, { ...output, type: e.target.value })} /> }} /></label>
              <Button variant="outline" onClick={() => setDraft({ ...draft, outputs: (draft.outputs ?? []).filter((_, i) => i !== index) })}><Message id="operator.deleteOutput" /></Button>
            </div>)}
            <Button variant="outline" disabled={(draft.outputs ?? []).length >= 20} onClick={() => setDraft({ ...draft, outputs: [...(draft.outputs ?? []), { name: "", node: "", type: "" }] })}><Message id="operator.addOutput" /></Button>
          </div>
          <div className="grid gap-3 md:grid-cols-2">
            <label className="grid gap-1 text-sm"><Message id="operator.customNodeDependenciesOneNameVersionPerLine_1805f2" values={{ content: <textarea className="min-h-20 w-full rounded-lg border border-input bg-transparent px-2.5 py-1.5 font-mono text-xs outline-none" value={(draft.dependencies?.custom_nodes ?? []).map((d) => d.version ? `${d.name},${d.version}` : d.name).join("\n")} onChange={(e) => setDraft({ ...draft, dependencies: { ...draft.dependencies, custom_nodes: e.target.value.split("\n").map((line) => line.trim()).filter(Boolean).map((line) => { const [name, version] = line.split(","); return version ? { name: name.trim(), version: version.trim() } : { name: name.trim() }; }) } })} /> }} /></label>
            <label className="grid gap-1 text-sm"><Message id="operator.modelDependenciesOneNameVersionPerLine_23fee4" values={{ content: <textarea className="min-h-20 w-full rounded-lg border border-input bg-transparent px-2.5 py-1.5 font-mono text-xs outline-none" value={(draft.dependencies?.models ?? []).map((d) => d.version ? `${d.name},${d.version}` : d.name).join("\n")} onChange={(e) => setDraft({ ...draft, dependencies: { ...draft.dependencies, models: e.target.value.split("\n").map((line) => line.trim()).filter(Boolean).map((line) => { const [name, version] = line.split(","); return version ? { name: name.trim(), version: version.trim() } : { name: name.trim() }; }) } })} /> }} /></label>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => void action(async () => { setVerdict(null); setVerdict(await call({ method: "POST", surface: "operator", path: `${base}/${encodeURIComponent(templateID)}/validate`, body: { content: currentContentForValidation(), visible_tenant_ids: parseVisibleTenantIDs(visibleTenantsText.split(",").map((s) => s.trim()).filter(Boolean)) }, parse: parseTemplateValidation })); })}><Message id="operator.validateDraft" /></Button>
            <Button disabled={!verdict} onClick={() => setConfirm("publish")}>{current ? t("operator.publishNewVersion") : t("operator.createPublish")}</Button>
          </div>
        </fieldset>
        {verdict && <p className="break-all text-sm"><Message id="operator.validationPassedNotPublishedYet" values={{ verdict: verdict }} /></p>}
      </section>
      <section className="space-y-3 rounded-lg border p-4"><div className="flex items-center justify-between"><h2 className="font-semibold"><Message id="operator.historyComparison" /></h2><Button variant="outline" disabled={busy} onClick={() => void action(() => loadHistory())}><Message id="operator.latestVersion" /></Button></div>
        {history.items.length === 0 && <p className="text-sm text-muted-foreground"><Message id="operator.noHistoryVersionsLoadedYet" /></p>}
        <div className="flex flex-wrap gap-2">{history.items.map((item) => <Button key={item.revision} variant={selected?.revision === item.revision ? "default" : "outline"} disabled={busy} onClick={() => void action(async () => setSelected(await call({ method: "GET", surface: "operator", path: `${base}/${encodeURIComponent(templateID)}/revisions/${item.revision}`, parse: parseTemplateSnapshot })))}>v{item.revision} · {item.created_at}{item.rollback_of ? t("operator.rolledBackFromV", { rollback_of: item.rollback_of }) : ""}</Button>)}</div>
        {history.next_before && <Button variant="outline" disabled={busy} onClick={() => void action(() => loadHistory(history.next_before))}><Message id="operator.olderVersions" /></Button>}
        {selected && <><div className="flex flex-wrap items-center gap-3"><p><Message id="operator.compareV_fb2a78" values={{ revision: selected.revision, actor_id: selected.actor_id }} /></p><label className="text-sm"><Message id="operator.compareWithDraftUncheckToUseCurrentRelease" values={{ content: <input type="checkbox" checked={compareDraft} onChange={(e) => setCompareDraft(e.target.checked)} /> }} /></label><Button variant="outline" disabled={busy || !current} onClick={() => setConfirm("rollback")}><Message id="operator.rollBackToV_b03299" values={{ revision: selected.revision }} /></Button>
            <div className="flex gap-1"><Button variant={compareView === "graph" ? "default" : "outline"} onClick={() => setCompareView("graph")}><Message id="operator.nodeGraph" /></Button><Button variant={compareView === "json" ? "default" : "outline"} onClick={() => setCompareView("json")}>JSON</Button></div></div>
          {compareView === "graph" && selectedGraph
            ? <div className="grid gap-3 md:grid-cols-2">
                <div className="min-w-0 space-y-1"><h3><Message id="operator.historyV_76d5d0" values={{ revision: selected.revision }} /></h3><ComfyGraphPreview graph={selectedGraph} bindings={selectedBindings} compareTo={compareGraph.ok ? compareGraph.graph : undefined} height={440} /></div>
                <div className="min-w-0 space-y-1"><h3>{compareDraft ? t("operator.localDraft_8107cc") : t("operator.currentV", { value: current?.revision ?? "—" })}</h3><ComfyGraphPreview graph={compareGraph} bindings={compareDraft ? draftBindings : currentBindings} compareTo={selected.graph} height={440} /></div>
                <p className="text-xs text-muted-foreground md:col-span-2"><Message id="operator.yellowBorderPresentOnBothSidesButWith" /></p>
              </div>
            : <div className="grid gap-3 md:grid-cols-2"><div><h3><Message id="operator.historyV_76d5d0" values={{ revision: selected.revision }} /></h3><pre className="max-h-96 overflow-auto rounded bg-muted p-3 text-xs">{JSON.stringify({ description: selected.description, inputs: selected.inputs, outputs: selected.outputs, dependencies: selected.dependencies, visible_tenant_ids: selected.visible_tenant_ids, layout: selected.workflow ? t("operator.present") : t("tenant.none"), graph: selected.graph }, null, 2)}</pre></div><div><h3>{compareDraft ? t("operator.localDraft_8107cc") : t("operator.currentV", { value: current?.revision ?? "—" })}</h3><pre className="max-h-96 overflow-auto rounded bg-muted p-3 text-xs">{JSON.stringify(compareDraft ? { ...draft, workflow: undefined, layout: layoutCurrent ? t("operator.present") : t("tenant.none"), graph: draftGraph.ok ? draftGraph.graph : t("operator.graphJsonCannotBeParsed") } : current && { ...current, workflow: undefined, layout: current.workflow ? t("operator.present") : t("tenant.none") }, null, 2)}</pre></div></div>}</>}
      </section>
      <section className="space-y-3 rounded-lg border p-4"><div className="flex items-center justify-between"><h2 className="font-semibold"><Message id="operator.replicaApplicationStatusBundle" /></h2><Button variant="outline" disabled={busy} onClick={() => void action(refreshStatus)}><Message id="operator.refreshLive" /></Button></div>
        <p>{statusError ? t("operator.unableToConfirmReplicaStatusIncomplete") : status ? status.complete ? t("operator.thisCheckAllReplicasHaveAppliedTheDesired_231803") : t("operator.notAllReplicasHaveBeenConfirmedMissingFleet_5f7ab0") : t("operator.noLiveConfirmationYet")}</p>
        {status && <><p className="break-all text-xs"><Message id="operator.desiredTemplatesChecked" values={{ desired_template_count: status.desired_template_count, desired_bundle_digest: status.desired_bundle_digest, checked_at: status.checked_at }} /></p><div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr><th><Message id="operator.replicaAddress" /></th><th><Message id="operator.mode" /></th><th><Message id="operator.countDigest" /></th><th><Message id="operator.appliedCheckedAt" /></th><th><Message id="operator.observation" /></th></tr></thead><tbody>{status.replicas.map((row, i) => <tr key={`${row.endpoint}-${i}`} className="border-t"><td className="p-2">{row.replica_id ?? t("tenant.unknown")}<br />{row.endpoint}</td><td>{row.mode ?? t("tenant.unknown")}</td><td className="max-w-xs break-all">{row.template_count ?? t("tenant.unknown")}<br />{row.bundle_digest ?? t("operator.noDigest")}</td><td>{row.applied_at ?? t("tenant.unknown")}<br />{row.checked_at ?? row.generated_at ?? t("tenant.unknown")}</td><td>{row.error ? t("operator.failedToLoad") : row.mode === "controlplane" && row.template_count === status.desired_template_count && row.bundle_digest === status.desired_bundle_digest ? t("operator.match") : t("operator.mismatch")}</td></tr>)}</tbody></table></div></>}
      </section>
    </>}
    <ConfirmDialog open={confirm !== null} onOpenChange={(open) => { if (!open) setConfirm(null); }} title={confirm === "publish" ? (current ? t("operator.publishANewVersionOfTemplate", { templateID: templateID }) : t("operator.createTemplate", { templateID: templateID })) : confirm === "rollback" ? t("operator.rollBackToVersion", { revision: selected?.revision ?? "—" }) : t("operator.replaceLocalDraft")} description={confirm === "replace" ? t("operator.theCurrentReleaseWillReplaceTheLocalDraft") : t("operator.createANewVersionBasedOnTheCurrent_d788bb", { value: current?.revision ?? 0, value2: confirm === "rollback" ? t("operator.theLocalDraftWillBeKept") : "" })} onConfirm={write} />
  </div>;
}

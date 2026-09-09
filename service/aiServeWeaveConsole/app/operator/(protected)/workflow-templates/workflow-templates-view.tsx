"use client";

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { createRouteReadGate } from "@/lib/console/route-read-gate";
import { ApiError, describe } from "@/lib/console/errors";
import {
  importTemplateManifestFile,
  parseTemplateContent,
  parseTemplateSnapshot,
  parseTemplateSummaries,
  parseTemplateHistory,
  parseTemplateStatus,
  parseTemplateValidation,
  parseVisibleTenantIDs,
  templateWriteMessage,
  type TemplateContent,
  type TemplateInput,
  type TemplateOutput,
  type TemplateSnapshot,
  type TemplateSummary,
  type TemplateHistory,
  type TemplateStatus,
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
  const call = useConsoleRequest();
  const reads = React.useRef(createRouteReadGate());
  const [list, setList] = React.useState<TemplateSummary[]>([]);
  const [listError, setListError] = React.useState<string | null>(null);
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
  const [error, setError] = React.useState<string | null>(null);
  const [verdict, setVerdict] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [confirm, setConfirm] = React.useState<"publish" | "rollback" | "replace" | null>(null);
  const [compareDraft, setCompareDraft] = React.useState(true);

  const refreshList = React.useCallback(async () => {
    try {
      const value = await call({ method: "GET", surface: "operator", path: base, parse: parseTemplateSummaries });
      setList(value);
    } catch (e) { setListError(describe(e)); }
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
      .catch((e) => setListError(describe(e)));
  }, [call]);

  async function action(work: () => Promise<void>) {
    setBusy(true); setError(null);
    try { await work(); } catch (e) { setError(describe(e)); } finally { setBusy(false); }
  }

  function loadDraftFrom(snapshot: TemplateSnapshot) {
    setDraft({ description: snapshot.description, inputs: snapshot.inputs, outputs: snapshot.outputs ?? [], dependencies: snapshot.dependencies ?? {}, graph: snapshot.graph });
    setGraphText(JSON.stringify(snapshot.graph, null, 2));
    setVisibleTenantsText((snapshot.visible_tenant_ids ?? []).join(", "));
  }

  async function selectTemplate(id: string) {
    setTemplateID(id); setCurrent(null); setNotFound(false); setSelected(null); setStatus(null); setStatusError(false); setVerdict(null); setError(null);
    setDraft(emptyDraft); setGraphText("{}"); setVisibleTenantsText("");
    if (!id.trim()) return;
    reads.current.invalidate();
    const acceptCurrent = reads.current.begin("current"), acceptHistory = reads.current.begin("history"), acceptStatus = reads.current.begin("status");
    call({ method: "GET", surface: "operator", path: `${base}/${encodeURIComponent(id)}`, parse: parseTemplateSnapshot }).then((value) => {
      if (acceptCurrent()) { setCurrent(value); loadDraftFrom(value); }
    }).catch((e) => {
      if (!acceptCurrent()) return;
      if (e instanceof ApiError && e.kind === "not_found") setNotFound(true); else setError(describe(e));
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
    return parseTemplateContent({ ...draft, graph });
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
      void loadHistory().catch((e) => setError(describe(e)));
    } catch (e) {
      const message = templateWriteMessage(e); setError(message); throw new Error(message);
    } finally { setBusy(false); setConfirm(null); }
  }

  function updateInput(index: number, next: TemplateInput) { setDraft({ ...draft, inputs: draft.inputs.map((item, i) => i === index ? next : item) }); }
  function updateOutput(index: number, next: TemplateOutput) { setDraft({ ...draft, outputs: (draft.outputs ?? []).map((item, i) => i === index ? next : item) }); }

  return <div className="space-y-6">
    <div><h1 className="text-2xl font-semibold">工作流模板</h1><p className="text-sm text-muted-foreground">创建、发布与回滚工作流模板；每个模板独立版本化。依赖检查仅结构性声明校验，不核对节点实际已装能力。草稿仅保留在当前页面。</p></div>
    {error && <p role="alert" className="text-destructive">{error}</p>}
    <section className="space-y-3 rounded-lg border p-4">
      <h2 className="font-semibold">选择模板</h2>
      {listError && <p className="text-destructive text-sm">{listError}</p>}
      <div className="flex flex-wrap gap-2">{list.map((item) => <Button key={item.template_id} variant={templateID === item.template_id ? "default" : "outline"} disabled={busy} onClick={() => void action(() => selectTemplate(item.template_id))}>{item.template_id} · v{item.revision}</Button>)}</div>
      <div className="flex items-end gap-2"><label className="grid flex-1 gap-1 text-sm">模板 ID（已存在则编辑，否则创建新模板）<Input value={templateID} onChange={(e) => setTemplateID(e.target.value)} /></label><Button variant="outline" disabled={busy || !templateID.trim()} onClick={() => void action(() => selectTemplate(templateID))}>加载</Button></div>
      {notFound && <p className="text-sm text-muted-foreground">该 ID 尚未发布，以下表单将创建它的第一个版本。</p>}
    </section>
    {templateID.trim() !== "" && <>
      <section className="space-y-3 rounded-lg border p-4">
        <h2 className="font-semibold">当前发布：{current ? `版本 ${current.revision}` : notFound ? "尚未创建" : "读取中"}</h2>
        {current && <p className="break-all text-xs text-muted-foreground">{current.digest} · {current.created_at} · {current.actor_id}</p>}
        <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={busy || !current} onClick={() => setConfirm("replace")}>用当前版本替换草稿</Button></div>
      </section>
      <section className="space-y-4 rounded-lg border p-4">
        <h2 className="font-semibold">本地草稿</h2>
        <fieldset disabled={busy} className="space-y-4">
          <label className="grid gap-1 text-sm">描述<Input value={draft.description ?? ""} onChange={(e) => setDraft({ ...draft, description: e.target.value })} /></label>
          <label className="grid gap-1 text-sm">可见租户 ID（逗号分隔；留空表示对所有租户可见）<Input value={visibleTenantsText} onChange={(e) => setVisibleTenantsText(e.target.value)} /></label>
          <label className="grid gap-1 text-sm">导入现有模板清单 JSON（file 模式沿用的 {"{description, inputs, graph}"} 格式，可直接导入；最多 4 MiB + 256 KiB）
            <input type="file" accept=".json,application/json" onChange={(event) => {
              const file = event.target.files?.[0]; event.target.value = "";
              if (file) void action(async () => {
                const imported = await importTemplateManifestFile(file);
                setDraft({ description: imported.description ?? "", inputs: imported.inputs, outputs: imported.outputs ?? [], dependencies: imported.dependencies ?? {}, graph: imported.graph });
                setGraphText(JSON.stringify(imported.graph, null, 2));
              });
            }} />
          </label>
          <label className="grid gap-1 text-sm">ComfyUI 图（API Format JSON；绝不外传到租户或运维目录，仅用于编辑与发布）
            <textarea className="min-h-48 w-full rounded-lg border border-input bg-transparent px-2.5 py-1.5 font-mono text-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50" value={graphText} onChange={(e) => setGraphText(e.target.value)} />
          </label>
          <div className="space-y-2"><h3 className="font-medium">输入</h3>
            {draft.inputs.map((input, index) => <div key={index} className="grid gap-2 rounded bg-muted/30 p-3 md:grid-cols-3">
              <label className="grid gap-1 text-xs">名称<Input value={input.name} onChange={(e) => updateInput(index, { ...input, name: e.target.value })} /></label>
              <label className="grid gap-1 text-xs">节点<Input value={input.node} onChange={(e) => updateInput(index, { ...input, node: e.target.value })} /></label>
              <label className="grid gap-1 text-xs">字段<Input value={input.field} onChange={(e) => updateInput(index, { ...input, field: e.target.value })} /></label>
              <label className="grid gap-1 text-xs">类型<select className="h-8 rounded-lg border border-input bg-transparent px-2 text-sm" value={input.type} onChange={(e) => updateInput(index, { ...input, type: e.target.value as TemplateInput["type"] })}><option value="string">string</option><option value="integer">integer</option><option value="number">number</option><option value="boolean">boolean</option></select></label>
              <label className="flex items-center gap-2 text-xs"><input type="checkbox" checked={input.required} onChange={(e) => updateInput(index, { ...input, required: e.target.checked })} /> 必填</label>
              <Button variant="outline" onClick={() => setDraft({ ...draft, inputs: draft.inputs.filter((_, i) => i !== index) })}>删除输入</Button>
            </div>)}
            <Button variant="outline" disabled={draft.inputs.length >= 100} onClick={() => setDraft({ ...draft, inputs: [...draft.inputs, { name: "", node: "", field: "", type: "string", required: false }] })}>添加输入</Button>
          </div>
          <div className="space-y-2"><h3 className="font-medium">输出（声明产出的节点，仅结构性校验节点是否存在）</h3>
            {(draft.outputs ?? []).map((output, index) => <div key={index} className="grid gap-2 rounded bg-muted/30 p-3 md:grid-cols-3">
              <label className="grid gap-1 text-xs">名称<Input value={output.name} onChange={(e) => updateOutput(index, { ...output, name: e.target.value })} /></label>
              <label className="grid gap-1 text-xs">节点<Input value={output.node} onChange={(e) => updateOutput(index, { ...output, node: e.target.value })} /></label>
              <label className="grid gap-1 text-xs">种类（如 image/video）<Input value={output.type} onChange={(e) => updateOutput(index, { ...output, type: e.target.value })} /></label>
              <Button variant="outline" onClick={() => setDraft({ ...draft, outputs: (draft.outputs ?? []).filter((_, i) => i !== index) })}>删除输出</Button>
            </div>)}
            <Button variant="outline" disabled={(draft.outputs ?? []).length >= 20} onClick={() => setDraft({ ...draft, outputs: [...(draft.outputs ?? []), { name: "", node: "", type: "" }] })}>添加输出</Button>
          </div>
          <div className="grid gap-3 md:grid-cols-2">
            <label className="grid gap-1 text-sm">自定义节点依赖（每行 名称[,版本]）<textarea className="min-h-20 w-full rounded-lg border border-input bg-transparent px-2.5 py-1.5 font-mono text-xs outline-none" value={(draft.dependencies?.custom_nodes ?? []).map((d) => d.version ? `${d.name},${d.version}` : d.name).join("\n")} onChange={(e) => setDraft({ ...draft, dependencies: { ...draft.dependencies, custom_nodes: e.target.value.split("\n").map((line) => line.trim()).filter(Boolean).map((line) => { const [name, version] = line.split(","); return version ? { name: name.trim(), version: version.trim() } : { name: name.trim() }; }) } })} /></label>
            <label className="grid gap-1 text-sm">模型依赖（每行 名称[,版本]）<textarea className="min-h-20 w-full rounded-lg border border-input bg-transparent px-2.5 py-1.5 font-mono text-xs outline-none" value={(draft.dependencies?.models ?? []).map((d) => d.version ? `${d.name},${d.version}` : d.name).join("\n")} onChange={(e) => setDraft({ ...draft, dependencies: { ...draft.dependencies, models: e.target.value.split("\n").map((line) => line.trim()).filter(Boolean).map((line) => { const [name, version] = line.split(","); return version ? { name: name.trim(), version: version.trim() } : { name: name.trim() }; }) } })} /></label>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => void action(async () => { setVerdict(null); setVerdict(await call({ method: "POST", surface: "operator", path: `${base}/${encodeURIComponent(templateID)}/validate`, body: { content: currentContentForValidation(), visible_tenant_ids: parseVisibleTenantIDs(visibleTenantsText.split(",").map((s) => s.trim()).filter(Boolean)) }, parse: parseTemplateValidation })); })}>验证草稿</Button>
            <Button disabled={!verdict} onClick={() => setConfirm("publish")}>{current ? "发布新版本" : "创建并发布"}</Button>
          </div>
        </fieldset>
        {verdict && <p className="break-all text-sm">验证通过：{verdict}。尚未发布。</p>}
      </section>
      <section className="space-y-3 rounded-lg border p-4"><div className="flex items-center justify-between"><h2 className="font-semibold">历史与比较</h2><Button variant="outline" disabled={busy} onClick={() => void action(() => loadHistory())}>最新版本</Button></div>
        {history.items.length === 0 && <p className="text-sm text-muted-foreground">暂无已读取的历史版本。</p>}
        <div className="flex flex-wrap gap-2">{history.items.map((item) => <Button key={item.revision} variant={selected?.revision === item.revision ? "default" : "outline"} disabled={busy} onClick={() => void action(async () => setSelected(await call({ method: "GET", surface: "operator", path: `${base}/${encodeURIComponent(templateID)}/revisions/${item.revision}`, parse: parseTemplateSnapshot })))}>v{item.revision} · {item.created_at}{item.rollback_of ? ` · 回滚自 v${item.rollback_of}` : ""}</Button>)}</div>
        {history.next_before && <Button variant="outline" disabled={busy} onClick={() => void action(() => loadHistory(history.next_before))}>更早版本</Button>}
        {selected && <><div className="flex flex-wrap items-center gap-3"><p>选中 v{selected.revision} · {selected.actor_id}</p><label className="text-sm"><input type="checkbox" checked={compareDraft} onChange={(e) => setCompareDraft(e.target.checked)} /> 与草稿比较（取消勾选：当前发布）</label><Button variant="outline" disabled={busy || !current} onClick={() => setConfirm("rollback")}>回滚到 v{selected.revision}</Button></div>
          <div className="grid gap-3 md:grid-cols-2"><div><h3>历史 v{selected.revision}</h3><pre className="max-h-96 overflow-auto rounded bg-muted p-3 text-xs">{JSON.stringify({ description: selected.description, inputs: selected.inputs, outputs: selected.outputs, dependencies: selected.dependencies, visible_tenant_ids: selected.visible_tenant_ids, graph: selected.graph }, null, 2)}</pre></div><div><h3>{compareDraft ? "本地草稿" : `当前 v${current?.revision ?? "—"}`}</h3><pre className="max-h-96 overflow-auto rounded bg-muted p-3 text-xs">{JSON.stringify(compareDraft ? draft : current, null, 2)}</pre></div></div></>}
      </section>
      <section className="space-y-3 rounded-lg border p-4"><div className="flex items-center justify-between"><h2 className="font-semibold">副本应用状态（整包）</h2><Button variant="outline" disabled={busy} onClick={() => void action(refreshStatus)}>实时刷新</Button></div>
        <p>{statusError ? "无法确认副本状态，未完成。" : status ? status.complete ? "本次检查：全部副本已应用期望整包。" : "尚未确认全部副本应用；缺少机群配置、连接失败或整包不一致均不算完成。" : "尚无实时确认。"}</p>
        {status && <><p className="break-all text-xs">期望 {status.desired_template_count} 个模板 · {status.desired_bundle_digest} · 检查 {status.checked_at}</p><div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr><th>副本 / 地址</th><th>模式</th><th>数量 / 摘要</th><th>应用 / 检查时间</th><th>观测</th></tr></thead><tbody>{status.replicas.map((row, i) => <tr key={`${row.endpoint}-${i}`} className="border-t"><td className="p-2">{row.replica_id ?? "未知"}<br />{row.endpoint}</td><td>{row.mode ?? "未知"}</td><td className="max-w-xs break-all">{row.template_count ?? "未知"}<br />{row.bundle_digest ?? "无摘要"}</td><td>{row.applied_at ?? "未知"}<br />{row.checked_at ?? row.generated_at ?? "未知"}</td><td>{row.error ? "读取失败" : row.mode === "controlplane" && row.template_count === status.desired_template_count && row.bundle_digest === status.desired_bundle_digest ? "匹配" : "未匹配"}</td></tr>)}</tbody></table></div></>}
      </section>
    </>}
    <ConfirmDialog open={confirm !== null} onOpenChange={(open) => { if (!open) setConfirm(null); }} title={confirm === "publish" ? (current ? `发布模板 ${templateID} 的新版本` : `创建模板 ${templateID}`) : confirm === "rollback" ? `回滚到版本 ${selected?.revision}` : "替换本地草稿"} description={confirm === "replace" ? "当前发布内容将替换本地草稿，未发布的修改会丢失。" : `以当前版本 ${current?.revision ?? 0} 为基础创建新版本。Gateway 拉取后生效；请检查每个副本状态。${confirm === "rollback" ? "本地草稿会保留。" : ""}`} onConfirm={write} />
  </div>;
}

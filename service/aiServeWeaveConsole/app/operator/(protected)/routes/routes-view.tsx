"use client";

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { createRouteReadGate } from "@/lib/console/route-read-gate";
import { ApiError, describe } from "@/lib/console/errors";
import { importRouteFile, parseRoutes, parseRouteSnapshot, parseRouteHistory, parseRouteStatus, parseRouteValidation, routeWriteMessage, type ModelRoute, type RouteSnapshot, type RouteHistory, type RouteStatus } from "@/lib/console/model-routes";

const path = "/operator/v1/routes";

/** RoutesView edits local drafts and explicitly publishes immutable revisions. / RoutesView 编辑本地草稿并显式发布不可变版本。 */
export function RoutesView() {
  const call = useConsoleRequest();
  const reads = React.useRef(createRouteReadGate());
  const draftInitialized = React.useRef(false);
  const [current, setCurrent] = React.useState<RouteSnapshot | null>(null);
  const [draft, setDraft] = React.useState<ModelRoute[]>([]);
  const [history, setHistory] = React.useState<RouteHistory>({ items: [] });
  const [selected, setSelected] = React.useState<RouteSnapshot | null>(null);
  const [status, setStatus] = React.useState<RouteStatus | null>(null);
  const [error, setError] = React.useState<string | null>(null);
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
    }).catch((e) => { if (acceptCurrent()) setError(describe(e)); });
    call({ method: "GET", surface: "operator", path: `${path}/history`, query: { limit: "20" }, parse: parseRouteHistory }).then((value) => { if (acceptHistory()) setHistory(value); }).catch((e) => { if (acceptHistory()) setError(describe(e)); });
    call({ method: "GET", surface: "operator", path: `${path}/status`, parse: parseRouteStatus }).then((value) => { if (acceptStatus()) setStatus(value); }).catch(() => { if (acceptStatus()) setStatusError(true); });
    return () => gate.invalidate();
  }, [call]);

  function edit(next: ModelRoute[]) { setDraft(next); setVerdict(null); }
  function routeChange(index: number, route: ModelRoute) { edit(draft.map((item, i) => i === index ? route : item)); }
  async function action(work: () => Promise<void>) {
    setBusy(true); setError(null);
    try { await work(); } catch (e) { setError(describe(e)); } finally { setBusy(false); }
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
      void loadHistory().catch((e) => setError(describe(e)));
    } catch (e) {
      const message = routeWriteMessage(e); setError(message); throw new Error(message);
    } finally { setBusy(false); }
  }

  return <div className="space-y-6">
    <div><h1 className="text-2xl font-semibold">模型路由</h1><p className="text-sm text-muted-foreground">别名、运行时模型与节点选择器。草稿仅保留在当前页面，离开页面会丢失。验证不会发布。</p></div>
    {error && <p role="alert" className="text-destructive">{error}</p>}
    <section className="space-y-3 rounded-lg border p-4">
      <h2 className="font-semibold">当前发布：{current ? current.revision === 0 ? "尚未发布" : `版本 ${current.revision}` : "读取中"}</h2>
      {current && <p className="break-all text-xs text-muted-foreground">{current.digest || "无摘要"} · {current.revision > 0 ? current.created_at : "无发布时间"}</p>}
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" disabled={busy} onClick={() => action(refreshCurrent)}>重新读取当前版本（保留草稿）</Button>
        <Button variant="outline" disabled={busy || !current} onClick={() => setConfirm("replace")}>用当前版本替换草稿</Button>
      </div>
    </section>
    <section className="space-y-4 rounded-lg border p-4">
      <h2 className="font-semibold">本地草稿 · {draft.length} 个别名</h2>
      <fieldset disabled={busy || !current} className="space-y-4">
        <label className="grid gap-1 text-sm">导入已有 JSON 文件（每个及合并后最多 1 MiB；替换草稿）<input type="file" accept=".json,application/json" multiple onChange={(event) => {
          const files = Array.from(event.target.files ?? []); event.target.value = "";
          if (files.length) void action(async () => {
            const imported: ModelRoute[] = []; let bytes = 0;
            for (const file of files) { bytes += file.size; if (bytes > (1 << 20)) throw new ApiError("invalid"); imported.push(...await importRouteFile(file)); }
            edit(parseRoutes(imported));
          });
        }} /></label>
        {draft.map((route, index) => <div key={index} className="space-y-3 rounded-md border p-3">
          <div className="flex items-end gap-2"><label className="grid flex-1 gap-1 text-sm">模型别名<Input value={route.model} onChange={(e) => routeChange(index, { ...route, model: e.target.value })} /></label><Button variant="outline" onClick={() => edit(draft.filter((_, i) => i !== index))}>删除别名</Button></div>
          {route.targets.map((target, targetIndex) => {
            const update = (next: typeof target) => routeChange(index, { ...route, targets: route.targets.map((item, i) => i === targetIndex ? next : item) });
            return <div key={targetIndex} className="space-y-2 rounded bg-muted/30 p-3">
              <div className="grid gap-2 md:grid-cols-3"><label className="grid gap-1 text-sm">运行时模型<Input value={target.runtime_model} onChange={(e) => update({ ...target, runtime_model: e.target.value })} /></label><label className="grid gap-1 text-sm">优先级（小值优先）<Input type="number" step="1" value={target.priority ?? 0} onChange={(e) => update({ ...target, priority: Number(e.target.value) })} /></label><label className="grid gap-1 text-sm">权重（0 等同 1）<Input type="number" min="0" step="1" value={target.weight ?? 0} onChange={(e) => update({ ...target, weight: Number(e.target.value) })} /></label></div>
              <p className="text-sm">节点选择器（所有标签须同时匹配；空选择器匹配所有节点）</p>
              {Object.entries(target.node_selector ?? {}).map(([key, value], selectorIndex) => <div className="flex gap-2" key={selectorIndex}><Input aria-label="标签名" value={key} onChange={(e) => { if (e.target.value !== key && Object.hasOwn(target.node_selector ?? {}, e.target.value)) { setError("标签名不能重复，原标签已保留。"); return; } update({ ...target, node_selector: Object.fromEntries(Object.entries(target.node_selector ?? {}).map((entry, i) => i === selectorIndex ? [e.target.value, value] : entry)) }); }} /><Input aria-label="标签值" value={value} onChange={(e) => update({ ...target, node_selector: { ...target.node_selector, [key]: e.target.value } })} /><Button variant="outline" onClick={() => update({ ...target, node_selector: Object.fromEntries(Object.entries(target.node_selector ?? {}).filter(([name]) => name !== key)) })}>删除标签</Button></div>)}
              <div className="flex gap-2"><Button variant="outline" onClick={() => { let key = "label"; while (Object.hasOwn(target.node_selector ?? {}, key)) key += "_"; update({ ...target, node_selector: { ...target.node_selector, [key]: "" } }); }}>添加标签</Button><Button variant="outline" onClick={() => routeChange(index, { ...route, targets: route.targets.filter((_, i) => i !== targetIndex) })}>删除目标</Button></div>
            </div>;
          })}
          <Button variant="outline" disabled={route.targets.length >= 100} onClick={() => routeChange(index, { ...route, targets: [...route.targets, { runtime_model: "", priority: 0, weight: 0 }] })}>添加目标</Button>
        </div>)}
        <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={draft.length >= 1000} onClick={() => edit([...draft, { model: "", targets: [{ runtime_model: "" }] }])}>添加别名</Button><Button variant="outline" onClick={() => action(async () => { setVerdict(null); setVerdict(await call({ method: "POST", surface: "operator", path: `${path}/validate`, body: { routes: parseRoutes(draft) }, parse: parseRouteValidation })); })}>验证草稿</Button><Button disabled={!verdict} onClick={() => setConfirm("publish")}>发布草稿</Button></div>
      </fieldset>
      {verdict && <p className="break-all text-sm">验证通过：{verdict}。尚未发布。</p>}
    </section>
    <section className="space-y-3 rounded-lg border p-4"><div className="flex items-center justify-between"><h2 className="font-semibold">历史与比较</h2><Button variant="outline" disabled={busy} onClick={() => action(() => loadHistory())}>最新版本</Button></div>
      {history.items.length === 0 && <p className="text-sm text-muted-foreground">暂无已读取的历史版本。</p>}
      <div className="flex flex-wrap gap-2">{history.items.map((item) => <Button key={item.revision} variant={selected?.revision === item.revision ? "default" : "outline"} disabled={busy} onClick={() => action(async () => setSelected(await call({ method: "GET", surface: "operator", path: `${path}/revisions/${item.revision}`, parse: parseRouteSnapshot })))}>v{item.revision} · {item.created_at}{item.rollback_of ? ` · 回滚自 v${item.rollback_of}` : ""}</Button>)}</div>
      {history.next_before && <Button variant="outline" disabled={busy} onClick={() => action(() => loadHistory(history.next_before))}>更早版本</Button>}
      {selected && <><div className="flex flex-wrap items-center gap-3"><p>选中 v{selected.revision} · {selected.actor_id}</p><label className="text-sm"><input type="checkbox" checked={compareDraft} onChange={(e) => setCompareDraft(e.target.checked)} /> 与草稿比较（取消勾选：当前发布）</label><Button variant="outline" disabled={busy || !current} onClick={() => setConfirm("rollback")}>回滚到 v{selected.revision}</Button></div><div className="grid gap-3 md:grid-cols-2"><div><h3>历史 v{selected.revision}</h3><pre className="max-h-96 overflow-auto rounded bg-muted p-3 text-xs">{JSON.stringify(selected.routes, null, 2)}</pre></div><div><h3>{compareDraft ? "本地草稿" : `当前 v${current?.revision ?? "—"}`}</h3><pre className="max-h-96 overflow-auto rounded bg-muted p-3 text-xs">{JSON.stringify(compareDraft ? draft : current?.routes, null, 2)}</pre></div></div></>}
    </section>
    <section className="space-y-3 rounded-lg border p-4"><div className="flex items-center justify-between"><h2 className="font-semibold">副本应用状态</h2><Button variant="outline" disabled={busy} onClick={() => action(refreshStatus)}>实时刷新</Button></div>
      <p>{statusError ? "无法确认副本状态，未完成。" : status ? status.complete && current?.revision === status.desired_revision && current.digest === status.desired_digest ? "本次检查：全部副本已应用期望版本。" : "尚未确认全部副本应用；缺少机群配置、连接失败或版本不一致均不算完成。" : "尚无实时确认。"}</p>
      {status && <><p className="break-all text-xs">期望 v{status.desired_revision} · {status.desired_digest} · 检查 {status.checked_at}</p><div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr><th>副本 / 地址</th><th>模式</th><th>版本 / 摘要</th><th>应用 / 检查时间</th><th>观测</th></tr></thead><tbody>{status.replicas.map((row, i) => <tr key={`${row.endpoint}-${i}`} className="border-t"><td className="p-2">{row.replica_id ?? "未知"}<br />{row.endpoint}</td><td>{row.mode ?? "未知"}</td><td className="max-w-xs break-all">{row.revision ?? "未知"}<br />{row.digest ?? "无摘要"}</td><td>{row.applied_at ?? "未知"}<br />{row.checked_at ?? row.generated_at ?? "未知"}</td><td>{row.error ? "读取失败" : row.mode === "controlplane" && row.revision === status.desired_revision && row.digest === status.desired_digest ? "匹配" : "未匹配"}</td></tr>)}</tbody></table></div></>}
    </section>
    <ConfirmDialog open={confirm !== null} onOpenChange={(open) => { if (!open) setConfirm(null); }} title={confirm === "publish" ? "发布路由草稿" : confirm === "rollback" ? `回滚到版本 ${selected?.revision}` : "替换本地草稿"} description={confirm === "replace" ? "当前发布内容将替换本地草稿，未发布的修改会丢失。" : `以当前版本 ${current?.revision} 为基础创建新版本。Gateway 拉取后生效；请检查每个副本状态。${confirm === "rollback" ? "本地草稿会保留。" : ""}`} onConfirm={write} />
  </div>;
}

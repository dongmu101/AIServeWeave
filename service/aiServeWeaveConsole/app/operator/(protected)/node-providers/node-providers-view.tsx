"use client";

import * as React from "react";
import { useSearchParams } from "next/navigation";

import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { NodeProvidersPanel } from "@/components/console/node-providers-panel";
import { FormError } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { tenantNodeOwner } from "@/lib/console/node-access";
import { parseNodeStates } from "@/lib/console/node-ops";
import { nodePathSegment } from "@/lib/console/node-path";
import type { MessageKey } from "@/lib/i18n/messages";

/** NODE_QUERY_PARAM preselects a node, e.g. from the fleet page's per-node link.
 * NODE_QUERY_PARAM 用于预选节点，例如来自节点管理页每台节点上的链接。 */
export const NODE_QUERY_PARAM = "node";

/** checkNodeId trims raw and accepts only path-safe platform node ids.
 * checkNodeId 去除首尾空白，只接受可安全放进路径的平台节点 id。 */
function checkNodeId(raw: string): { nodeId: string; problem?: undefined } | { nodeId?: undefined; problem: MessageKey } {
  const id = raw.trim();
  if (!nodePathSegment(id)) return { problem: "operator.nodeIdIsRequiredAndCannotContainPath" };
  if (tenantNodeOwner(id) !== null) return { problem: "operator.nodeProvidersTenantNode" };
  return { nodeId: id };
}

/** NodeProvidersView picks one platform node, from the Registry list or typed
 * by hand, and shows its provider panel.
 * NodeProvidersView 从 Registry 列表选择或手动输入一台平台节点，并展示其供应商面板。 */
export function NodeProvidersView() {
  const { t } = useI18n();
  const params = useSearchParams();
  const registry = useResource({ method: "GET", surface: "operator", path: "/operator/v1/nodes/states", parse: parseNodeStates });
  // Tenant nodes are managed by their tenant, so they are never offered here.
  // 商户节点由商户自己管理，这里不提供选择。
  const platformNodes = React.useMemo(
    () => (registry.data ?? []).map((node) => node.nodeId).filter((id) => tenantNodeOwner(id) === null && nodePathSegment(id)).sort(),
    [registry.data],
  );
  // A ?node= preselection is checked like typed input, once, on first render.
  // ?node= 预选值与手动输入同样校验，仅在首次渲染时读取一次。
  const [initial] = React.useState(() => { const id = params.get(NODE_QUERY_PARAM); return id ? checkNodeId(id) : null; });
  const [input, setInput] = React.useState("");
  const [nodeId, setNodeId] = React.useState<string | null>(initial?.nodeId ?? null);
  const [error, setError] = React.useState<React.ReactNode>(initial?.problem ? <Message id={initial.problem} /> : null);

  function choose(raw: string) {
    const result = checkNodeId(raw);
    setError(result.problem ? <Message id={result.problem} /> : null);
    if (result.nodeId) setNodeId(result.nodeId);
  }

  function lookup(event: React.FormEvent) {
    event.preventDefault();
    choose(input);
  }

  return <div className="grid max-w-4xl gap-6">
    <div>
      <h1 className="font-heading text-xl font-semibold tracking-tight"><Message id="operator.nodeProviders" /></h1>
      <p className="mt-2 text-sm text-muted-foreground"><Message id="operator.nodeProvidersIntro" /></p>
    </div>
    {platformNodes.length > 0 ? <label className="grid gap-1 text-sm" htmlFor="node-providers-pick"><Message id="operator.nodeProvidersPick" />
      <select id="node-providers-pick" className="h-9 w-full max-w-xl rounded-md border bg-background px-3 font-mono" value={nodeId && platformNodes.includes(nodeId) ? nodeId : ""} onChange={(e) => { if (e.target.value) choose(e.target.value); }}>
        <option value="">{t("operator.nodeProvidersPickPlaceholder")}</option>
        {platformNodes.map((id) => <option key={id} value={id}>{id}</option>)}
      </select>
    </label> : null}
    <form onSubmit={lookup} className="flex flex-wrap items-end gap-2">
      <label className="grid gap-1 text-sm" htmlFor="node-providers-id"><Message id={platformNodes.length > 0 ? "operator.nodeProvidersManual" : "operator.nodeProvidersNodeId"} />
        <Input id="node-providers-id" className="w-80" value={input} onChange={(e) => setInput(e.target.value)} />
      </label>
      <Button type="submit" variant={platformNodes.length > 0 ? "outline" : "default"}><Message id="operator.nodeProvidersLookup" /></Button>
    </form>
    <FormError message={error} />
    {nodeId ? <section className="grid gap-3 rounded-xl border bg-card p-4">
      <code className="break-all text-sm">{nodeId}</code>
      <NodeProvidersPanel key={nodeId} surface="operator" nodeId={nodeId} canManage />
    </section> : null}
  </div>;
}

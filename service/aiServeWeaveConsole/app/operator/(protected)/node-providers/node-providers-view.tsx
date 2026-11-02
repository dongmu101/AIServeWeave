"use client";

import * as React from "react";

import { Message } from "@/components/i18n/message";
import { NodeProvidersPanel } from "@/components/console/node-providers-panel";
import { FormError } from "@/components/console/states";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { tenantNodeOwner } from "@/lib/console/node-access";
import { nodePathSegment } from "@/lib/console/node-path";

/** NodeProvidersView looks up one node by id and shows its provider panel.
 * NodeProvidersView 按 id 查询一台节点并展示其供应商面板。 */
export function NodeProvidersView() {
  const [input, setInput] = React.useState("");
  const [nodeId, setNodeId] = React.useState<string | null>(null);
  const [error, setError] = React.useState<React.ReactNode>(null);

  function lookup(event: React.FormEvent) {
    event.preventDefault();
    const id = input.trim();
    if (!nodePathSegment(id)) { setError(<Message id="operator.nodeIdIsRequiredAndCannotContainPath" />); return; }
    if (tenantNodeOwner(id) !== null) { setError(<Message id="operator.nodeProvidersTenantNode" />); return; }
    setError(null);
    setNodeId(id);
  }

  return <div className="grid max-w-4xl gap-6">
    <div>
      <h1 className="font-heading text-lg font-semibold"><Message id="operator.nodeProviders" /></h1>
      <p className="mt-2 text-sm text-muted-foreground"><Message id="operator.nodeProvidersIntro" /></p>
    </div>
    <form onSubmit={lookup} className="flex flex-wrap items-end gap-2">
      <label className="grid gap-1 text-sm" htmlFor="node-providers-id"><Message id="operator.nodeProvidersNodeId" />
        <Input id="node-providers-id" className="w-80" value={input} onChange={(e) => setInput(e.target.value)} />
      </label>
      <Button type="submit"><Message id="operator.nodeProvidersLookup" /></Button>
    </form>
    <FormError message={error} />
    {nodeId ? <section className="grid gap-3 rounded-xl border p-4">
      <code className="break-all text-sm">{nodeId}</code>
      <NodeProvidersPanel key={nodeId} surface="operator" nodeId={nodeId} canManage />
    </section> : null}
  </div>;
}

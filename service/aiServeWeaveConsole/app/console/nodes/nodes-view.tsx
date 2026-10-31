"use client";

import * as React from "react";
import { toast } from "sonner";

import { ErrorMessage } from "@/components/i18n/error-message";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { EmptyState, ErrorState, FormError, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { EnrollmentBundle } from "@/lib/console/agent-enrollment";
import { parseNoContent } from "@/lib/console/contract";
import { ApiError } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";
import { canPurchaseCredits } from "@/lib/console/permissions";
import { activeNodeCount, parseTenantNodeBundle, parseTenantNodeList, type TenantNode, type TenantNodeStatus } from "@/lib/console/tenant-nodes";
import type { MessageKey } from "@/lib/i18n/messages";

const STATUS_LABEL: Record<TenantNodeStatus, MessageKey> = {
  online: "tenant.nodeStatusOnline",
  offline: "tenant.nodeStatusOffline",
  unknown: "tenant.nodeStatusUnknown",
  revoked: "tenant.nodeStatusRevoked",
};

/**
 * NodesView lists the tenant's own nodes and issues enrollment bundles. A
 * bundle holds a one-time credential, so it lives only in this component's
 * memory and is never retried: a second request would mint a second token.
 *
 * NodesView 列出商户的自有节点并签发接入包。接入包含一次性凭据，因此只保存在本组件的
 * 内存中，且从不重试：第二次请求会铸出第二枚令牌。
 */
export function NodesView({ role }: { role: string }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const nodes = useResource({ method: "GET", path: "/admin/v1/nodes", parse: parseTenantNodeList });
  const canManage = canPurchaseCredits(role);
  const [ttl, setTTL] = React.useState("60");
  const [pending, setPending] = React.useState(false);
  const inFlight = React.useRef(false);
  const [error, setError] = React.useState<unknown>(null);
  const [bundle, setBundle] = React.useState<EnrollmentBundle | null>(null);

  async function generate(event: React.FormEvent) {
    event.preventDefault();
    if (inFlight.current) return;
    inFlight.current = true;
    setPending(true); setError(null); setBundle(null);
    try {
      setBundle(await run({ method: "POST", path: "/admin/v1/nodes", body: { ttl_minutes: Number(ttl) }, parse: parseTenantNodeBundle }));
      nodes.reload();
    } catch (failure) { setError(failure); }
    finally { inFlight.current = false; setPending(false); }
  }

  if (nodes.errorKind === "not_found") {
    return <EmptyState title={t("tenant.nodesDisabled")} description={t("tenant.nodesDisabledDescription")} />;
  }
  const data = nodes.data;
  const full = data !== null && data !== undefined && activeNodeCount(data) >= data.maxPerTenant;

  return <div className="grid max-w-4xl gap-6">
    <div>
      <h1 className="font-heading text-lg font-semibold"><Message id="tenant.myNodes" /></h1>
      <p className="mt-2 text-sm text-muted-foreground"><Message id="tenant.myNodesIntro" /></p>
    </div>
    {nodes.error ? <ErrorState message={nodes.error} onRetry={nodes.reload} /> : !data ? <LoadingState rows={3} /> : <>
      {canManage ? <form onSubmit={generate} className="grid gap-4 rounded-xl border p-4" aria-labelledby="node-bundle-title">
        <h2 id="node-bundle-title" className="font-semibold"><Message id="tenant.addNode" /></h2>
        <p className="text-sm text-muted-foreground"><Message id="tenant.nodeLimits" values={{ active: activeNodeCount(data), max: data.maxPerTenant, daily: data.maxCreatesPerDay }} /></p>
        <label className="grid gap-2 text-sm" htmlFor="node-ttl"><Message id="operator.enrollmentTTL" /><select id="node-ttl" className="h-9 w-fit rounded-md border bg-background px-3" value={ttl} onChange={(e) => setTTL(e.target.value)} disabled={pending}><option value="15">{t("operator.enrollment15Minutes")}</option><option value="60">{t("operator.enrollment1Hour")}</option><option value="1440">{t("operator.enrollment24Hours")}</option></select></label>
        <Button type="submit" disabled={pending || full} aria-busy={pending} className="w-fit"><Message id={pending ? "operator.enrollmentGenerating" : "operator.enrollmentGenerate"} /></Button>
        {full ? <p className="text-sm text-muted-foreground"><Message id="tenant.nodeLimitReached" /></p> : null}
        {error ? <><FormError message={error instanceof ApiError && error.kind === "conflict" ? <Message id="tenant.nodeLimitReached" /> : <ErrorMessage error={error} />} /><p className="text-sm text-muted-foreground"><Message id="operator.enrollmentUncertain" /></p></> : null}
      </form> : <p className="text-sm text-muted-foreground"><Message id="tenant.nodesReadOnly" /></p>}
      {bundle ? <section className="grid gap-3 rounded-xl border p-4" aria-live="polite">
        <p className="font-semibold"><Message id="operator.enrollmentReady" /></p>
        <code className="break-all text-sm">{bundle.nodeId}</code>
        <p className="text-sm"><Message id="operator.enrollmentExpires" values={{ value: formatDateTime(bundle.expiresAt, undefined, locale) }} /></p>
        <Button className="w-fit" onClick={() => downloadFile(new Blob([bundle.bytes], { type: "application/zip" }), bundle.filename)}><Message id="operator.enrollmentDownloadBundle" /></Button>
        <p className="text-sm text-muted-foreground"><Message id="operator.enrollmentPrivate" /></p>
        <p className="text-sm text-muted-foreground"><Message id="tenant.nodeInstallHint" /></p>
      </section> : null}
      {data.items.length === 0 ? <EmptyState title={t("tenant.noNodes")} description={t("tenant.noNodesDescription")} /> : <ul className="grid gap-3">
        {data.items.map((node) => <li key={node.nodeId}><NodeCard node={node} canManage={canManage} onDeleted={nodes.reload} /></li>)}
      </ul>}
    </>}
  </div>;
}

/** NodeCard shows one node and, for owner and admin, its delete action. / NodeCard 展示一台节点，并为 owner 与 admin 提供删除操作。 */
function NodeCard({ node, canManage, onDeleted }: { node: TenantNode; canManage: boolean; onDeleted: () => void }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);

  async function remove() {
    if (!window.confirm(t("tenant.deleteNodeConfirm"))) return;
    setPending(true); setError(null);
    try {
      await run({ method: "DELETE", path: `/admin/v1/nodes/${encodeURIComponent(node.nodeId)}`, parse: parseNoContent });
      toast.success(<Message id="tenant.nodeDeleted" />);
    } catch (failure) {
      // 404 means it is already deleted or not this tenant's; re-read either way.
      // 404 表示它已被删除或不属于本商户；无论哪种都重新读取。
      if (!(failure instanceof ApiError && failure.kind === "not_found")) {
        setError(failure);
        setPending(false);
        return;
      }
    }
    setPending(false);
    onDeleted();
  }

  const revoked = node.status === "revoked";
  return <Card><CardHeader><CardTitle className="flex flex-wrap items-center gap-2 text-base">
    <code className="break-all text-sm">{node.nodeId}</code>
    <Badge variant={node.status === "online" ? "default" : node.status === "offline" ? "destructive" : "outline"}><Message id={STATUS_LABEL[node.status]} /></Badge>
  </CardTitle></CardHeader><CardContent className="grid gap-2 text-sm">
    <p className="text-muted-foreground"><Message id="tenant.nodeCreatedAt" values={{ time: formatDateTime(node.createdAt, undefined, locale) }} /></p>
    {revoked && node.revokedAt ? <p className="text-muted-foreground"><Message id="tenant.nodeDeletedAt" values={{ time: formatDateTime(node.revokedAt, undefined, locale) }} /></p> : null}
    {!revoked && node.lastHeartbeat === null ? <p className="text-muted-foreground"><Message id="tenant.nodeEnrollBy" values={{ time: formatDateTime(node.enrollBy, undefined, locale) }} /></p> : null}
    {node.lastHeartbeat ? <p className="text-muted-foreground"><Message id="tenant.nodeLastHeartbeat" values={{ time: formatDateTime(node.lastHeartbeat, undefined, locale) }} /></p> : null}
    {node.agentVersion ? <p className="text-muted-foreground"><Message id="tenant.nodeAgentVersion" values={{ version: node.agentVersion }} /></p> : null}
    {canManage && !revoked ? <Button variant="destructive" size="sm" className="w-fit" disabled={pending} onClick={remove}><Message id="tenant.deleteNode" /></Button> : null}
    {error ? <FormError message={<ErrorMessage error={error} />} /> : null}
  </CardContent></Card>;
}

function downloadFile(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url; anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click(); anchor.remove();
  // Release after the browser has begun consuming the download. / 浏览器开始读取下载后释放。
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

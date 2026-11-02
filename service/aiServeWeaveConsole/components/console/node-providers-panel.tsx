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
import { Input } from "@/components/ui/input";
import { ApiError } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";
import {
  HOSTED_KINDS,
  REMOTE_ID_PREFIX,
  checkProviderDraft,
  deleteNodeProviderRequest,
  nodeProvidersRequest,
  putNodeProviderRequest,
  type NodeProvider,
  type NodeSurface,
  type ProviderDraft,
  type ProviderOutcome,
  type ProviderProblem,
  type ProviderState,
} from "@/lib/console/node-providers";
import type { MessageKey } from "@/lib/i18n/messages";

const STATE_LABEL: Record<ProviderState, MessageKey> = {
  "": "tenant.providerStatePending",
  active: "tenant.providerStateActive",
  conflict: "tenant.providerStateConflict",
  failed: "tenant.providerStateFailed",
  unknown: "tenant.providerStateUnknown",
};

const OUTCOME_LABEL: Record<ProviderOutcome, MessageKey> = {
  none: "tenant.providerOutcomeNone",
  applied: "tenant.providerOutcomeApplied",
  disabled: "tenant.providerOutcomeDisabled",
  open_failed: "tenant.providerOutcomeOpenFailed",
  invalid: "tenant.providerOutcomeInvalid",
  stale: "tenant.providerOutcomeStale",
  store_failed: "tenant.providerOutcomeStoreFailed",
};

const PROBLEM_LABEL: Record<ProviderProblem, MessageKey> = {
  id: "tenant.providerProblemId",
  kind: "tenant.providerProblemKind",
  base_url: "tenant.providerProblemBaseUrl",
  key: "tenant.providerProblemKey",
  key_required: "tenant.providerProblemKeyRequired",
  models: "tenant.providerProblemModels",
};

const EMPTY_DRAFT: ProviderDraft = { id: REMOTE_ID_PREFIX, kind: "openai", baseUrl: "", apiKey: "", models: "" };

/**
 * NodeProvidersPanel lists and edits the hosted providers provisioned on one
 * node, for the tenant routes or the operator routes. The key field is
 * write-only and cleared after every attempt; writes are sent once and never
 * retried, like every other write in the Console.
 *
 * NodeProvidersPanel 列出并编辑下发到一台节点的托管供应商，可用于商户路由或运维路由。
 * Key 字段只写不读，每次尝试后清空；写操作与 Console 其他写操作一样只发送一次、从不重试。
 */
export function NodeProvidersPanel({ surface, nodeId, canManage }: { surface: NodeSurface; nodeId: string; canManage: boolean }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const providers = useResource(nodeProvidersRequest(surface, nodeId));
  const [draft, setDraft] = React.useState<ProviderDraft | null>(null);
  const [saved, setSaved] = React.useState<NodeProvider | null>(null);
  const [pending, setPending] = React.useState(false);
  const inFlight = React.useRef(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  function edit(provider: NodeProvider | null) {
    setSaved(provider);
    setError(null);
    setDraft(provider ? { id: provider.id, kind: provider.kind, baseUrl: provider.baseUrl, apiKey: "", models: provider.models.join(", ") } : EMPTY_DRAFT);
  }

  function announce(delivered: boolean) {
    toast.success(<Message id={delivered ? "tenant.providerDelivered" : "tenant.providerQueued"} />);
  }

  async function save(event: React.FormEvent) {
    event.preventDefault();
    if (!draft || inFlight.current) return;
    const problem = checkProviderDraft(draft, saved);
    if (problem) { setError(<Message id={PROBLEM_LABEL[problem]} />); return; }
    inFlight.current = true;
    setPending(true); setError(null);
    try {
      const result = await run(putNodeProviderRequest(surface, nodeId, draft));
      announce(result.delivered);
      setDraft(null);
      providers.reload();
    } catch (failure) {
      setError(failure instanceof ApiError && failure.kind === "conflict" ? <Message id="tenant.providerLimit" /> : <ErrorMessage error={failure} />);
    } finally {
      // The key never outlives an attempt. / Key 不会在一次尝试之后保留。
      setDraft((current) => (current ? { ...current, apiKey: "" } : current));
      inFlight.current = false; setPending(false);
    }
  }

  async function remove(id: string) {
    if (inFlight.current || !window.confirm(t("tenant.providerDeleteConfirm"))) return;
    inFlight.current = true;
    setPending(true); setError(null);
    try {
      announce((await run(deleteNodeProviderRequest(surface, nodeId, id))).delivered);
      providers.reload();
    } catch (failure) {
      if (failure instanceof ApiError && failure.kind === "not_found") providers.reload();
      else setError(<ErrorMessage error={failure} />);
    } finally { inFlight.current = false; setPending(false); }
  }

  if (providers.errorKind === "not_found") return <p className="text-sm text-muted-foreground"><Message id="tenant.providersDisabled" /></p>;
  if (providers.error) return <ErrorState message={providers.error} onRetry={providers.reload} />;
  const data = providers.data;
  if (!data) return <LoadingState rows={2} />;

  return <section className="grid gap-3" aria-label={t("tenant.providers")}>
    <p className="text-sm text-muted-foreground"><Message id="tenant.providersIntro" /></p>
    {!data.connected ? <p className="text-sm text-muted-foreground"><Message id="tenant.providerOffline" /></p> : data.report ? <p className="text-sm text-muted-foreground">
      <Message id="tenant.providerOutcome" values={{ outcome: t(OUTCOME_LABEL[data.report.outcome]), applied: data.report.appliedVersion, version: data.version }} />
    </p> : null}
    {data.items.length === 0 ? <EmptyState title={t("tenant.providersEmpty")} /> : <ul className="grid gap-2">
      {data.items.map((p) => <li key={p.id} className="grid gap-1 rounded-lg border p-3 text-sm">
        <div className="flex flex-wrap items-center gap-2">
          <code className="break-all">{p.id}</code>
          <Badge variant="outline">{p.kind}</Badge>
          <Badge variant={p.state === "active" ? "default" : p.state === "" ? "outline" : "destructive"}><Message id={STATE_LABEL[p.state]} /></Badge>
        </div>
        <p className="break-all text-muted-foreground">{p.baseUrl || t("tenant.providerDefaultEndpoint")}</p>
        {p.models.length > 0 ? <p className="break-all text-muted-foreground">{p.models.join(", ")}</p> : null}
        <p className="text-muted-foreground"><Message id="tenant.providerKeyTail" values={{ last4: p.keyLast4 || "····", time: formatDateTime(p.updatedAt, undefined, locale) }} /></p>
        {canManage ? <div className="flex gap-2">
          <Button size="sm" variant="outline" disabled={pending} onClick={() => edit(p)}><Message id="tenant.providerEdit" /></Button>
          <Button size="sm" variant="destructive" disabled={pending} onClick={() => remove(p.id)}><Message id="tenant.providerDelete" /></Button>
        </div> : null}
      </li>)}
    </ul>}
    {canManage && !draft ? <Button size="sm" className="w-fit" disabled={pending} onClick={() => edit(null)}><Message id="tenant.providerNew" /></Button> : null}
    {canManage && draft ? <ProviderForm draft={draft} saved={saved} pending={pending} onChange={setDraft} onSubmit={save} onCancel={() => { setDraft(null); setError(null); }} nodeId={nodeId} /> : null}
    {error ? <FormError message={error} /> : null}
  </section>;
}

function ProviderForm({ draft, saved, pending, nodeId, onChange, onSubmit, onCancel }: {
  draft: ProviderDraft; saved: NodeProvider | null; pending: boolean; nodeId: string;
  onChange: (draft: ProviderDraft) => void; onSubmit: (event: React.FormEvent) => void; onCancel: () => void;
}) {
  const { t } = useI18n();
  const field = (name: string) => `provider-${name}-${nodeId}`;
  const set = (patch: Partial<ProviderDraft>) => onChange({ ...draft, ...patch });
  return <form onSubmit={onSubmit} className="grid gap-3 rounded-lg border p-3 text-sm" autoComplete="off">
    <label className="grid gap-1" htmlFor={field("id")}><Message id="tenant.providerId" />
      <Input id={field("id")} value={draft.id} disabled={pending || saved !== null} onChange={(e) => set({ id: e.target.value })} />
      <span className="text-muted-foreground"><Message id="tenant.providerIdHint" /></span>
    </label>
    <label className="grid gap-1" htmlFor={field("kind")}><Message id="tenant.providerKind" />
      <select id={field("kind")} className="h-9 w-fit rounded-md border bg-background px-3" value={draft.kind} disabled={pending} onChange={(e) => set({ kind: e.target.value })}>
        {HOSTED_KINDS.map((kind) => <option key={kind} value={kind}>{kind}</option>)}
      </select>
    </label>
    <label className="grid gap-1" htmlFor={field("base")}><Message id="tenant.providerBaseUrl" />
      <Input id={field("base")} value={draft.baseUrl} placeholder="https://" disabled={pending} onChange={(e) => set({ baseUrl: e.target.value })} />
      <span className="text-muted-foreground"><Message id="tenant.providerBaseUrlHint" /></span>
    </label>
    <label className="grid gap-1" htmlFor={field("key")}><Message id="tenant.providerApiKey" />
      <Input id={field("key")} type="password" autoComplete="new-password" spellCheck={false} value={draft.apiKey} disabled={pending}
        placeholder={saved ? t("tenant.providerApiKeyKeep") : ""} onChange={(e) => set({ apiKey: e.target.value })} />
      <span className="text-muted-foreground"><Message id="tenant.providerApiKeyHint" /></span>
    </label>
    <label className="grid gap-1" htmlFor={field("models")}><Message id="tenant.providerModels" />
      <textarea id={field("models")} className="min-h-16 rounded-md border bg-background px-3 py-2" value={draft.models} disabled={pending} onChange={(e) => set({ models: e.target.value })} />
      <span className="text-muted-foreground"><Message id="tenant.providerModelsHint" /></span>
    </label>
    <div className="flex gap-2">
      <Button type="submit" size="sm" disabled={pending} aria-busy={pending}><Message id={pending ? "tenant.providerSaving" : "tenant.providerSave"} /></Button>
      <Button type="button" size="sm" variant="outline" disabled={pending} onClick={onCancel}><Message id="tenant.providerCancel" /></Button>
    </div>
  </form>;
}

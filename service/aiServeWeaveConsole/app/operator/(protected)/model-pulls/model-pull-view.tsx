"use client";
import { formatBytes as displayBytes } from "@/lib/i18n/format";

import { ErrorMessage } from "@/components/i18n/error-message";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";
import type { Locale } from "@/lib/i18n/locale";

import * as React from "react";

import { ErrorState, FormError } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  modelPullStatusRequest,
  modelPullTriggerRequest,
  type ModelPullStatusResult,
} from "@/lib/console/model-pull";

import { formatDateTime } from "@/lib/console/format";
import { nodePathSegment } from "@/lib/console/node-path";

const STATE_LABELS = (t: Translator): Record<string, string> => ({
  pending: t("tenant.queued"), downloading: t("operator.downloading"), done: t("operator.completed"), failed: t("tenant.failed"), unspecified: t("tenant.unknown"),
});

const REASON_LABELS = (t: Translator): Record<string, string> => ({
  unknown_name: t("operator.nameIsNotInTheNodeSLocal"),
  invalid_spec: t("operator.manifestEntryIsMalformed"),
  not_allowlisted: t("operator.sourceIsNotInTheNodeSLocal"),
  quota_exceeded: t("operator.perRunByteBudgetExhausted"),
  ledger_quota_exceeded: t("operator.persistentCumulativeLedgerLimitReached"),
  disk_space_low: t("operator.insufficientFreeSpaceOnTheDestinationDisk"),
  fetch_failed: t("operator.networkRequestFailed"),
  unexpected_http_status: t("operator.sourceReturnedAnUnexpectedHttpStatus"),
  checksum_mismatch: t("operator.checksumMismatch"),
  storage_error: t("operator.localDiskWriteFailed"),
  ollama_unconfigured: t("operator.ollamaIsNotConfiguredOnThisNode"),
  ollama_pull_failed: t("operator.theOllamaServerReportedAnError"),
});

/** formatBytes renders a byte count as a human-scaled string, or "—" for an
 * unknown total (BytesTotal is 0 when the manifest entry's size was never
 * declared).
 *
 * formatBytes 把字节数渲染成人类可读的量级字符串；未知的总量（清单条目从
 * 未声明大小时 BytesTotal 为 0）渲染为 "—"。 */
function formatBytes(bytes: number, locale: Locale): string {
  return bytes <= 0 ? "—" : displayBytes(bytes, locale);
}

/** ModelPullView is the per-node lookup + trigger panel described in
 * page.tsx's doc comment.
 *
 * ModelPullView 是 page.tsx 文档注释所述的按节点查询与触发面板。 */
export function ModelPullView() {
  const { locale, t } = useI18n();
  const run = useConsoleRequest();
  const [nodeIdInput, setNodeIdInput] = React.useState("");
  const [nodeId, setNodeId] = React.useState<string | null>(null);
  const [status, setStatus] = React.useState<ModelPullStatusResult | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [queryError, setQueryError] = React.useState<React.ReactNode>(null);
  const [notice, setNotice] = React.useState<React.ReactNode>(null);
  const [namesInput, setNamesInput] = React.useState("");
  const [triggerPending, setTriggerPending] = React.useState(false);
  const [triggerError, setTriggerError] = React.useState<React.ReactNode>(null);

  async function query(event: React.FormEvent) {
    event.preventDefault();
    const id = nodeIdInput.trim();
    if (!nodePathSegment(id)) {
      setQueryError(<Message id={"operator.nodeIdIsRequiredAndCannotContainPath"} />);
      return;
    }
    setLoading(true);
    setQueryError(null);
    setNotice(null);
    try {
      const result = await run(modelPullStatusRequest(id));
      setStatus(result);
      setNodeId(id);
    } catch (failure) {
      setQueryError(<ErrorMessage error={failure} />);
      setStatus(null);
      setNodeId(null);
    } finally {
      setLoading(false);
    }
  }

  async function reload() {
    if (!nodeId) {
      return;
    }
    setLoading(true);
    setQueryError(null);
    try {
      setStatus(await run(modelPullStatusRequest(nodeId)));
    } catch (failure) {
      setQueryError(<ErrorMessage error={failure} />);
    } finally {
      setLoading(false);
    }
  }

  async function triggerPull(event: React.FormEvent) {
    event.preventDefault();
    if (!nodeId) {
      return;
    }
    const names = namesInput
      .split(/[\n,]/)
      .map((name) => name.trim())
      .filter((name) => name.length > 0);
    if (names.length === 0) {
      setTriggerError(<Message id={"operator.enterAtLeastOneName"} />);
      return;
    }
    setTriggerPending(true);
    setTriggerError(null);
    setNotice(null);
    try {
      const outcome = await run(modelPullTriggerRequest(nodeId, names));
      const connected = outcome.replicas.filter((replica) => replica.connected).length;
      setNotice(<Message id={"operator.triggerForDispatchedConfiguredReplicasReportingTheNode"} values={{ value: names.join(", "), connected: connected, count: outcome.replicas.length }} />);
      setNamesInput("");
    } catch (failure) {
      setTriggerError(<ErrorMessage error={failure} />);
    } finally {
      setTriggerPending(false);
    }
  }

  return <div className="grid gap-8">
    <section className="grid gap-4" aria-labelledby="model-pull-title">
      <div>
        <h1 id="model-pull-title" className="font-heading text-lg font-semibold"><Message id="operator.modelPulls" /></h1>
        <p className="text-sm text-muted-foreground">
          <Message id="operator.queryAndTriggerPullsForModelsDeclaredIn" /></p>
      </div>
      <form onSubmit={query} className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1.5">
          <label htmlFor="node-id" className="text-sm font-medium"><Message id="operator.nodeId" /></label>
          <Input id="node-id" className="max-w-xs" value={nodeIdInput} onChange={(event) => setNodeIdInput(event.target.value)} placeholder={t("operator.eGMacMini01")} aria-label={t("operator.nodeId")} />
        </div>
        <SubmitButton pending={loading}><Message id="operator.queryStatus" /></SubmitButton>
        {nodeId ? <Button type="button" variant="outline" disabled={loading} onClick={reload}><Message id="operator.queryAgain" /></Button> : null}
      </form>
      <FormError message={queryError} />
      {notice ? <p role="status" className="text-sm">{notice}</p> : null}
    </section>

    {nodeId && !queryError ? <section className="grid gap-4" aria-labelledby="model-pull-node-title">
      <h2 id="model-pull-node-title" className="font-heading text-base font-semibold">
        <code className="break-all">{nodeId}</code>
      </h2>

      <div className="grid gap-2">
        <h3 className="text-sm font-medium"><Message id="operator.knownPulls" /></h3>
        {status && status.pulls.length > 0 ? <ul className="grid gap-2">
          {status.pulls.map((pull) => <li key={pull.name} className="grid gap-1 rounded-xl border p-3 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <code>{pull.name}</code>
              <Badge variant={pull.state === "done" ? "default" : pull.state === "failed" ? "destructive" : "outline"}>
                {STATE_LABELS(t)[pull.state] ?? pull.state}
              </Badge>
              <span className="text-xs text-muted-foreground"><Message id="tenant.updated_734a94" values={{ value: formatDateTime(pull.updatedAt, undefined, locale) }} /></span>
            </div>
            <div className="text-xs text-muted-foreground">
              {formatBytes(pull.bytesDownloaded, locale)} / {formatBytes(pull.bytesTotal, locale)}
              {pull.reason ? <> — {REASON_LABELS(t)[pull.reason] ?? pull.reason}</> : null}
            </div>
          </li>)}
        </ul> : <p className="text-sm text-muted-foreground"><Message id="operator.thisNodeHasNotReportedAnyPullStatus" /></p>}
      </div>

      <form onSubmit={triggerPull} className="grid gap-3 rounded-xl border p-4">
        <h3 className="text-sm font-medium"><Message id="operator.triggerPull" /></h3>
        <p className="text-xs text-muted-foreground"><Message id="operator.separateMultipleNamesWithCommasOrNewlinesEach" /></p>
        <div className="grid gap-1.5">
          <label htmlFor="pull-names" className="text-sm font-medium"><Message id="operator.names" /></label>
          <textarea
            id="pull-names"
            className="min-h-20 max-w-md rounded-md border bg-transparent px-3 py-2 text-sm"
            value={namesInput}
            onChange={(event) => setNamesInput(event.target.value)}
            placeholder={"qwen3-coder:30b\nllama3.1:8b"}
            aria-label={t("operator.modelNamesToTrigger")}
          />
        </div>
        <div>
          <SubmitButton pending={triggerPending}><Message id="operator.trigger" /></SubmitButton>
        </div>
        <FormError message={triggerError} />
      </form>

      <div className="grid gap-2">
        <h3 className="text-sm font-medium"><Message id="operator.configuredReplicas" /></h3>
        {status && status.replicas.length > 0 ? <ul className="grid gap-1 text-sm">
          {status.replicas.map((replica) => <li key={replica.endpoint}>
            <code>{replica.endpoint}</code> — <Badge variant={replica.connected ? "default" : "outline"}>{replica.connected ? t("operator.connected") : t("operator.disconnected")}</Badge>
            {replica.error ? <span className="text-muted-foreground"> {replica.error}</span> : null}
          </li>)}
        </ul> : <p className="text-sm text-muted-foreground"><Message id="operator.noReplicasHaveReportedThisNode" /></p>}
      </div>
    </section> : null}

    {queryError ? <ErrorState message={queryError} onRetry={nodeId ? reload : undefined} /> : null}
  </div>;
}

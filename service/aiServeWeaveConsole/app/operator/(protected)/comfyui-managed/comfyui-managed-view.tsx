"use client";
import { MessageError } from "@/lib/i18n/problem";

import { ErrorMessage } from "@/components/i18n/error-message";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";

import * as React from "react";

import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { ErrorState, FormError } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  comfyUIManagedCustomNodeInstallRequest,
  comfyUIManagedStatusRequest,
  comfyUIManagedTriggerRequest,
  type ComfyUIManagedAction,
  type ComfyUIManagedStatus,
} from "@/lib/console/comfyui-managed";

import { formatDateTime } from "@/lib/console/format";
import { nodePathSegment } from "@/lib/console/node-path";

const ACTION_LABELS = (t: Translator): Record<ComfyUIManagedAction, string> => ({
  start: t("operator.start"), stop: t("operator.stop"), restart: t("operator.restart"),
});

const ACTION_EFFECTS = (t: Translator): Record<ComfyUIManagedAction, string> => ({
  start: t("operator.ensureTheContainerExistsAndIsRunningAn"),
  stop: t("operator.unregisterTheRuntimeThenStopAndRemoveThe"),
  restart: t("operator.stopAndThenStartTheContainerRecreatingIt"),
});

const STATE_LABELS = (t: Translator): Record<string, string> => ({
  pending: t("operator.notCreatedYet"), starting: t("operator.starting"), running: t("tenant.running"), stopped: t("operator.stopped"), failed: t("tenant.failed"), unspecified: t("tenant.unknown"),
});

/** ComfyUIManagedView is the per-node lookup + control panel described in
 * page.tsx's doc comment.
 *
 * ComfyUIManagedView 是 page.tsx 文档注释所述的按节点查询与操作面板。 */
export function ComfyUIManagedView() {
  const { locale, t } = useI18n();
  const run = useConsoleRequest();
  const [nodeIdInput, setNodeIdInput] = React.useState("");
  const [nodeId, setNodeId] = React.useState<string | null>(null);
  const [status, setStatus] = React.useState<ComfyUIManagedStatus | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [queryError, setQueryError] = React.useState<React.ReactNode>(null);
  const [notice, setNotice] = React.useState<React.ReactNode>(null);
  const [confirmAction, setConfirmAction] = React.useState<ComfyUIManagedAction | null>(null);
  const [customNodeName, setCustomNodeName] = React.useState("");
  const [installPending, setInstallPending] = React.useState(false);
  const [installError, setInstallError] = React.useState<React.ReactNode>(null);

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
      const result = await run(comfyUIManagedStatusRequest(id));
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
      setStatus(await run(comfyUIManagedStatusRequest(nodeId)));
    } catch (failure) {
      setQueryError(<ErrorMessage error={failure} />);
    } finally {
      setLoading(false);
    }
  }

  async function installCustomNode(event: React.FormEvent) {
    event.preventDefault();
    if (!nodeId) {
      return;
    }
    const name = customNodeName.trim();
    if (!name) {
      setInstallError(<Message id={"operator.nodeNameIsRequired"} />);
      return;
    }
    setInstallPending(true);
    setInstallError(null);
    setNotice(null);
    try {
      const outcome = await run(comfyUIManagedCustomNodeInstallRequest(nodeId, name));
      const connected = outcome.replicas.filter((replica) => replica.connected).length;
      setNotice(<Message id={"operator.installationOfDispatchedConfiguredReplicasReportingTheNode"} values={{ name: name, connected: connected, count: outcome.replicas.length }} />);
      setCustomNodeName("");
    } catch (failure) {
      setInstallError(<ErrorMessage error={failure} />);
    } finally {
      setInstallPending(false);
    }
  }

  const instance = status?.instances[0] ?? null;

  return <div className="grid gap-8">
    <section className="grid gap-4" aria-labelledby="comfyui-managed-title">
      <div>
        <h1 id="comfyui-managed-title" className="font-heading text-lg font-semibold">ComfyUI Managed</h1>
        <p className="text-sm text-muted-foreground">
          <Message id="operator.queryAndManageTheSingleManagedComfyuiContainer" /></p>
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

    {nodeId && !queryError ? <section className="grid gap-4" aria-labelledby="comfyui-managed-instance-title">
      <h2 id="comfyui-managed-instance-title" className="font-heading text-base font-semibold">
        <code className="break-all">{nodeId}</code>
      </h2>
      {instance ? <div className="grid gap-3 rounded-xl border p-4">
        <div className="flex flex-wrap items-center gap-2">
          <code className="text-sm">{instance.containerName}</code>
          <Badge variant={instance.state === "running" ? "default" : instance.state === "failed" ? "destructive" : "outline"}>
            {STATE_LABELS(t)[instance.state] ?? instance.state}
          </Badge>
          <span className="text-xs text-muted-foreground"><Message id="tenant.updated_734a94" values={{ value: formatDateTime(instance.updatedAt, undefined, locale) }} /></span>
        </div>
        <div className="flex flex-wrap gap-2">
          {(["start", "stop", "restart"] as const).map((action) => (
            <Button key={action} variant="outline" disabled={confirmAction !== null} onClick={() => setConfirmAction(action)}>
              {ACTION_LABELS(t)[action]}
            </Button>
          ))}
        </div>
        <div>
          <h3 className="text-sm font-medium"><Message id="operator.installedCustomNodes" /></h3>
          {instance.customNodes.length === 0 ? (
            <p className="text-sm text-muted-foreground"><Message id="operator.thisInstanceReportsNoInstalledCustomNodesOr" /></p>
          ) : (
            <ul className="grid gap-1 text-sm">
              {instance.customNodes.map((node) => (
                <li key={node.name}>
                  <code>{node.name}</code> — <span className="text-muted-foreground">{node.version}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div> : <p className="text-sm text-muted-foreground"><Message id="operator.thisNodeHasNoLocallyConfiguredManagedInstance" /></p>}

      <form onSubmit={installCustomNode} className="grid gap-3 rounded-xl border p-4">
        <h3 className="text-sm font-medium"><Message id="operator.installCustomNode" /></h3>
        <p className="text-xs text-muted-foreground"><Message id="operator.installationCanOnlyBeTriggeredByNameThe_9bb3bc" values={{ code: <code>-comfyui-managed-custom-nodes</code> }} /></p>
        <div className="flex flex-wrap items-end gap-3">
          <div className="grid gap-1.5">
            <label htmlFor="custom-node-name" className="text-sm font-medium"><Message id="operator.nodeName" /></label>
            <Input id="custom-node-name" className="max-w-xs" value={customNodeName} onChange={(event) => setCustomNodeName(event.target.value)} placeholder={t("operator.eGMyNode")} aria-label={t("operator.customNodeName")} />
          </div>
          <SubmitButton pending={installPending}><Message id="operator.install" /></SubmitButton>
        </div>
        <FormError message={installError} />
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

    <ConfirmDialog
      open={confirmAction !== null}
      onOpenChange={(open) => { if (!open) setConfirmAction(null); }}
      title={confirmAction ? ACTION_LABELS(t)[confirmAction] : t("operator.lifecycleActions")}
      confirmLabel={t("operator.confirmAction")}
      destructive={confirmAction === "stop" || confirmAction === "restart"}
      description={confirmAction && nodeId ? <><span className="break-all font-mono">{nodeId}</span>：{ACTION_EFFECTS(t)[confirmAction]}</> : ""}
      onConfirm={async () => {
        if (!confirmAction || !nodeId) return;
        setNotice(null);
        try {
          const outcome = await run(comfyUIManagedTriggerRequest(nodeId, confirmAction));
          const connected = outcome.replicas.filter((replica) => replica.connected).length;
          setNotice(<Message id={"operator.dispatchedConfiguredReplicasReportingTheNodeAsConnected"} values={{ value: <ActionLabel action={confirmAction} />, connected: connected, count: outcome.replicas.length }} />);
        } catch (failure) {
          throw new MessageError("operator.theWriteResultForIsUnconfirmedQueryStatus", {  }, failure);
        }
      }}
    />
  </div>;
}

/** ActionLabel keeps a retained trigger notice in the current language.
 * ActionLabel 让保留的触发结果提示随当前语言更新。 */
function ActionLabel({ action }: { action: ComfyUIManagedAction }) {
  const { t } = useI18n();
  return ACTION_LABELS(t)[action];
}

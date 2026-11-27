"use client";

import * as React from "react";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { ErrorState, FormError, LoadingState } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { useResource } from "@/components/console/use-resource";
import { ErrorMessage } from "@/components/i18n/error-message";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiError } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";
import {
  MAX_RETENTION_DAYS,
  checkDraft,
  deleteStorageRequest,
  draftFromStorage,
  locationChanged,
  secretsKept,
  putStorageRequest,
  storageErrorMessage,
  storageRequest,
  type DraftKind,
  type StorageDraft,
  type StorageProblem,
  type StorageState,
} from "@/lib/console/tenant-storage";
import type { MessageKey } from "@/lib/i18n/messages";
import { MessageError } from "@/lib/i18n/problem";

const PROBLEM_LABEL: Record<StorageProblem, MessageKey> = {
  bucket: "tenant.storageProblemBucket",
  url: "tenant.storageProblemUrl",
  https: "tenant.storageProblemHttps",
  credentials: "tenant.storageErrCredentials",
  retention: "tenant.storageProblemRetention",
};

/**
 * TenantStoragePanel lets an owner or admin point the tenant's workflow
 * artifacts and Studio files at their own S3 or WebDAV. Secrets are
 * write-only: the form never receives them, shows only what the control plane
 * reports (the last four characters of the access key ID), clears what was
 * typed after every attempt, and treats an empty secret as "keep the saved
 * one". The control plane probes the storage before it saves, so a save may
 * take a few seconds; it is sent once and never retried.
 *
 * TenantStoragePanel 让 owner 或 admin 把租户的工作流产物与 Studio 文件指向自己的 S3 或
 * WebDAV。密钥只写不读：表单从不接收它们，只展示控制面报告的内容（Access Key ID 的末四位），
 * 每次尝试之后清空已输入的内容，并把空密钥当作「保留已保存的值」。控制面保存前会探测存储，
 * 所以保存可能要几秒；它只发送一次、从不重试。
 */
export function TenantStoragePanel() {
  const storage = useResource(storageRequest());
  if (storage.errorKind === "not_found") return <p className="text-sm text-muted-foreground"><Message id="tenant.storageDisabled" /></p>;
  if (storage.error) return <ErrorState message={storage.error} onRetry={storage.reload} />;
  const state: StorageState | null = storage.data;
  if (!state) return <LoadingState rows={3} />;
  if (!state.enabled) return <p className="text-sm text-muted-foreground"><Message id="tenant.storageDisabled" /></p>;
  // Keyed by the saved version so a save or delete re-seeds the form with the
  // stored location and empty secrets.
  // 以已保存的版本为 key，保存或删除之后表单会用存储的位置与空的密钥重新初始化。
  return <StorageForm key={state.storage?.updatedAt ?? "none"} state={state} onChanged={storage.reload} />;
}

function StorageForm({ state, onChanged }: { state: StorageState; onChanged: () => void }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const saved = state.storage;
  const [draft, setDraft] = React.useState<StorageDraft>(() => draftFromStorage(saved));
  const [pending, setPending] = React.useState(false);
  const inFlight = React.useRef(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const [confirm, setConfirm] = React.useState<"replace" | "delete" | null>(null);
  const set = (patch: Partial<StorageDraft>) => setDraft((current) => ({ ...current, ...patch }));

  const moved = locationChanged(draft, saved);
  // Same kind, new place: the saved secrets cannot follow, so ask for new ones.
  // 类型不变而位置变了：已保存的密钥不能跟过去，所以要求重新填写。
  const keepsSecrets = secretsKept(draft, saved);
  const mustRetype = saved !== null && draft.kind !== "none" && !keepsSecrets;

  // The fixed copy for a storage failure, or the generic copy of its kind.
  // 存储失败对应的固定文案，没有时用该失败类型的通用文案。
  function displayError(failure: unknown): unknown {
    const key = storageErrorMessage(failure);
    return key ? new MessageError(key) : failure;
  }

  /** persist sends the save; it throws so a dialog can show the failure. / persist 发出保存请求；失败时抛出，好让对话框展示原因。 */
  async function persist() {
    try {
      await run(putStorageRequest(draft));
      toast.success(<Message id="tenant.storageSavedToast" />);
      onChanged();
    } catch (raw) {
      const failure = displayError(raw);
      // The control plane answers 404 when the feature is off; re-read to show that.
      // 功能关闭时控制面回 404；重新读取以展示这一点。
      if (raw instanceof ApiError && raw.status === 404) onChanged();
      throw failure;
    } finally {
      // Secrets never outlive an attempt. / 密钥不会在一次尝试之后保留。
      setDraft((current) => ({ ...current, accessKeyId: "", secretAccessKey: "", webdavPassword: "" }));
    }
  }

  async function remove() {
    try {
      await run(deleteStorageRequest());
    } catch (failure) {
      // Already gone: the page just needs to catch up. / 已经不存在了：页面只需跟上现状。
      if (!(failure instanceof ApiError && failure.status === 404)) throw displayError(failure);
    }
    toast.success(<Message id="tenant.storageDeletedToast" />);
    onChanged();
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (inFlight.current) return;
    if (draft.kind === "none") {
      if (saved) setConfirm("delete");
      return;
    }
    const problem = checkDraft(draft, saved, state.allowPrivateEndpoints);
    if (problem) { setError(<Message id={PROBLEM_LABEL[problem]} />); return; }
    setError(null);
    if (saved && moved) { setConfirm("replace"); return; }
    inFlight.current = true;
    setPending(true);
    try {
      await persist();
    } catch (failure) {
      setError(<ErrorMessage error={failure} />);
    } finally {
      inFlight.current = false;
      setPending(false);
    }
  }

  const secretPlaceholder = (last4?: string) => {
    if (!keepsSecrets) return "";
    return last4 ? t("tenant.storageSecretSavedLast4", { last4 }) : t("tenant.storageSecretSaved");
  };
  const addressHint = state.allowPrivateEndpoints ? "tenant.storageAddressPrivateAllowed" : "tenant.storageAddressPublicOnly";
  const id = (name: string) => `storage-${name}`;

  return <>
    <form onSubmit={submit} className="grid gap-3 text-sm" autoComplete="off" noValidate>
      <div className="flex flex-wrap items-center gap-2">
        {saved ? <Badge variant="secondary">{saved.kind === "s3" ? "S3" : "WebDAV"}</Badge> : <Badge variant="outline"><Message id="tenant.storageNotConfigured" /></Badge>}
        {saved ? <span className="text-muted-foreground"><Message id="tenant.storageUpdated" values={{ time: formatDateTime(saved.updatedAt, undefined, locale), user: saved.updatedBy }} /></span> : null}
      </div>

      <label className="grid gap-1" htmlFor={id("kind")}><Message id="tenant.storageKind" />
        <select id={id("kind")} className="h-9 w-fit rounded-md border bg-background px-3" value={draft.kind} disabled={pending}
          onChange={(e) => { setError(null); set({ kind: e.target.value as DraftKind }); }}>
          <option value="none">{t("tenant.storageKindNone")}</option>
          <option value="s3">S3</option>
          <option value="webdav">WebDAV</option>
        </select>
      </label>

      {draft.kind === "s3" ? <>
        <label className="grid gap-1" htmlFor={id("endpoint")}><Message id="tenant.storageEndpoint" />
          <Input id={id("endpoint")} value={draft.endpoint} placeholder="https://" spellCheck={false} disabled={pending} onChange={(e) => set({ endpoint: e.target.value })} />
          <span className="text-muted-foreground"><Message id="tenant.storageEndpointHint" /> <Message id={addressHint} /></span>
        </label>
        <label className="grid gap-1" htmlFor={id("region")}><Message id="tenant.storageRegion" />
          <Input id={id("region")} value={draft.region} spellCheck={false} disabled={pending} onChange={(e) => set({ region: e.target.value })} />
        </label>
        <label className="grid gap-1" htmlFor={id("bucket")}><Message id="tenant.storageBucket" />
          <Input id={id("bucket")} value={draft.bucket} spellCheck={false} disabled={pending} onChange={(e) => set({ bucket: e.target.value })} />
        </label>
        <label className="grid gap-1" htmlFor={id("prefix")}><Message id="tenant.storagePrefix" />
          <Input id={id("prefix")} value={draft.prefix} spellCheck={false} disabled={pending} onChange={(e) => set({ prefix: e.target.value })} />
          <span className="text-muted-foreground"><Message id="tenant.storagePrefixHint" /></span>
        </label>
        <label className="flex items-center gap-2" htmlFor={id("path-style")}>
          <input id={id("path-style")} type="checkbox" checked={draft.pathStyle} disabled={pending} onChange={(e) => set({ pathStyle: e.target.checked })} />
          <Message id="tenant.storagePathStyle" />
        </label>
        <label className="grid gap-1" htmlFor={id("access-key")}><Message id="tenant.storageAccessKeyId" />
          <Input id={id("access-key")} value={draft.accessKeyId} autoComplete="off" spellCheck={false} disabled={pending}
            placeholder={secretPlaceholder(saved?.s3?.accessKeyIdLast4)} onChange={(e) => set({ accessKeyId: e.target.value })} />
        </label>
        <label className="grid gap-1" htmlFor={id("secret")}><Message id="tenant.storageSecretAccessKey" />
          <Input id={id("secret")} type="password" value={draft.secretAccessKey} autoComplete="new-password" spellCheck={false} disabled={pending}
            placeholder={secretPlaceholder()} onChange={(e) => set({ secretAccessKey: e.target.value })} />
        </label>
      </> : null}

      {draft.kind === "webdav" ? <>
        <label className="grid gap-1" htmlFor={id("url")}><Message id="tenant.storageWebdavUrl" />
          <Input id={id("url")} value={draft.webdavUrl} placeholder="https://" spellCheck={false} disabled={pending} onChange={(e) => set({ webdavUrl: e.target.value })} />
          <span className="text-muted-foreground"><Message id={addressHint} /></span>
        </label>
        <label className="grid gap-1" htmlFor={id("dir")}><Message id="tenant.storageDir" />
          <Input id={id("dir")} value={draft.webdavDir} spellCheck={false} disabled={pending} onChange={(e) => set({ webdavDir: e.target.value })} />
          <span className="text-muted-foreground"><Message id="tenant.storagePrefixHint" /></span>
        </label>
        <label className="grid gap-1" htmlFor={id("username")}><Message id="tenant.storageWebdavUsername" />
          <Input id={id("username")} value={draft.webdavUsername} autoComplete="off" spellCheck={false} disabled={pending} onChange={(e) => set({ webdavUsername: e.target.value })} />
        </label>
        <label className="grid gap-1" htmlFor={id("password")}><Message id="tenant.storageWebdavPassword" />
          <Input id={id("password")} type="password" value={draft.webdavPassword} autoComplete="new-password" spellCheck={false} disabled={pending}
            placeholder={secretPlaceholder()} onChange={(e) => set({ webdavPassword: e.target.value })} />
        </label>
      </> : null}

      {draft.kind !== "none" ? <>
        <p className={mustRetype ? "text-destructive" : "text-muted-foreground"}>
          <Message id={mustRetype ? "tenant.storageSecretRetype" : "tenant.storageSecretHint"} />
        </p>
        <label className="grid gap-1" htmlFor={id("retention")}><Message id="tenant.storageRetention" />
          <Input id={id("retention")} className="w-32" type="number" inputMode="numeric" min={0} max={MAX_RETENTION_DAYS} step={1}
            value={draft.retentionDays} disabled={pending} onChange={(e) => set({ retentionDays: e.target.value })} />
          <span className="text-muted-foreground"><Message id="tenant.storageRetentionHint" /></span>
        </label>
        {saved && moved ? <p className="text-muted-foreground"><Message id="tenant.storageMoveNotice" /></p> : null}
      </> : null}

      <FormError message={error} />

      <div className="flex flex-wrap items-center gap-2">
        {draft.kind === "none" ? (
          saved ? <Button type="submit" variant="destructive" disabled={pending}><Message id="tenant.storageDisable" /></Button> : null
        ) : (
          <SubmitButton pending={pending}><Message id={pending ? "tenant.storageSaving" : "tenant.storageSave"} /></SubmitButton>
        )}
        {saved && draft.kind !== "none" ? <Button type="button" variant="outline" disabled={pending} onClick={() => setConfirm("delete")}><Message id="tenant.storageDelete" /></Button> : null}
      </div>
    </form>

    <ConfirmDialog
      open={confirm === "replace"}
      onOpenChange={(open) => { if (!open) setConfirm(null); }}
      title={<Message id="tenant.storageReplaceTitle" />}
      description={<Message id="tenant.storageMoveNotice" />}
      confirmLabel={<Message id="tenant.storageSave" />}
      onConfirm={persist}
    />
    <ConfirmDialog
      open={confirm === "delete"}
      onOpenChange={(open) => { if (!open) setConfirm(null); }}
      title={<Message id="tenant.storageDeleteTitle" />}
      description={<Message id="tenant.storageDeleteBody" />}
      confirmLabel={<Message id="tenant.storageDelete" />}
      destructive
      onConfirm={remove}
    />
  </>;
}

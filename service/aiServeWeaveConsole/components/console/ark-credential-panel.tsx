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
import {
  DEFAULT_ARK_BASE_URL,
  arkErrorMessage,
  arkRequest,
  checkArkDraft,
  deleteArkRequest,
  draftFromCredential,
  keyKept,
  putArkRequest,
  type ArkDraft,
  type ArkProblem,
  type ArkState,
  type ArkSurface,
} from "@/lib/console/ark";
import { ApiError } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";
import type { MessageKey } from "@/lib/i18n/messages";
import { MessageError } from "@/lib/i18n/problem";

const PROBLEM_LABEL: Record<ArkProblem, MessageKey> = {
  key: "tenant.arkProblemKey",
  base_url: "tenant.arkProblemBaseUrl",
  models: "tenant.arkProblemModels",
};

/** arkFailure turns a failed Ark call into its fixed copy, or leaves it to the generic copy of its kind. / arkFailure 把失败的方舟调用转成固定文案，没有时交给该失败类型的通用文案。 */
export function arkFailure(failure: unknown): unknown {
  const key = arkErrorMessage(failure);
  return key ? new MessageError(key) : failure;
}

/**
 * ArkCredentialPanel saves the Volcengine Ark key of one scope: the tenant's
 * own (surface "admin", owner/admin only) or the platform's (surface
 * "operator"). The key is write-only: the form never receives it, shows only
 * its last four characters, clears what was typed after every attempt, and
 * treats an empty key as "keep the saved one" — which the control plane allows
 * only while the base URL stays the same. Only the platform may set a base
 * URL. A save makes the control plane check the key with Ark first, so it may
 * take up to 20 seconds; it is sent once and never retried. children render
 * below the form with the current state, for what depends on a saved key.
 *
 * ArkCredentialPanel 保存一个 scope 的火山方舟 Key：租户自己的（surface 为 "admin"，仅
 * owner/admin）或平台的（surface 为 "operator"）。Key 只写不读：表单从不接收它，只展示末四位，
 * 每次尝试之后清空已输入的内容，并把空 Key 当作「保留已保存的值」——控制面只在 base URL 不变时
 * 允许这样做。只有平台可以设置 base URL。保存时控制面先向方舟检查 Key，可能要 20 秒；只发送
 * 一次、从不重试。children 以当前状态渲染在表单下方，用于依赖已保存 Key 的内容。
 */
export function ArkCredentialPanel({ surface, children }: { surface: ArkSurface; children?: (state: ArkState) => React.ReactNode }) {
  const ark = useResource(arkRequest(surface));
  if (ark.errorKind === "not_found") return <p className="text-sm text-muted-foreground"><Message id="tenant.arkDisabled" /></p>;
  if (ark.error) return <ErrorState message={ark.error} onRetry={ark.reload} />;
  const state = ark.data;
  if (!state) return <LoadingState rows={3} />;
  if (!state.enabled) return <p className="text-sm text-muted-foreground"><Message id="tenant.arkDisabled" /></p>;
  return <>
    {/* Keyed by the saved version so a save or delete re-seeds the form with an empty key.
        以已保存的版本为 key，保存或删除之后表单以空 Key 重新初始化。 */}
    <ArkForm key={state.credential?.updatedAt ?? "none"} surface={surface} state={state} onChanged={ark.reload} />
    {children ? children(state) : null}
  </>;
}

function ArkForm({ surface, state, onChanged }: { surface: ArkSurface; state: ArkState; onChanged: () => void }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const saved = state.credential;
  const [draft, setDraft] = React.useState<ArkDraft>(() => draftFromCredential(saved));
  const [pending, setPending] = React.useState(false);
  const inFlight = React.useRef(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const [confirmDelete, setConfirmDelete] = React.useState(false);
  const set = (patch: Partial<ArkDraft>) => setDraft((current) => ({ ...current, ...patch }));
  const operator = surface === "operator";
  // A tenant never changes the base URL, so its saved key is always kept when left blank.
  // 租户从不更改 base URL，因此留空时总能保留已保存的 Key。
  const keeps = saved !== null && (!operator || keyKept(draft, saved));
  const mustRetype = saved !== null && !keeps;
  const id = (name: string) => `ark-${surface}-${name}`;

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (inFlight.current) return;
    const problem = checkArkDraft(surface, draft, saved);
    if (problem) { setError(<Message id={PROBLEM_LABEL[problem]} />); return; }
    setError(null);
    inFlight.current = true;
    setPending(true);
    try {
      await run(putArkRequest(surface, draft));
      toast.success(<Message id="tenant.arkSavedToast" />);
      onChanged();
    } catch (failure) {
      // The control plane answers 404 when the feature is off; re-read to show that.
      // 功能关闭时控制面回 404；重新读取以展示这一点。
      if (failure instanceof ApiError && failure.status === 404) onChanged();
      setError(<ErrorMessage error={arkFailure(failure)} />);
    } finally {
      // The key never outlives an attempt. / Key 不会在一次尝试之后保留。
      setDraft((current) => ({ ...current, apiKey: "" }));
      inFlight.current = false;
      setPending(false);
    }
  }

  async function remove() {
    try {
      await run(deleteArkRequest(surface));
    } catch (failure) {
      // Already gone: the page just needs to catch up. / 已经不存在了：页面只需跟上现状。
      if (!(failure instanceof ApiError && failure.status === 404)) throw arkFailure(failure);
    }
    toast.success(<Message id="tenant.arkDeletedToast" />);
    onChanged();
  }

  return <>
    <form onSubmit={submit} className="grid gap-3 text-sm" autoComplete="off" noValidate>
      <div className="flex flex-wrap items-center gap-2">
        {saved
          ? <Badge variant="secondary"><Message id="tenant.arkOwnKey" values={{ last4: saved.keyLast4 || "····" }} /></Badge>
          : <Badge variant="outline"><Message id="tenant.arkNotConfigured" /></Badge>}
        {saved ? <span className="text-muted-foreground"><Message id="tenant.arkUpdated" values={{ time: formatDateTime(saved.updatedAt, undefined, locale), user: saved.updatedBy }} /></span> : null}
      </div>
      {!operator && !saved ? (
        <p className={state.platformAvailable ? "text-muted-foreground" : "text-amber-700 dark:text-amber-300"}>
          <Message id={state.platformAvailable ? "tenant.arkUsingPlatform" : "tenant.arkNoKey"} />
        </p>
      ) : null}

      {operator ? (
        <label className="grid gap-1" htmlFor={id("base")}><Message id="operator.arkBaseUrl" />
          <Input id={id("base")} value={draft.baseUrl} placeholder={DEFAULT_ARK_BASE_URL} spellCheck={false} disabled={pending} onChange={(e) => set({ baseUrl: e.target.value })} />
          <span className="text-muted-foreground"><Message id="operator.arkBaseUrlHint" values={{ url: DEFAULT_ARK_BASE_URL }} /></span>
        </label>
      ) : null}

      <label className="grid gap-1" htmlFor={id("key")}><Message id="tenant.arkKey" />
        <Input id={id("key")} type="password" value={draft.apiKey} autoComplete="new-password" spellCheck={false} disabled={pending}
          placeholder={keeps && saved ? t("tenant.arkKeySavedLast4", { last4: saved.keyLast4 || "····" }) : ""} onChange={(e) => set({ apiKey: e.target.value })} />
        <span className={mustRetype ? "text-destructive" : "text-muted-foreground"}><Message id={mustRetype ? "tenant.arkKeyRetype" : "tenant.arkKeyHint"} /></span>
      </label>

      <label className="grid gap-1" htmlFor={id("models")}><Message id={operator ? "operator.arkModels" : "tenant.arkModels"} />
        <textarea id={id("models")} className="min-h-24 rounded-md border bg-background px-3 py-2 font-mono text-sm" rows={4} value={draft.models} spellCheck={false} disabled={pending}
          placeholder="doubao-seedance-1-0-pro-250528" onChange={(e) => set({ models: e.target.value })} />
        <span className="text-muted-foreground"><Message id={operator ? "operator.arkModelsHint" : "tenant.arkModelsHint"} /></span>
      </label>

      <FormError message={error} />

      <div className="flex flex-wrap items-center gap-2">
        <SubmitButton pending={pending}><Message id={pending ? "tenant.arkSaving" : "tenant.arkSave"} /></SubmitButton>
        {saved ? <Button type="button" variant="outline" disabled={pending} onClick={() => setConfirmDelete(true)}><Message id="tenant.arkDelete" /></Button> : null}
      </div>
    </form>

    <ConfirmDialog
      open={confirmDelete}
      onOpenChange={setConfirmDelete}
      title={<Message id="tenant.arkDeleteTitle" />}
      description={<Message id={operator ? "operator.arkDeleteBody" : "tenant.arkDeleteBody"} />}
      confirmLabel={<Message id="tenant.arkDelete" />}
      destructive
      onConfirm={remove}
    />
  </>;
}

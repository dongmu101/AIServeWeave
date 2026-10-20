"use client";
import { ErrorMessage } from "@/components/i18n/error-message";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";


import * as React from "react";
import { CopyIcon } from "lucide-react";
import { toast } from "sonner";

import { FormError } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { parseCreatedApiKey } from "@/lib/console/contract";

import { DEFAULT_TTL_CHOICE, ttlChoices, ttlSeconds } from "@/lib/console/format";

/**
 * Creating a key produces the only plaintext credential this system will ever
 * hand out, and the flow is built around that one fact.
 *
 * The plaintext lives in this component's state and nowhere else: not in the
 * list, which shows the `display` form; not in a toast, which persists on
 * screen and in the accessibility tree after focus has moved on; not in the
 * router cache, since the list is re-read from the server rather than patched
 * with the created row. Closing the dialog drops it, and there is no read path
 * anywhere in the system that can produce it again.
 *
 * 创建 key 会产出本系统唯一一次交出的明文凭据，整个流程都是围绕这一个事实搭的。
 *
 * 明文只存在于本组件的状态里，别处一概没有：不在列表里，那里展示的是 `display` 形式；
 * 不在通知里，通知在焦点离开之后仍留在屏幕上和无障碍树中；也不在路由缓存里，因为列表是
 * 从服务端重新读取的，而不是拿创建返回的那行去打补丁。关闭对话框即丢弃它，而系统中
 * 任何地方都没有能再次产出它的读取路径。
 */
export function CreateKeyDialog({ onCreated }: { onCreated: () => void }) {
  const { locale, t } = useI18n();
  const run = useConsoleRequest();
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [ttl, setTtl] = React.useState(DEFAULT_TTL_CHOICE);
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  // revealed holds the plaintext for exactly as long as its dialog is open.
  //
  // revealed 持有明文，且只在它的对话框打开期间持有。
  const [revealed, setRevealed] = React.useState<
    { plaintext: string; name: string; display: string } | null
  >(null);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }
    const trimmed = name.trim();
    if (trimmed === "") {
      setError(<Message id={"tenant.enterAKeyName"} />);
      return;
    }
    const seconds = ttlSeconds(ttl);
    if (seconds === null) {
      setError(<Message id={"tenant.selectAnExpirationPeriod"} />);
      return;
    }

    setPending(true);
    setError(null);
    try {
      const created = await run({
        method: "POST",
        path: "/admin/v1/apikeys",
        body: { name: trimmed, ttl_seconds: seconds },
        parse: parseCreatedApiKey,
      });
      setOpen(false);
      setName("");
      setTtl(DEFAULT_TTL_CHOICE);
      setRevealed({
        plaintext: created.plaintext,
        name: created.key.name,
        display: created.key.display,
      });
      onCreated();
    } catch (failure) {
      setError(<ErrorMessage error={failure} />);
    } finally {
      setPending(false);
    }
  }

  async function copy(plaintext: string) {
    try {
      await navigator.clipboard.writeText(plaintext);
      toast.success(<Message id={"tenant.copiedToClipboard"} />);
    } catch {
      // Clipboard access needs a secure context and the person's permission.
      // Failing here is ordinary, and the answer is to let them select it.
      //
      // 剪贴板访问需要安全上下文与当事人的授权。这里失败很常见，应对办法是让他们
      // 自己选中复制。
      toast.error(<Message id={"tenant.copyFailedSelectAndCopyTheTextManually"} />);
    }
  }

  return (
    <>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (pending) {
            return;
          }
          if (!next) {
            setError(null);
          }
          setOpen(next);
        }}
      >
        <DialogTrigger render={<Button />}><Message id="tenant.createKey" /></DialogTrigger>
        <DialogContent className="sm:max-w-md">
          <form onSubmit={submit} className="grid gap-4" noValidate>
            <DialogHeader>
              <DialogTitle><Message id="tenant.createApiKey" /></DialogTitle>
              <DialogDescription>
                <Message id="tenant.thePlaintextIsShownOnlyOnceAfterCreation" /></DialogDescription>
            </DialogHeader>

            <div className="grid gap-2">
              <label htmlFor="new-key-name" className="text-sm font-medium">
                <Message id="tenant.name" /></label>
              <Input
                id="new-key-name"
                required
                value={name}
                onChange={(event) => setName(event.target.value)}
                disabled={pending}
                placeholder={t("tenant.eGProductionGateway")}
              />
            </div>

            <div className="grid gap-2">
              <label htmlFor="new-key-ttl" className="text-sm font-medium">
                <Message id="tenant.expirationPeriod" /></label>
              <Select
                value={ttl}
                onValueChange={(next) => {
                  if (next !== null) {
                    setTtl(next);
                  }
                }}
                disabled={pending}
              >
                <SelectTrigger id="new-key-ttl" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {ttlChoices(locale).map((choice) => (
                    <SelectItem key={choice.value} value={choice.value}>
                      {choice.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                <Message id="tenant.theControlPlaneAllowsUpTo365Days" /></p>
            </div>

            <FormError message={error} />

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                disabled={pending}
                onClick={() => setOpen(false)}
              >
                <Message id="common.cancel" /></Button>
              <SubmitButton pending={pending}><Message id="common.create" /></SubmitButton>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog
        open={revealed !== null}
        onOpenChange={(next) => {
          if (!next) {
            setRevealed(null);
          }
        }}
      >
        <DialogContent className="sm:max-w-lg" showCloseButton={false}>
          <DialogHeader>
            <DialogTitle><Message id="tenant.created_c6762b" values={{ name: revealed?.name }} /></DialogTitle>
            <DialogDescription><Message id="tenant.thisIsTheKeySPlaintextShownOnly_8ea4e9" values={{ code: <code className="font-mono">{revealed?.display}</code> }} /></DialogDescription>
          </DialogHeader>

          <div className="flex items-center gap-2 rounded-lg border bg-muted/40 p-3">
            <code className="flex-1 font-mono text-xs break-all select-all">
              {revealed?.plaintext}
            </code>
            <Button
              variant="outline"
              size="icon-sm"
              aria-label={t("tenant.copyPlaintext")}
              onClick={() => revealed && copy(revealed.plaintext)}
            >
              <CopyIcon />
            </Button>
          </div>

          <DialogFooter>
            <Button onClick={() => setRevealed(null)}><Message id="tenant.iVeSavedItClose" /></Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

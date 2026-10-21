"use client";
import { ErrorMessage } from "@/components/i18n/error-message";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";


import * as React from "react";
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
import { parseUser, roleLabels, type Role } from "@/lib/console/contract";
import {ApiError } from "@/lib/console/errors";
import { assignableRoles } from "@/lib/console/permissions";

/**
 * Creating a user is the one Console form that carries a password, and it is
 * the only place one exists in this view: it lives in state for the duration
 * of a submission and is cleared when the dialog settles, whichever way it
 * settled. It is never echoed into an error message, a toast, or the list that
 * follows.
 *
 * 创建用户是 Console 里唯一携带密码的表单，而这个视图里也只有这一处存在密码：它在一次
 * 提交期间存在于状态中，对话框尘埃落定即清除，无论结果如何。它绝不会被回显进错误信息、
 * 通知，或随后的列表里。
 */
export function CreateUserDialog({ onCreated }: { onCreated: () => void }) {
  const {locale } = useI18n();
  const run = useConsoleRequest();
  const [open, setOpen] = React.useState(false);
  const [email, setEmail] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [name, setName] = React.useState("");
  const [role, setRole] = React.useState<Role>("member");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  function reset() {
    setEmail("");
    setPassword("");
    setName("");
    setRole("member");
    setError(null);
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }
    const trimmedEmail = email.trim();
    if (trimmedEmail === "") {
      setError(<Message id={"tenant.enterAnEmailAddress"} />);
      return;
    }
    setPending(true);
    setError(null);
    try {
      const created = await run({
        method: "POST",
        path: "/admin/v1/users",
        body: { email: trimmedEmail, password, name: name.trim(), role },
        parse: parseUser,
      });
      setPassword("");
      toast.success(<Message id={"tenant.createdUser"} values={{ email: created.email }} />);
      reset();
      setOpen(false);
      onCreated();
    } catch (failure) {
      setPassword("");
      setError(
        failure instanceof ApiError && failure.kind === "conflict" ? <Message id={"tenant.thisEmailAddressAlreadyExistsInThisTenant"} /> : <ErrorMessage error={failure} />
      );
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (pending) {
          return;
        }
        if (!next) {
          reset();
        }
        setOpen(next);
      }}
    >
      <DialogTrigger render={<Button />}><Message id="tenant.createUser" /></DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <DialogHeader>
            <DialogTitle><Message id="tenant.createUser" /></DialogTitle>
            <DialogDescription>
              <Message id="tenant.theNewUserBelongsToTheCurrentTenant" /></DialogDescription>
          </DialogHeader>

          <div className="grid gap-2">
            <label htmlFor="new-user-email" className="text-sm font-medium">
              <Message id="tenant.email" /></label>
            <Input
              id="new-user-email"
              type="email"
              autoComplete="off"
              required
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              disabled={pending}
            />
          </div>

          <div className="grid gap-2">
            <label htmlFor="new-user-name" className="text-sm font-medium">
              <Message id="tenant.name" /></label>
            <Input
              id="new-user-name"
              autoComplete="off"
              value={name}
              onChange={(event) => setName(event.target.value)}
              disabled={pending}
            />
          </div>

          <div className="grid gap-2">
            <label htmlFor="new-user-password" className="text-sm font-medium">
              <Message id="tenant.initialPassword" /></label>
            <Input
              id="new-user-password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              disabled={pending}
            />
            <p className="text-xs text-muted-foreground">
              <Message id="tenant.noLengthOrCharacterRestrictionsMayBeLeft" /></p>
          </div>

          <div className="grid gap-2">
            <label htmlFor="new-user-role" className="text-sm font-medium">
              <Message id="tenant.role" /></label>
            <Select
              value={role}
              onValueChange={(next) => {
                if (next !== null) {
                  setRole(next as Role);
                }
              }}
              disabled={pending}
            >
              <SelectTrigger id="new-user-role" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {assignableRoles.map((item) => (
                  <SelectItem key={item} value={item}>
                    {roleLabels(locale)[item]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
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
  );
}

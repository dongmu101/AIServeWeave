"use client";
import { ErrorMessage } from "@/components/i18n/error-message";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";


import * as React from "react";
import { toast } from "sonner";

import { FormError } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { parseNoContent, roleLabels, type Role, type User } from "@/lib/console/contract";

import { assignableRoles } from "@/lib/console/permissions";

/**
 * UserActionsDialog exposes owner-only lifecycle actions for one non-self
 * user. The caller owns the target and renders this dialog outside the table:
 * a reload swaps the table for a loading state, and a dialog living in a row
 * would be unmounted mid-action. A successful action closes the dialog; a
 * failed one keeps it open with the error while the list reloads behind it.
 *
 * UserActionsDialog 为一个非当前用户提供仅 owner 可见的生命周期操作。目标用户由调用方
 * 持有，弹窗渲染在表格之外：列表重新加载时表格会被加载态替换，放在行里的弹窗会在操作
 * 途中被卸载。操作成功即关闭弹窗；失败则保持打开并显示错误，列表在背后重新加载。
 */
export function UserActionsDialog({ user, onClose, onChanged }: { user: User | null; onClose: () => void; onChanged: () => void }) {
  const [pending, setPending] = React.useState(false);
  return (
    <Dialog
      open={user !== null}
      onOpenChange={(next) => {
        if (!next && !pending) {
          onClose();
        }
      }}
    >
      <DialogContent className="sm:max-w-lg">
        {user ? <UserActionsForm key={user.id} user={user} pending={pending} setPending={setPending} onDone={onClose} onChanged={onChanged} /> : null}
      </DialogContent>
    </Dialog>
  );
}

function UserActionsForm({ user, pending, setPending, onDone, onChanged }: {
  user: User;
  pending: boolean;
  setPending: (pending: boolean) => void;
  onDone: () => void;
  onChanged: () => void;
}) {
  const { locale, t } = useI18n();
  const run = useConsoleRequest();
  const [role, setRole] = React.useState<Role>(user.role);
  const [password, setPassword] = React.useState("");
  const [error, setError] = React.useState<React.ReactNode>(null);

  async function mutate(path: string, method: "POST" | "PUT" = "POST", body?: unknown) {
    if (pending) return;
    setPending(true); setError(null);
    try {
      await run({ method, path, body, parse: parseNoContent });
      setPassword(""); toast.success(<Message id={"tenant.userStatusUpdated"} />); onChanged(); onDone();
    } catch (failure) { setPassword(""); setError(<ErrorMessage error={failure} />); onChanged(); }
    finally { setPending(false); }
  }

  const base = `/admin/v1/users/${encodeURIComponent(user.id)}`;
  return <div className="grid gap-4">
    <DialogHeader>
      <DialogTitle><Message id="tenant.manageUser" /></DialogTitle>
      <DialogDescription className="flex flex-wrap items-center gap-2">
        {user.name ? <span className="font-medium text-foreground">{user.name}</span> : null}
        <code className="text-xs">{user.email}</code>
      </DialogDescription>
    </DialogHeader>

    <div className="grid gap-2">
      <label htmlFor="manage-user-role" className="text-sm font-medium"><Message id="tenant.role" /></label>
      <div className="flex flex-wrap gap-2">
        <Select value={role} onValueChange={(value) => value && setRole(value as Role)} disabled={pending}>
          <SelectTrigger id="manage-user-role" className="w-40" aria-label={t("tenant.changeRoleFor", { email: user.email })}><SelectValue /></SelectTrigger>
          <SelectContent>{assignableRoles.map((item) => <SelectItem key={item} value={item}>{roleLabels(locale)[item]}</SelectItem>)}</SelectContent>
        </Select>
        <Button variant="outline" disabled={pending || role === user.role} onClick={() => mutate(`${base}/role`, "PUT", { role })}><Message id="tenant.saveRole" /></Button>
      </div>
    </div>

    <div className="grid gap-2">
      <label htmlFor="manage-user-password" className="text-sm font-medium"><Message id="tenant.resetPassword" /></label>
      <div className="flex flex-wrap gap-2">
        <Input id="manage-user-password" className="w-56" type="password" autoComplete="new-password" aria-label={t("tenant.setANewPasswordForMayBeBlank", { email: user.email })} placeholder={t("tenant.newPasswordMayBeBlank")} value={password} onChange={(event) => setPassword(event.target.value)} disabled={pending} />
        <Button variant="outline" disabled={pending} onClick={() => mutate(`${base}/password`, "PUT", { new_password: password })}><Message id="tenant.resetPassword" /></Button>
      </div>
    </div>

    <Separator />

    <div className="flex flex-wrap gap-2">
      <Button variant="outline" disabled={pending} onClick={() => window.confirm(t("tenant.revokeAllSessionsForThisUser")) && mutate(`${base}/sessions/revoke`)}><Message id="tenant.revokeSessions" /></Button>
      {user.status === "active" ? <Button variant="destructive" disabled={pending} onClick={() => window.confirm(t("tenant.disablingThisUserPermanentlyRevokesAllApiKeys")) && mutate(`${base}/disable`)}><Message id="tenant.disable" /></Button>
        : <Button variant="outline" disabled={pending} onClick={() => mutate(`${base}/enable`)}><Message id="tenant.enable" /></Button>}
    </div>
    <FormError message={error} />
  </div>;
}

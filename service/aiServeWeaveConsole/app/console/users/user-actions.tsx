"use client";
import { ErrorMessage } from "@/components/i18n/error-message";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";


import * as React from "react";
import { toast } from "sonner";

import { FormError } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { parseNoContent, roleLabels, type Role, type User } from "@/lib/console/contract";

import { assignableRoles } from "@/lib/console/permissions";

/** UserActions exposes owner-only lifecycle actions for one non-self user.
 * UserActions 为一个非当前用户提供仅 owner 可见的生命周期操作。 */
export function UserActions({ user, onChanged }: { user: User; onChanged: () => void }) {
  const { locale, t } = useI18n();
  const run = useConsoleRequest();
  const [role, setRole] = React.useState<Role>(user.role);
  const [password, setPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  async function mutate(path: string, method: "POST" | "PUT" = "POST", body?: unknown) {
    if (pending) return;
    setPending(true); setError(null);
    try {
      await run({ method, path, body, parse: parseNoContent });
      setPassword(""); toast.success(<Message id={"tenant.userStatusUpdated"} />); onChanged();
    } catch (failure) { setPassword(""); setError(<ErrorMessage error={failure} />); onChanged(); }
    finally { setPending(false); }
  }

  const base = `/admin/v1/users/${encodeURIComponent(user.id)}`;
  return <div className="grid gap-3 rounded-lg border p-3">
    <div className="flex flex-wrap items-center gap-2"><span className="font-medium">{user.name || user.email}</span><code className="text-xs">{user.email}</code></div>
    <div className="flex flex-wrap items-end gap-2">
      <Select value={role} onValueChange={(value) => value && setRole(value as Role)} disabled={pending}>
        <SelectTrigger className="w-36" aria-label={t("tenant.changeRoleFor", { email: user.email })}><SelectValue /></SelectTrigger>
        <SelectContent>{assignableRoles.map((item) => <SelectItem key={item} value={item}>{roleLabels(locale)[item]}</SelectItem>)}</SelectContent>
      </Select>
      <Button variant="outline" disabled={pending || role === user.role} onClick={() => mutate(`${base}/role`, "PUT", { role })}><Message id="tenant.saveRole" /></Button>
      <Input className="w-44" type="password" autoComplete="new-password" aria-label={t("tenant.setANewPasswordForMayBeBlank", { email: user.email })} placeholder={t("tenant.newPasswordMayBeBlank")} value={password} onChange={(event) => setPassword(event.target.value)} disabled={pending} />
      <Button variant="outline" disabled={pending} onClick={() => mutate(`${base}/password`, "PUT", { new_password: password })}><Message id="tenant.resetPassword" /></Button>
      <Button variant="outline" disabled={pending} onClick={() => window.confirm(t("tenant.revokeAllSessionsForThisUser")) && mutate(`${base}/sessions/revoke`)}><Message id="tenant.revokeSessions" /></Button>
      {user.status === "active" ? <Button variant="destructive" disabled={pending} onClick={() => window.confirm(t("tenant.disablingThisUserPermanentlyRevokesAllApiKeys")) && mutate(`${base}/disable`)}><Message id="tenant.disable" /></Button>
        : <Button variant="outline" disabled={pending} onClick={() => mutate(`${base}/enable`)}><Message id="tenant.enable" /></Button>}
    </div><FormError message={error} />
  </div>;
}

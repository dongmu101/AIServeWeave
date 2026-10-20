"use client";
import { ErrorMessage } from "@/components/i18n/error-message";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";


import * as React from "react";
import { useRouter } from "next/navigation";

import { FormError } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { signOut } from "@/lib/console/api-client";
import { parseNoContent } from "@/lib/console/contract";
import { operatorSignOut } from "@/lib/console/operator-client";

/** AccountSecurity renders self-service password and session revocation for
 * one isolated authentication surface.
 *
 * AccountSecurity 为一个隔离的认证入口渲染自助改密与会话吊销。 */
export function AccountSecurity({ surface }: { surface: "admin" | "operator" }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const router = useRouter();
  const [currentPassword, setCurrentPassword] = React.useState("");
  const [newPassword, setNewPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const prefix = surface === "operator" ? "/operator/v1" : "/admin/v1";
  const login = surface === "operator" ? "/operator/login" : "/login";

  async function leave() {
    if (surface === "operator") await operatorSignOut();
    else await signOut();
    router.replace(login);
    router.refresh();
  }

  async function changePassword(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      await run({ method: "POST", surface, path: `${prefix}/auth/password`, body: {
        current_password: currentPassword, new_password: newPassword,
      }, parse: parseNoContent });
      setCurrentPassword("");
      setNewPassword("");
      await leave();
    } catch (failure) {
      setCurrentPassword("");
      setNewPassword("");
      setError(<ErrorMessage error={failure} />);
    } finally {
      setPending(false);
    }
  }

  async function revokeAll() {
    if (pending || !window.confirm(t("common.revokeAllSessionsForThisAccountAndSign"))) return;
    setPending(true);
    setError(null);
    try {
      await run({ method: "POST", surface, path: `${prefix}/auth/sessions/revoke`, parse: parseNoContent });
      await leave();
    } catch (failure) {
      setError(<ErrorMessage error={failure} />);
    } finally {
      setPending(false);
    }
  }

  return <div className="grid gap-4">
    <div><h1 className="font-heading text-lg font-semibold"><Message id="tenant.accountSecurity" /></h1><p className="text-sm text-muted-foreground"><Message id="common.afterChangingYourPasswordOrRevokingAllSessions" /></p></div>
    <Card><CardHeader><CardTitle><Message id="common.changePassword" /></CardTitle></CardHeader><CardContent>
      <form className="grid max-w-md gap-3" onSubmit={changePassword}>
        <label className="grid gap-1 text-sm"><Message id="common.currentPassword_1dc575" values={{ content: <Input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} disabled={pending} /> }} /></label>
        <label className="grid gap-1 text-sm"><Message id="common.newPassword_862631" values={{ content: <Input type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} disabled={pending} /> }} /></label>
        <p className="text-xs text-muted-foreground"><Message id="common.passwordRulesAreUnchangedThePasswordMayBe" /></p>
        <FormError message={error} /><SubmitButton pending={pending}><Message id="common.changePasswordSignInAgain" /></SubmitButton>
      </form>
    </CardContent></Card>
    <Card><CardHeader><CardTitle><Message id="common.revokeAllSessions" /></CardTitle></CardHeader><CardContent className="grid gap-3">
      <p className="text-sm text-muted-foreground"><Message id="common.immediatelyEndThisAccountSSessionsInAll" /></p>
      <Button variant="destructive" className="w-fit" disabled={pending} onClick={revokeAll}><Message id="common.revokeAllSessions" /></Button>
    </CardContent></Card>
  </div>;
}

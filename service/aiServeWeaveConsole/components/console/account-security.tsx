"use client";

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
import { describe } from "@/lib/console/errors";
import { operatorSignOut } from "@/lib/console/operator-client";

/** AccountSecurity renders self-service password and session revocation for
 * one isolated authentication surface.
 *
 * AccountSecurity 为一个隔离的认证入口渲染自助改密与会话吊销。 */
export function AccountSecurity({ surface }: { surface: "admin" | "operator" }) {
  const run = useConsoleRequest();
  const router = useRouter();
  const [currentPassword, setCurrentPassword] = React.useState("");
  const [newPassword, setNewPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
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
      setError(describe(failure));
    } finally {
      setPending(false);
    }
  }

  async function revokeAll() {
    if (pending || !window.confirm("撤销此账户的全部会话并重新登录？")) return;
    setPending(true);
    setError(null);
    try {
      await run({ method: "POST", surface, path: `${prefix}/auth/sessions/revoke`, parse: parseNoContent });
      await leave();
    } catch (failure) {
      setError(describe(failure));
    } finally {
      setPending(false);
    }
  }

  return <div className="grid gap-4">
    <div><h1 className="font-heading text-lg font-semibold">账户安全</h1><p className="text-sm text-muted-foreground">改密或撤销全部会话后，需要重新登录。</p></div>
    <Card><CardHeader><CardTitle>修改密码</CardTitle></CardHeader><CardContent>
      <form className="grid max-w-md gap-3" onSubmit={changePassword}>
        <label className="grid gap-1 text-sm">当前密码<Input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} disabled={pending} /></label>
        <label className="grid gap-1 text-sm">新密码<Input type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} disabled={pending} /></label>
        <p className="text-xs text-muted-foreground">密码规则保持现状，可以为空；输入不会写入 URL、日志或浏览器存储。</p>
        <FormError message={error} /><SubmitButton pending={pending}>修改并重新登录</SubmitButton>
      </form>
    </CardContent></Card>
    <Card><CardHeader><CardTitle>撤销全部会话</CardTitle></CardHeader><CardContent className="grid gap-3">
      <p className="text-sm text-muted-foreground">立即结束该账户在所有浏览器中的会话，包括当前会话。</p>
      <Button variant="destructive" className="w-fit" disabled={pending} onClick={revokeAll}>撤销全部会话</Button>
    </CardContent></Card>
  </div>;
}

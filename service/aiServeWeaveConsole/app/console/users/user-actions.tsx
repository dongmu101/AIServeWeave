"use client";

import * as React from "react";
import { toast } from "sonner";

import { FormError } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { parseNoContent, ROLE_LABELS, type Role, type User } from "@/lib/console/contract";
import { describe } from "@/lib/console/errors";
import { assignableRoles } from "@/lib/console/permissions";

/** UserActions exposes owner-only lifecycle actions for one non-self user.
 * UserActions 为一个非当前用户提供仅 owner 可见的生命周期操作。 */
export function UserActions({ user, onChanged }: { user: User; onChanged: () => void }) {
  const run = useConsoleRequest();
  const [role, setRole] = React.useState<Role>(user.role);
  const [password, setPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  async function mutate(path: string, method: "POST" | "PUT" = "POST", body?: unknown) {
    if (pending) return;
    setPending(true); setError(null);
    try {
      await run({ method, path, body, parse: parseNoContent });
      setPassword(""); toast.success("用户状态已更新"); onChanged();
    } catch (failure) { setPassword(""); setError(describe(failure)); onChanged(); }
    finally { setPending(false); }
  }

  const base = `/admin/v1/users/${encodeURIComponent(user.id)}`;
  return <div className="grid gap-3 rounded-lg border p-3">
    <div className="flex flex-wrap items-center gap-2"><span className="font-medium">{user.name || user.email}</span><code className="text-xs">{user.email}</code></div>
    <div className="flex flex-wrap items-end gap-2">
      <Select value={role} onValueChange={(value) => value && setRole(value as Role)} disabled={pending}>
        <SelectTrigger className="w-36" aria-label={`调整 ${user.email} 的角色`}><SelectValue /></SelectTrigger>
        <SelectContent>{assignableRoles.map((item) => <SelectItem key={item} value={item}>{ROLE_LABELS[item]}</SelectItem>)}</SelectContent>
      </Select>
      <Button variant="outline" disabled={pending || role === user.role} onClick={() => mutate(`${base}/role`, "PUT", { role })}>保存角色</Button>
      <Input className="w-44" type="password" autoComplete="new-password" aria-label={`为 ${user.email} 设置新密码（可为空）`} placeholder="新密码（可为空）" value={password} onChange={(event) => setPassword(event.target.value)} disabled={pending} />
      <Button variant="outline" disabled={pending} onClick={() => mutate(`${base}/password`, "PUT", { new_password: password })}>重置密码</Button>
      <Button variant="outline" disabled={pending} onClick={() => window.confirm("撤销该用户的全部会话？") && mutate(`${base}/sessions/revoke`)}>撤销会话</Button>
      {user.status === "active" ? <Button variant="destructive" disabled={pending} onClick={() => window.confirm("禁用用户将永久吊销其创建的全部 API Key；Gateway 缓存仍可能在配置 TTL 内接受它们。继续？") && mutate(`${base}/disable`)}>禁用</Button>
        : <Button variant="outline" disabled={pending} onClick={() => mutate(`${base}/enable`)}>启用</Button>}
    </div><FormError message={error} />
  </div>;
}

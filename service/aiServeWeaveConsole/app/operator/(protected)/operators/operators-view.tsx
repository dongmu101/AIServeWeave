"use client";

import * as React from "react";
import { toast } from "sonner";

import { Pager } from "@/components/console/pager";
import { ErrorState, FormError, LoadingState } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { parseNoContent, parsePlatformOperator, parsePlatformOperators, type PlatformOperator } from "@/lib/console/contract";
import { describe } from "@/lib/console/errors";

const PAGE_SIZE = 50;

/** OperatorsView lists and manages peer platform operators. / OperatorsView 列出并管理同级平台运维。 */
export function OperatorsView({ currentOperatorId }: { currentOperatorId: string }) {
  const run = useConsoleRequest();
  const [query, setQuery] = React.useState("");
  const [status, setStatus] = React.useState("");
  const [email, setEmail] = React.useState("");
  const [name, setName] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const operators = usePagedResource<PlatformOperator>({ path: "/operator/v1/operators", surface: "operator", filters: { q: query, status }, pageSize: PAGE_SIZE, parse: parsePlatformOperators });

  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (pending || email.trim() === "") return;
    setPending(true); setError(null);
    try {
      const created = await run({ method: "POST", surface: "operator", path: "/operator/v1/operators", body: { email: email.trim(), name: name.trim(), password }, parse: parsePlatformOperator });
      setEmail(""); setName(""); setPassword(""); toast.success(`已创建 ${created.email}`); operators.reload();
    } catch (failure) { setPassword(""); setError(describe(failure)); }
    finally { setPending(false); }
  }

  return <div className="grid gap-4">
    <div><h1 className="font-heading text-lg font-semibold">平台运维账户</h1><p className="text-sm text-muted-foreground">运维账户彼此同级；系统始终保留至少一名有效运维。</p></div>
    <Card><CardHeader><CardTitle>创建运维账户</CardTitle></CardHeader><CardContent><form className="grid gap-3 sm:grid-cols-2" onSubmit={create}>
      <label className="grid gap-1 text-sm">邮箱<Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} disabled={pending} /></label>
      <label className="grid gap-1 text-sm">名称<Input value={name} onChange={(e) => setName(e.target.value)} disabled={pending} /></label>
      <label className="grid gap-1 text-sm">密码（可为空）<Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} disabled={pending} /></label>
      <SubmitButton pending={pending}>创建</SubmitButton><FormError message={error} />
    </form></CardContent></Card>
    <div className="flex flex-wrap gap-2"><Input className="max-w-xs" aria-label="按邮箱或名称搜索运维账户" placeholder="按邮箱或名称搜索" value={query} onChange={(e) => setQuery(e.target.value)} />
      <Select value={status || "all"} onValueChange={(value) => setStatus(value === "all" || value === null ? "" : value)}><SelectTrigger className="w-36" aria-label="按运维账户状态筛选"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">全部状态</SelectItem><SelectItem value="active">有效</SelectItem><SelectItem value="suspended">已禁用</SelectItem></SelectContent></Select></div>
    {operators.error ? <ErrorState message={operators.error} onRetry={operators.reload} /> : operators.loading ? <LoadingState label="正在读取运维账户" rows={3} /> : <>
      <div className="grid gap-3">{(operators.items ?? []).map((operator) => <OperatorCard key={operator.id} operator={operator} current={operator.id === currentOperatorId} onChanged={operators.reload} />)}</div>
      <Pager resource={operators} loadedCount={operators.items?.length ?? 0} />
    </>}
  </div>;
}

function OperatorCard({ operator, current, onChanged }: { operator: PlatformOperator; current: boolean; onChanged: () => void }) {
  const run = useConsoleRequest();
  const [password, setPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const base = `/operator/v1/operators/${encodeURIComponent(operator.id)}`;
  async function mutate(path: string, method: "POST" | "PUT" = "POST", body?: unknown) {
    setPending(true); setError(null);
    try { await run({ method, surface: "operator", path, body, parse: parseNoContent }); setPassword(""); toast.success("运维账户已更新"); onChanged(); }
    catch (failure) { setPassword(""); setError(describe(failure)); onChanged(); }
    finally { setPending(false); }
  }
  return <Card><CardHeader><CardTitle className="flex flex-wrap items-center gap-2 text-base">{operator.name || operator.email}<Badge variant={operator.status === "active" ? "outline" : "destructive"}>{operator.status === "active" ? "有效" : "已禁用"}</Badge>{current ? <Badge>当前账户</Badge> : null}</CardTitle></CardHeader><CardContent className="grid gap-3">
    <code className="text-xs">{operator.email}</code>
    {!current ? <div className="flex flex-wrap gap-2"><Input className="w-44" type="password" aria-label={`为 ${operator.email} 设置新密码（可为空）`} placeholder="新密码（可为空）" value={password} onChange={(e) => setPassword(e.target.value)} disabled={pending} />
      <Button variant="outline" disabled={pending} onClick={() => mutate(`${base}/password`, "PUT", { new_password: password })}>重置密码</Button>
      <Button variant="outline" disabled={pending} onClick={() => window.confirm("撤销该运维的全部会话？") && mutate(`${base}/sessions/revoke`)}>撤销会话</Button>
      {operator.status === "active" ? <Button variant="destructive" disabled={pending} onClick={() => window.confirm("禁用该平台运维？系统不会允许禁用最后一名有效运维。") && mutate(`${base}/disable`)}>禁用</Button> : <Button variant="outline" disabled={pending} onClick={() => mutate(`${base}/enable`)}>启用</Button>}</div> : <p className="text-sm text-muted-foreground">当前账户请在“账户安全”中改密或撤销会话。</p>}
    <FormError message={error} />
  </CardContent></Card>;
}

"use client";
import { ErrorMessage } from "@/components/i18n/error-message";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

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

const PAGE_SIZE = 50;

/** OperatorsView lists and manages peer platform operators. / OperatorsView 列出并管理同级平台运维。 */
export function OperatorsView({ currentOperatorId }: { currentOperatorId: string }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const [query, setQuery] = React.useState("");
  const [status, setStatus] = React.useState("");
  const [email, setEmail] = React.useState("");
  const [name, setName] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const operators = usePagedResource<PlatformOperator>({ path: "/operator/v1/operators", surface: "operator", filters: { q: query, status }, pageSize: PAGE_SIZE, parse: parsePlatformOperators });

  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (pending || email.trim() === "") return;
    setPending(true); setError(null);
    try {
      const created = await run({ method: "POST", surface: "operator", path: "/operator/v1/operators", body: { email: email.trim(), name: name.trim(), password }, parse: parsePlatformOperator });
      setEmail(""); setName(""); setPassword(""); toast.success(<Message id={"operator.created_0653b3"} values={{ email: created.email }} />); operators.reload();
    } catch (failure) { setPassword(""); setError(<ErrorMessage error={failure} />); }
    finally { setPending(false); }
  }

  return <div className="grid gap-4">
    <div><h1 className="font-heading text-lg font-semibold"><Message id="operator.platformOperatorAccounts" /></h1><p className="text-sm text-muted-foreground"><Message id="operator.operatorAccountsHaveEqualPrivilegesTheSystemAlways" /></p></div>
    <Card><CardHeader><CardTitle><Message id="tenant.createOperatorAccount" /></CardTitle></CardHeader><CardContent><form className="grid gap-3 sm:grid-cols-2" onSubmit={create}>
      <label className="grid gap-1 text-sm"><Message id="operator.email" values={{ content: <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} disabled={pending} /> }} /></label>
      <label className="grid gap-1 text-sm"><Message id="operator.name" values={{ content: <Input value={name} onChange={(e) => setName(e.target.value)} disabled={pending} /> }} /></label>
      <label className="grid gap-1 text-sm"><Message id="operator.passwordMayBeBlank_a3b748" values={{ content: <Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} disabled={pending} /> }} /></label>
      <SubmitButton pending={pending}><Message id="common.create" /></SubmitButton><FormError message={error} />
    </form></CardContent></Card>
    <div className="flex flex-wrap gap-2"><Input className="max-w-xs" aria-label={t("operator.searchOperatorAccountsByEmailOrName")} placeholder={t("operator.searchByEmailOrName")} value={query} onChange={(e) => setQuery(e.target.value)} />
      <Select value={status || "all"} onValueChange={(value) => setStatus(value === "all" || value === null ? "" : value)}><SelectTrigger className="w-36" aria-label={t("operator.filterByOperatorAccountStatus")}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all"><Message id="tenant.allStatuses" /></SelectItem><SelectItem value="active"><Message id="operator.active" /></SelectItem><SelectItem value="suspended"><Message id="operator.disabled" /></SelectItem></SelectContent></Select></div>
    {operators.error ? <ErrorState message={operators.error} onRetry={operators.reload} /> : operators.loading ? <LoadingState label={t("operator.loadingOperatorAccounts")} rows={3} /> : <>
      <div className="grid gap-3">{(operators.items ?? []).map((operator) => <OperatorCard key={operator.id} operator={operator} current={operator.id === currentOperatorId} onChanged={operators.reload} />)}</div>
      <Pager resource={operators} loadedCount={operators.items?.length ?? 0} />
    </>}
  </div>;
}

function OperatorCard({ operator, current, onChanged }: { operator: PlatformOperator; current: boolean; onChanged: () => void }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const [password, setPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const base = `/operator/v1/operators/${encodeURIComponent(operator.id)}`;
  async function mutate(path: string, method: "POST" | "PUT" = "POST", body?: unknown) {
    setPending(true); setError(null);
    try { await run({ method, surface: "operator", path, body, parse: parseNoContent }); setPassword(""); toast.success(<Message id={"operator.operatorAccountUpdated"} />); onChanged(); }
    catch (failure) { setPassword(""); setError(<ErrorMessage error={failure} />); onChanged(); }
    finally { setPending(false); }
  }
  return <Card><CardHeader><CardTitle className="flex flex-wrap items-center gap-2 text-base">{operator.name || operator.email}<Badge variant={operator.status === "active" ? "outline" : "destructive"}>{operator.status === "active" ? t("operator.active") : t("operator.disabled")}</Badge>{current ? <Badge><Message id="operator.currentAccount" /></Badge> : null}</CardTitle></CardHeader><CardContent className="grid gap-3">
    <code className="text-xs">{operator.email}</code>
    {!current ? <div className="flex flex-wrap gap-2"><Input className="w-44" type="password" aria-label={t("tenant.setANewPasswordForMayBeBlank", { email: operator.email })} placeholder={t("tenant.newPasswordMayBeBlank")} value={password} onChange={(e) => setPassword(e.target.value)} disabled={pending} />
      <Button variant="outline" disabled={pending} onClick={() => mutate(`${base}/password`, "PUT", { new_password: password })}><Message id="tenant.resetPassword" /></Button>
      <Button variant="outline" disabled={pending} onClick={() => window.confirm(t("operator.revokeAllSessionsForThisOperator")) && mutate(`${base}/sessions/revoke`)}><Message id="tenant.revokeSessions" /></Button>
      {operator.status === "active" ? <Button variant="destructive" disabled={pending} onClick={() => window.confirm(t("operator.disableThisPlatformOperatorTheSystemWillNot")) && mutate(`${base}/disable`)}><Message id="tenant.disable" /></Button> : <Button variant="outline" disabled={pending} onClick={() => mutate(`${base}/enable`)}><Message id="tenant.enable" /></Button>}</div> : <p className="text-sm text-muted-foreground"><Message id="operator.useAccountSecurityToChangeYourOwnPassword" /></p>}
    <FormError message={error} />
  </CardContent></Card>;
}

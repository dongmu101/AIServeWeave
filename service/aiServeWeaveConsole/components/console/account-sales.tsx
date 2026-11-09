"use client";

import * as React from "react";
import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import { ErrorMessage } from "@/components/i18n/error-message";
import { useConsoleRequest } from "./use-console-request";
import { useResource } from "./use-resource";
import { ConfirmDialog } from "./confirm-dialog";
import { EmptyState, ErrorState, FormError, LoadingState } from "./states";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { parseAccountSales, parseAccountSale, parseAccountPurchase, type AccountSale } from "@/lib/console/account-sales";
import { formatCredits, parseCreditsInput, MAX_PRICE_MICRO_PER_MTOK } from "@/lib/console/billing";
import { formatDateTime, matchesQuery } from "@/lib/console/format";
import { parseFleetSnapshot } from "@/lib/console/fleet";
import { tenantNodeOwner } from "@/lib/console/node-access";
import { ApiError } from "@/lib/console/errors";

/** AccountSales shares the credential-free monthly inventory between surfaces. */
export function AccountSales({ operator = false, canPurchase = false, onPurchased }: { operator?: boolean; canPurchase?: boolean; onPurchased?: () => void }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const surface = operator ? "operator" : "admin";
  const path = `/${surface}/v1/billing/accounts`;
  const resource = useResource({ method: "GET", surface, path, parse: parseAccountSales });
  const [editing, setEditing] = React.useState<AccountSale | "new" | null>(null);
  const [buying, setBuying] = React.useState<{ account: AccountSale; requestId: string } | null>(null);
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(0);
  const rows = (resource.data ?? []).filter(row => matchesQuery([row.name, row.nodeId, row.runtimeId, row.tenantId], query));
  const current = Math.min(page, Math.max(0, Math.ceil(rows.length / 12) - 1));
  return <Card>
    <CardHeader><CardTitle><Message id="tenant.accountMonthly" /></CardTitle></CardHeader>
    <CardContent className="grid gap-4">
      <p className="text-sm text-muted-foreground"><Message id={operator ? "tenant.accountOperatorNote" : "tenant.accountMonthlyNote"} /></p>
      <div className="flex flex-wrap gap-3">
        <Input className="max-w-sm" aria-label={t("tenant.accountSearch")} placeholder={t("tenant.accountSearch")} value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} />
        <Button variant="outline" disabled={resource.loading} onClick={resource.reload}><Message id="tenant.accountRefresh" /></Button>
        {operator ? <Button disabled={resource.loading || resource.error !== null} onClick={() => setEditing("new")}><Message id="tenant.accountAdd" /></Button> : null}
      </div>
      {resource.errorKind === "not_found" ? <EmptyState title={t("tenant.accountDisabled")} description={operator ? t("tenant.accountEnableHint") : undefined} /> : resource.error ? <ErrorState message={resource.error} onRetry={resource.reload} /> : resource.loading ? <LoadingState label={t("tenant.accountMonthly")} rows={3} /> : rows.length === 0 ? <EmptyState title={t("tenant.accountEmpty")} /> : <>
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">{rows.slice(current * 12, current * 12 + 12).map(account => <div key={account.id} className="grid content-between gap-3 rounded-lg border p-4">
          <div className="grid gap-2">
            <p className="font-medium break-all">{account.name}</p>
            <code className="break-all text-xs text-muted-foreground">{account.nodeId} / {account.runtimeId}</code>
            <p className="text-lg font-semibold">{t("tenant.accountMonthlyPrice", { credits: formatCredits(account.priceMicro, locale) })}</p>
            <div className="flex flex-wrap gap-2">
              {account.owned ? <Badge><Message id="tenant.accountOwned" /></Badge> : null}
              {!account.active ? <Badge variant="outline"><Message id="tenant.accountArchived" /></Badge> : null}
            </div>
            {account.expiresAt ? <p className="text-sm">{t("tenant.accountExpires", { time: formatDateTime(account.expiresAt, undefined, locale) })}</p> : null}
            {operator && account.tenantId ? <p className="text-xs break-all">{t("tenant.accountHolder", { tenant: account.tenantId })}</p> : null}
          </div>
          {operator ? <Button variant="outline" onClick={() => setEditing(account)}><Message id="tenant.accountEdit" /></Button> : canPurchase ?
            <Button disabled={!account.active} onClick={() => setBuying({ account, requestId: crypto.randomUUID().replaceAll("-", "") })}><Message id={account.owned ? "tenant.accountRenew" : "tenant.accountBuy"} /></Button> : null}
        </div>)}</div>
        <div className="flex items-center justify-between gap-3">
          <Button variant="outline" disabled={current === 0} onClick={() => setPage(current - 1)}><Message id="tenant.accountPrevious" /></Button>
          <span className="text-sm">{current + 1} / {Math.ceil(rows.length / 12)}</span>
          <Button variant="outline" disabled={(current + 1) * 12 >= rows.length} onClick={() => setPage(current + 1)}><Message id="tenant.accountNext" /></Button>
        </div>
      </>}
    </CardContent>
    <ConfirmDialog open={buying !== null} onOpenChange={open => { if (!open) setBuying(null); }} title={t("tenant.accountPurchase")}
      description={buying ? t("tenant.accountBuyConfirm", { name: buying.account.name, credits: formatCredits(buying.account.priceMicro, locale) }) : ""}
      onConfirm={async () => {
        if (!buying) return;
        try {
          await run({ method: "POST", path: `${path}/${encodeURIComponent(buying.account.id)}/purchase`, body: { request_id: buying.requestId, expected_price_micro: buying.account.priceMicro }, parse: parseAccountPurchase });
        } catch (error) {
          if (error instanceof ApiError && error.status === 409) throw new ApiError("account_unavailable", 409);
          throw error;
        }
        resource.reload();
        onPurchased?.();
      }} />
    {editing ? <AccountEditor key={typeof editing === "string" ? editing : editing.id} account={editing === "new" ? null : editing} inventory={resource.data ?? []} onClose={() => setEditing(null)} onSaved={resource.reload} /> : null}
  </Card>;
}

function AccountEditor({ account, inventory, onClose, onSaved }: { account: AccountSale | null; inventory: AccountSale[]; onClose: () => void; onSaved: () => void }) {
  const run = useConsoleRequest();
  const [name, setName] = React.useState(account?.name ?? "");
  const [autoName, setAutoName] = React.useState("");
  const [node, setNode] = React.useState(account?.nodeId ?? "");
  const [runtime, setRuntime] = React.useState(account?.runtimeId ?? "");
  const [rawPrice, setPrice] = React.useState(account ? String(account.priceMicro / 1_000_000) : "");
  const [active, setActive] = React.useState(account?.active ?? true);
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const price = parseCreditsInput(rawPrice);
  const prefix = React.useId();
  return <Card className="m-4">
    <CardHeader><CardTitle><Message id={account ? "tenant.accountEdit" : "tenant.accountAdd"} /></CardTitle></CardHeader>
    <CardContent><form className="grid gap-3" onSubmit={async event => {
      event.preventDefault();
      if (!node || !runtime || price === null || price <= 0 || price > MAX_PRICE_MICRO_PER_MTOK) return;
      setPending(true); setError(null);
      try {
        await run({ method: account ? "PUT" : "POST", surface: "operator", path: `/operator/v1/billing/accounts${account ? `/${encodeURIComponent(account.id)}` : ""}`,
          body: { name, node_id: node, runtime_id: runtime, price_micro: price, active }, parse: parseAccountSale });
        onSaved(); onClose();
      } catch (failure) { setError(failure); } finally { setPending(false); }
    }}>
      <label htmlFor={`${prefix}-name`}><Message id="tenant.accountName" /></label><Input id={`${prefix}-name`} required maxLength={128} value={name} disabled={pending} onChange={e => setName(e.target.value)} />
      {account ? <>
        <label htmlFor={`${prefix}-node`}><Message id="tenant.accountNode" /></label><Input id={`${prefix}-node`} value={node} disabled />
        <label htmlFor={`${prefix}-runtime`}><Message id="tenant.accountRuntime" /></label><Input id={`${prefix}-runtime`} value={runtime} disabled />
      </> : <AccountTargetPicker inventory={inventory} nodeId={node} runtimeId={runtime} disabled={pending} onSelect={(nextNode, nextRuntime, nextName) => {
        setNode(nextNode); setRuntime(nextRuntime);
        if (nextName) {
          setName(previous => previous === "" || previous === autoName ? nextName : previous);
          setAutoName(nextName);
        }
      }} />}
      <label htmlFor={`${prefix}-price`}><Message id="tenant.accountPrice" /></label><Input id={`${prefix}-price`} required inputMode="decimal" value={rawPrice} disabled={pending} onChange={e => setPrice(e.target.value)} />
      <label className="flex items-center gap-2"><input type="checkbox" checked={active} disabled={pending} onChange={e => setActive(e.target.checked)} /><Message id="tenant.accountListed" /></label>
      <FormError message={error === null ? null : <ErrorMessage error={error} />} />
      <div className="flex gap-3"><Button type="submit" disabled={pending || !node || !runtime || price === null || price <= 0 || price > MAX_PRICE_MICRO_PER_MTOK}><Message id="tenant.accountSave" /></Button><Button type="button" variant="outline" disabled={pending} onClick={onClose}><Message id="common.cancel" /></Button></div>
    </form></CardContent>
  </Card>;
}

/** Only Agent-reported CLI accounts can be selected when creating an offer. */
function AccountTargetPicker({ inventory, nodeId, runtimeId, disabled, onSelect }: {
  inventory: AccountSale[]; nodeId: string; runtimeId: string; disabled: boolean;
  onSelect: (nodeId: string, runtimeId: string, name: string) => void;
}) {
  const { t } = useI18n();
  const prefix = React.useId();
  const fleet = useResource({ method: "GET", surface: "operator", path: "/operator/v1/nodes", parse: parseFleetSnapshot });
  const nodes = (fleet.data?.nodes ?? []).filter(node => tenantNodeOwner(node.nodeId) === null).map(node => ({
    ...node, runtimes: node.runtimes.filter(runtime => (runtime.kind === "claude" || runtime.kind === "codex") &&
      !inventory.some(account => account.nodeId === node.nodeId && account.runtimeId === runtime.id)),
  })).filter(node => node.runtimes.length > 0);
  const selected = nodes.find(node => node.nodeId === nodeId);
  const label = (runtime: { kind: string; id: string }) => `${runtime.kind === "claude" ? "Claude Code" : "Codex"} · ${runtime.id}`;
  const selectClass = "h-10 w-full min-w-0 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50";
  const refresh = () => { onSelect("", "", ""); fleet.reload(); };
  const unavailable = disabled || fleet.loading || fleet.error !== null;
  return <div className="grid min-w-0 gap-3">
    <Button type="button" variant="outline" className="justify-self-start" disabled={disabled || fleet.loading} onClick={refresh}><Message id="common.refresh" /></Button>
    {fleet.loading ? <LoadingState label={t("tenant.accountSelectAgent")} rows={1} /> : null}
    {fleet.error ? <ErrorState message={fleet.error} onRetry={refresh} /> : null}
    {fleet.data?.partial ? <p role="status" className="text-sm text-muted-foreground"><Message id="tenant.accountFleetPartial" /></p> : null}
    {fleet.data && nodes.length === 0 ? <EmptyState title={t("tenant.accountNoTargets")} description={t("tenant.accountConnectHint")} /> : null}
    <label htmlFor={`${prefix}-agent`}><Message id="tenant.accountSelectAgent" /></label>
    <select id={`${prefix}-agent`} className={selectClass} required disabled={unavailable || nodes.length === 0} value={nodeId} onChange={event => onSelect(event.target.value, "", "")}>
      <option value=""><Message id="tenant.accountSelectAgent" /></option>
      {nodes.map(node => <option key={node.nodeId} value={node.nodeId}>{node.nodeId}{node.live ? "" : ` (${t("tenant.accountOffline")})`}</option>)}
    </select>
    <label htmlFor={`${prefix}-account`}><Message id="tenant.accountSelectRuntime" /></label>
    <select id={`${prefix}-account`} className={selectClass} required disabled={unavailable || !selected} value={runtimeId} onChange={event => {
      const runtime = selected?.runtimes.find(runtime => runtime.id === event.target.value);
      onSelect(nodeId, runtime?.id ?? "", runtime ? label(runtime) : "");
    }}>
      <option value=""><Message id="tenant.accountSelectRuntime" /></option>
      {selected?.runtimes.map(runtime => <option key={runtime.id} value={runtime.id}>{label(runtime)}</option>)}
    </select>
    <p className="text-sm text-muted-foreground"><Message id="tenant.accountTargetHint" /></p>
  </div>;
}

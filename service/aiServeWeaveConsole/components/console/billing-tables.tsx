"use client";

import * as React from "react";

import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Pager } from "@/components/console/pager";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  formatCredits,
  formatMoney,
  formatSignedCredits,
  LEDGER_KINDS,
  ORDER_STATUSES,
  parseLedgerPage,
  parseOrderPage,
  type CheckoutOrder,
  type LedgerEntry,
} from "@/lib/console/billing";
import { formatDateTime } from "@/lib/console/format";
import type { MessageKey } from "@/lib/i18n/messages";
import type { Translator } from "@/lib/i18n/translate";

/** PAGE_SIZE is the ledger and order page size.
 *
 * PAGE_SIZE 是账目与订单的分页大小。 */
const PAGE_SIZE = 20;

const KIND_KEYS: Record<(typeof LEDGER_KINDS)[number], MessageKey> = {
  grant: "tenant.billingKindGrant",
  topup: "tenant.billingKindTopup",
  usage: "tenant.billingKindUsage",
  refund: "tenant.billingKindRefund",
  dispute: "tenant.billingKindDispute",
  dispute_reversal: "tenant.billingKindDisputeReversal",
  adjust: "tenant.billingKindAdjust",
};

const STATUS_KEYS: Record<(typeof ORDER_STATUSES)[number], MessageKey> = {
  pending: "tenant.billingStatusPending",
  paid: "tenant.billingStatusPaid",
  failed: "tenant.billingStatusFailed",
  refunded: "tenant.billingStatusRefunded",
  disputed: "tenant.billingStatusDisputed",
};

/** ledgerKindLabel names a ledger kind, falling back to the raw value so a
 * build that does not know a newer kind still shows something.
 *
 * ledgerKindLabel 给出账目类型的名称；不认识的较新类型回退为原始值，仍能显示点什么。 */
export function ledgerKindLabel(kind: string, t: Translator): string {
  return kind in KIND_KEYS ? t(KIND_KEYS[kind as keyof typeof KIND_KEYS]) : kind;
}

/** orderStatusLabel names an order status, with the same fallback.
 *
 * orderStatusLabel 给出订单状态的名称，回退方式相同。 */
export function orderStatusLabel(status: string, t: Translator): string {
  return status in STATUS_KEYS ? t(STATUS_KEYS[status as keyof typeof STATUS_KEYS]) : status;
}

/** BillingDisabled is the empty state for a deployment whose control plane
 * has not enabled billing: every billing route answers 404 there, and that
 * is a configuration, not a failure.
 *
 * BillingDisabled 是控制面未开启计费时的空状态：那种部署下每个计费路由都回 404，
 * 这是一种配置，而不是一次失败。 */
export function BillingDisabled() {
  const { t } = useI18n();
  return <EmptyState title={t("tenant.billingDisabled")} description={t("tenant.billingDisabledDescription")} />;
}

/** pathFor picks the endpoint for a surface: the tenant's own list or the
 * cross-tenant operator list.
 *
 * pathFor 按面选择端点：租户自己的列表，或运维的跨租户列表。 */
function pathFor(surface: "admin" | "operator", resource: "ledger" | "orders"): string {
  return surface === "operator" ? `/operator/v1/billing/${resource}` : `/admin/v1/billing/${resource}`;
}

/**
 * LedgerTable lists balance changes, newest first, a page at a time. On the
 * operator surface it shows the tenant and honours a tenant filter; on the
 * tenant surface the session already scopes it and no tenant is sent.
 *
 * LedgerTable 按时间倒序逐页列出余额变动。在运维面上它显示租户列并支持按租户筛选；
 * 在租户面上会话已限定范围，不发送任何租户参数。
 */
export function LedgerTable({ surface = "admin", tenantId = "" }: { surface?: "admin" | "operator"; tenantId?: string }) {
  const { t, locale } = useI18n();
  const [kind, setKind] = React.useState("");
  const filters: Record<string, string> = { kind };
  if (surface === "operator") filters.tenant_id = tenantId;
  const ledger = usePagedResource<LedgerEntry>({ path: pathFor(surface, "ledger"), surface, filters, pageSize: PAGE_SIZE, parse: parseLedgerPage });
  const operator = surface === "operator";
  const id = `${surface}-ledger-kind`;

  return <Card>
    <CardHeader className="flex flex-wrap items-center justify-between gap-3">
      <CardTitle><Message id="tenant.billingLedger" /></CardTitle>
      <Select value={kind === "" ? "all" : kind} onValueChange={(next) => { if (next !== null) setKind(next === "all" ? "" : next); }}>
        <SelectTrigger id={id} className="w-44" aria-label={t("tenant.billingKindFilter")}>
          <SelectValue>{kind === "" ? t("tenant.billingAllKinds") : ledgerKindLabel(kind, t)}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all"><Message id="tenant.billingAllKinds" /></SelectItem>
          {LEDGER_KINDS.map((value) => <SelectItem key={value} value={value}>{ledgerKindLabel(value, t)}</SelectItem>)}
        </SelectContent>
      </Select>
    </CardHeader>
    <CardContent className="grid gap-3">
      {ledger.errorKind === "not_found" ? <BillingDisabled /> : ledger.error ? <ErrorState message={ledger.error} onRetry={ledger.reload} /> : ledger.loading || !ledger.items ? <LoadingState label={t("tenant.billingLoadingLedger")} rows={3} /> : ledger.items.length === 0 ? <EmptyState title={t("tenant.billingNoLedger")} /> : <>
        <Table>
          <TableHeader><TableRow>
            <TableHead><Message id="tenant.time" /></TableHead>
            {operator ? <TableHead><Message id="tenant.tenantId" /></TableHead> : null}
            <TableHead><Message id="tenant.billingKind" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.billingChange" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.billingBalanceAfter" /></TableHead>
            <TableHead><Message id="tenant.billingReference" /></TableHead>
          </TableRow></TableHeader>
          <TableBody>{ledger.items.map((entry) => <TableRow key={entry.id}>
            <TableCell className="whitespace-nowrap">{formatDateTime(entry.createdAt, undefined, locale)}</TableCell>
            {operator ? <TableCell><code className="text-xs break-all">{entry.tenantId}</code></TableCell> : null}
            <TableCell className="whitespace-nowrap">{ledgerKindLabel(entry.kind, t)}</TableCell>
            <TableCell className={`text-right font-mono whitespace-nowrap ${entry.amountMicro < 0 ? "text-destructive" : ""}`}>{formatSignedCredits(entry.amountMicro, locale)}</TableCell>
            <TableCell className="text-right font-mono whitespace-nowrap">{formatCredits(entry.balanceAfterMicro, locale)}</TableCell>
            <TableCell className="max-w-64"><span className="text-xs break-all">{entry.ref || "—"}</span></TableCell>
          </TableRow>)}</TableBody>
        </Table>
        <Pager resource={ledger} loadedCount={ledger.items.length} />
      </>}
    </CardContent>
  </Card>;
}

/**
 * OrdersTable lists checkout orders, newest first, with the same surface
 * rules as LedgerTable. An order's status is what the server recorded from
 * the provider's webhook — never what a return URL claimed.
 *
 * OrdersTable 按时间倒序列出结账订单，面向规则与 LedgerTable 相同。订单状态是服务端
 * 依据服务商 webhook 记录的结果——绝不是回跳 URL 声称的结果。
 */
export function OrdersTable({ surface = "admin", tenantId = "" }: { surface?: "admin" | "operator"; tenantId?: string }) {
  const { t, locale } = useI18n();
  const [status, setStatus] = React.useState("");
  const filters: Record<string, string> = { status };
  if (surface === "operator") filters.tenant_id = tenantId;
  const orders = usePagedResource<CheckoutOrder>({ path: pathFor(surface, "orders"), surface, filters, pageSize: PAGE_SIZE, parse: parseOrderPage });
  const operator = surface === "operator";

  return <Card>
    <CardHeader className="flex flex-wrap items-center justify-between gap-3">
      <CardTitle><Message id="tenant.billingOrders" /></CardTitle>
      <Select value={status === "" ? "all" : status} onValueChange={(next) => { if (next !== null) setStatus(next === "all" ? "" : next); }}>
        <SelectTrigger id={`${surface}-order-status`} className="w-44" aria-label={t("tenant.billingStatusFilter")}>
          <SelectValue>{status === "" ? t("tenant.allStatuses") : orderStatusLabel(status, t)}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all"><Message id="tenant.allStatuses" /></SelectItem>
          {ORDER_STATUSES.map((value) => <SelectItem key={value} value={value}>{orderStatusLabel(value, t)}</SelectItem>)}
        </SelectContent>
      </Select>
    </CardHeader>
    <CardContent className="grid gap-3">
      {orders.errorKind === "not_found" ? <BillingDisabled /> : orders.error ? <ErrorState message={orders.error} onRetry={orders.reload} /> : orders.loading || !orders.items ? <LoadingState label={t("tenant.billingLoadingOrders")} rows={3} /> : orders.items.length === 0 ? <EmptyState title={t("tenant.billingNoOrders")} /> : <>
        <Table>
          <TableHeader><TableRow>
            <TableHead><Message id="tenant.time" /></TableHead>
            {operator ? <TableHead><Message id="tenant.tenantId" /></TableHead> : null}
            <TableHead><Message id="tenant.billingOrderId" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.billingAmount" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.billingCredits" /></TableHead>
            <TableHead><Message id="tenant.status" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.billingRefunded" /></TableHead>
          </TableRow></TableHeader>
          <TableBody>{orders.items.map((order) => <TableRow key={order.id}>
            <TableCell className="whitespace-nowrap">{formatDateTime(order.createdAt, undefined, locale)}</TableCell>
            {operator ? <TableCell><code className="text-xs break-all">{order.tenantId}</code></TableCell> : null}
            <TableCell><code className="text-xs break-all">{order.id}</code></TableCell>
            <TableCell className="text-right whitespace-nowrap">{formatMoney(order.amountMinor, order.currency, locale)}</TableCell>
            <TableCell className="text-right font-mono whitespace-nowrap">{formatCredits(order.creditsMicro, locale)}</TableCell>
            <TableCell><Badge variant={order.status === "paid" ? "default" : order.status === "failed" || order.status === "disputed" ? "destructive" : "outline"}>{orderStatusLabel(order.status, t)}</Badge></TableCell>
            <TableCell className="text-right whitespace-nowrap">{order.refundedMinor > 0 ? formatMoney(order.refundedMinor, order.currency, locale) : "—"}</TableCell>
          </TableRow>)}</TableBody>
        </Table>
        <Pager resource={orders} loadedCount={orders.items.length} />
      </>}
    </CardContent>
  </Card>;
}

/** TenantIdFilter is the operator's tenant filter for orders and ledger.
 *
 * TenantIdFilter 是运维对订单与账目的租户筛选框。 */
export function TenantIdFilter({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const { t } = useI18n();
  return <Input className="max-w-xs" aria-label={t("operator.billingTenantFilter")} placeholder={t("operator.billingTenantFilter")} value={value} onChange={(event) => onChange(event.target.value.trim())} />;
}

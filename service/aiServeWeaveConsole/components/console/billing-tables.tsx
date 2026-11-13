"use client";

import * as React from "react";

import { ErrorMessage } from "@/components/i18n/error-message";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Pager } from "@/components/console/pager";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
  parseCheckoutOrder,
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
  account_purchase: "tenant.accountPurchase",
  alipay_topup: "tenant.billingKindAlipayTopup",
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

/** orderMethodLabel names how an order was paid: Dodo, EZFP with its payment
 * type, or 一人付 Alipay quota. An EZFP order without a type says only that
 * it went through EZFP.
 *
 * orderMethodLabel 给出订单的付款方式：Dodo、带支付类型的易支付，或一人付的支付宝额度。
 * 没有类型的易支付订单只说明它经由易支付。 */
export function orderMethodLabel(order: Pick<CheckoutOrder, "provider" | "payType">, t: Translator): string {
  switch (order.provider) {
    case "opcpay":
      return t("tenant.billingProviderOpcpay");
    case "ezfp":
      return order.payType === "alipay" ? t("tenant.billingProviderEzfpAlipay")
        : order.payType === "wxpay" ? t("tenant.billingProviderEzfpWxpay")
        : order.payType === "usdt" ? t("tenant.billingProviderEzfpUsdt")
        : t("tenant.billingProviderEzfp");
    case "dodo":
      return t("tenant.billingProviderDodo");
    default:
      // A provider this Console predates is shown as sent.
      // 本 Console 早于其出现的服务商按原值显示。
      return order.provider;
  }
}

/** recheckable says whether a tenant may ask for an EZFP order to be
 * re-checked: it is an EZFP order still waiting for payment.
 *
 * recheckable 说明租户能否请求复核一笔易支付订单：它是仍在等待付款的易支付订单。 */
function recheckable(order: CheckoutOrder): boolean {
  return order.provider === "ezfp" && order.status === "pending";
}

/** RecheckButton asks the control plane to query EZFP for one pending order
 * and settle it if paid. Each click sends one request and never retries; the
 * control plane spaces calls per order and answers a call that comes too soon
 * with the order as it is. The answer is the order the server holds: the row
 * shows it, and the button says so when it is still pending.
 *
 * RecheckButton 请控制面向易支付查询一笔待支付订单，已付款则入账。每次点击只发送一次
 * 请求、绝不重试；控制面按订单限制调用间隔，过早的调用会得到订单的现状。答复就是服务端
 * 持有的订单：该行据此显示，仍待支付时按钮会如实说明。 */
function RecheckButton({ order, onUpdated }: { order: CheckoutOrder; onUpdated: (order: CheckoutOrder) => void }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const [pending, setPending] = React.useState(false);
  const [notice, setNotice] = React.useState<React.ReactNode>(null);

  async function recheck() {
    if (pending) return;
    setPending(true);
    setNotice(null);
    try {
      const updated = await run({ method: "POST", path: `/admin/v1/billing/orders/${encodeURIComponent(order.id)}/refresh`, parse: parseCheckoutOrder });
      if (updated.status === "pending") {
        setNotice(<Message id="tenant.billingRecheckStillPending" />);
      }
      onUpdated(updated);
    } catch (failure) {
      setNotice(<ErrorMessage error={failure} />);
    } finally {
      setPending(false);
    }
  }

  return <div className="grid justify-items-start gap-1">
    <Button size="sm" variant="outline" onClick={recheck} disabled={pending} aria-busy={pending} title={t("tenant.billingRecheckHint")}><Message id="tenant.billingRecheck" /></Button>
    {notice ? <p role="status" aria-live="polite" className="max-w-48 text-xs text-muted-foreground">{notice}</p> : null}
  </div>;
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
  // Rows a re-check has updated, kept only for the page they were read with:
  // once the list is read again, the server's rows win.
  // 已被复核更新的行，只对读取它们的那一页有效：列表重新读取后，以服务端的行为准。
  const [patch, setPatch] = React.useState<{ base: CheckoutOrder[] | null; rows: Record<string, CheckoutOrder> }>({ base: null, rows: {} });
  const patched = patch.base === orders.items ? patch.rows : {};
  const rows = orders.items?.map((order) => patched[order.id] ?? order) ?? null;
  const showRecheck = !operator && (rows?.some(recheckable) ?? false);

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
      {orders.errorKind === "not_found" ? <BillingDisabled /> : orders.error ? <ErrorState message={orders.error} onRetry={orders.reload} /> : orders.loading || !orders.items ? <LoadingState label={t("tenant.billingLoadingOrders")} rows={3} /> : orders.items.length === 0 || rows === null ? <EmptyState title={t("tenant.billingNoOrders")} /> : <>
        <Table>
          <TableHeader><TableRow>
            <TableHead><Message id="tenant.time" /></TableHead>
            {operator ? <TableHead><Message id="tenant.tenantId" /></TableHead> : null}
            <TableHead><Message id="tenant.billingOrderId" /></TableHead>
            <TableHead><Message id="tenant.billingPayMethod" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.billingAmount" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.billingOrderContent" /></TableHead>
            <TableHead><Message id="tenant.status" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.billingRefunded" /></TableHead>
            {showRecheck ? <TableHead><span className="sr-only"><Message id="tenant.action" /></span></TableHead> : null}
          </TableRow></TableHeader>
          <TableBody>{rows.map((order) => <TableRow key={order.id}>
            <TableCell className="whitespace-nowrap">{formatDateTime(order.createdAt, undefined, locale)}</TableCell>
            {operator ? <TableCell><code className="text-xs break-all">{order.tenantId}</code></TableCell> : null}
            <TableCell><code className="text-xs break-all">{order.id}</code></TableCell>
            <TableCell className="whitespace-nowrap">{orderMethodLabel(order, t)}</TableCell>
            <TableCell className="text-right whitespace-nowrap">{order.alipayAmount > 0 ? t("operator.presaleAlipayPaid", { amount: order.alipayAmount }) : formatMoney(order.amountMinor, order.currency, locale)}</TableCell>
            <TableCell className="text-right whitespace-nowrap">{order.presaleId !== "" && order.accountExpiresAt === null
              ? order.status === "paid" ? <span className="font-medium">{t("tenant.presaleOrderAwaiting")}</span> : t("tenant.presale")
              : order.accountId === ""
              ? <span className="font-mono">{t("tenant.billingCreditsValue", { credits: formatCredits(order.creditsMicro, locale) })}</span>
              : order.accountExpiresAt
                ? t("tenant.accountOrderGranted", { time: formatDateTime(order.accountExpiresAt, undefined, locale) })
                : order.status === "paid" ? <span className="text-destructive">{t("tenant.accountOrderUngranted")}</span> : t("tenant.accountMonthly")}</TableCell>
            <TableCell><Badge variant={order.status === "paid" ? "default" : order.status === "failed" || order.status === "disputed" ? "destructive" : "outline"}>{orderStatusLabel(order.status, t)}</Badge></TableCell>
            <TableCell className="text-right whitespace-nowrap">{order.refundedMinor > 0 ? formatMoney(order.refundedMinor, order.currency, locale) : "—"}</TableCell>
            {showRecheck ? <TableCell>{recheckable(order) ? <RecheckButton order={order} onUpdated={(updated) => setPatch({ base: orders.items, rows: { ...patched, [updated.id]: updated } })} /> : null}</TableCell> : null}
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

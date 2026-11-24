"use client";

import Link from "next/link";
import { useResource } from "@/components/console/use-resource";
import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import { LocalDateTime } from "@/components/i18n/local-date-time";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { buttonVariants } from "@/components/ui/button";
import { formatCredits, parseBillingOverview, type BillingOverview } from "@/lib/console/billing";
import { roleLabels, type Role } from "@/lib/console/contract";
import type { SessionUser } from "@/lib/console/session-payload";
import { canCreateApiKey } from "@/lib/console/permissions";

/** OverviewView renders session facts using the current display language.
 * OverviewView 使用当前显示语言渲染会话事实。 */
export function OverviewView({ user, expiresAt }: { user: SessionUser; expiresAt: string }) {
  const { locale, t } = useI18n();

  const facts: { label: string; value: React.ReactNode }[] = [
    { label: t("tenant.signInEmail"), value: user.email },
    { label: t("tenant.name_be4c26"), value: user.name || t("tenant.notProvided") },
    { label: t("tenant.role"), value: roleLabels(locale)[user.role as Role] ?? user.role },
    { label: t("tenant.tenantId"), value: user.tenantId },
    { label: t("tenant.userId"), value: user.id },
    {
      label: t("tenant.sessionExpires"),
      value: <LocalDateTime value={expiresAt} />,
    },
  ];

  return (
    <div className="grid gap-6">
      <div>
        <h1 className="font-heading text-xl font-semibold tracking-tight"><Message id="tenant.overview" /></h1>
        <p className="text-sm text-muted-foreground">
          <Message id="tenant.currentSessionInformationUsersApiKeysAdministrativeAudits" /></p>
      </div>

      <BalanceCard />

      <Card>
        <CardHeader>
          <CardTitle><Message id="tenant.quickStart" /></CardTitle>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-2">
          {canCreateApiKey(user.role) ? (
            <Link href="/console/keys" className={buttonVariants({ variant: "outline" })}><Message id="tenant.createApiKey" /></Link>
          ) : null}
          <Link href="/console/docs/clients" className={buttonVariants({ variant: "outline" })}><Message id="tenant.docsClientsTitle" /></Link>
          <Link href="/console/docs#quick-start" className={buttonVariants({ variant: "outline" })}><Message id="tenant.quickStartExample" /></Link>
          <Link href="/console/usage" className={buttonVariants({ variant: "outline" })}><Message id="tenant.usage" /></Link>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle><Message id="tenant.currentSession" /></CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
            {facts.map((fact) => (
              <div key={fact.label} className="grid gap-1">
                <dt className="text-xs text-muted-foreground">{fact.label}</dt>
                <dd className="font-mono text-sm break-all">{fact.value}</dd>
              </div>
            ))}
          </dl>
        </CardContent>
      </Card>
    </div>
  );
}

/** BalanceCard shows the tenant's remaining credits. It renders nothing while
 * loading, when billing is off (404) or when the read fails: the overview
 * should not be blocked by billing.
 *
 * BalanceCard 展示租户剩余点数。加载中、计费未开启（404）或读取失败时不渲染：
 * 概览页不应被计费阻塞。 */
function BalanceCard() {
  const { t, locale } = useI18n();
  const overview = useResource<BillingOverview>({ method: "GET", path: "/admin/v1/billing", parse: parseBillingOverview });
  const data = overview.data;
  if (!data) return null;
  const exempt = data.mode === "exempt";
  return (
    <Card>
      <CardContent className="grid gap-3">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <dl className="grid min-w-0 gap-1">
            <dt className="text-xs text-muted-foreground"><Message id="tenant.billingBalance" /></dt>
            <dd className="font-heading text-2xl font-semibold break-all">{t("tenant.billingCreditsValue", { credits: formatCredits(data.balanceMicro, locale) })}</dd>
          </dl>
          <Link href="/console/billing" className={buttonVariants({ variant: "outline" })}><Message id="tenant.billing" /></Link>
        </div>
        {!exempt && data.blocked ? <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm font-medium text-destructive"><Message id="tenant.billingBlocked" /></p> : null}
      </CardContent>
    </Card>
  );
}

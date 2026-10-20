"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import { LocalDateTime } from "@/components/i18n/local-date-time";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { roleLabels, type Role } from "@/lib/console/contract";
import type { SessionUser } from "@/lib/console/session-payload";

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
        <h1 className="font-heading text-lg font-semibold"><Message id="tenant.overview" /></h1>
        <p className="text-sm text-muted-foreground">
          <Message id="tenant.currentSessionInformationUsersApiKeysAdministrativeAudits" /></p>
      </div>

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

      <Card>
        <CardHeader>
          <CardTitle><Message id="tenant.currentBoundaries" /></CardTitle>
        </CardHeader>
        <CardContent>
          <ul className="grid gap-2 text-sm text-muted-foreground">
            <li>
              <Message id="tenant.theConsoleCallsOnlyTheControlPlaneS" /></li>
            <li>
              <Message id="tenant.signingOutClearsOnlyThisConsoleSSession" /></li>
            <li>
              <Message id="tenant.theRoleShownOnThePageOnlyDetermines" /></li>
            <li>
              <Message id="tenant.userApiKeyAndAuditListsArePaginated" /></li>
            <li>
              <Message id="tenant.theRunsPageIsALiveViewNot" /></li>
          </ul>
        </CardContent>
      </Card>
    </div>
  );
}

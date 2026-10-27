"use client";
import { ErrorMessage } from "@/components/i18n/error-message";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";
import { LanguageSwitcher } from "@/components/i18n/language-switcher";


import * as React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { FormError } from "@/components/console/states";
import { operatorSignOut } from "@/lib/console/operator-client";
import type { OperatorIdentity } from "@/lib/console/operator-session";

function navigationItems(t: Translator) { return [
  { href: "/operator/fleet", label: t("operator.fleetApprovals") },
  { href: "/operator/models", label: t("tenant.models_98fd0c") },
  { href: "/operator/routes", label: t("tenant.modelRoutes") },
  { href: "/operator/model-access", label: t("operator.modelAccess") },
  { href: "/operator/workflow-templates", label: t("operator.workflowTemplates") },
  { href: "/operator/workflows", label: t("operator.releaseStatus") },
  { href: "/operator/api-docs", label: t("tenant.apiDocumentation") },
  { href: "/operator/comfyui-managed", label: "ComfyUI Managed" },
  { href: "/operator/model-pulls", label: t("operator.modelPulls") },
  { href: "/operator/audit", label: t("operator.operationsAudit") },
  { href: "/operator/requests", label: t("tenant.requestSearch") },
  { href: "/operator/metrics", label: t("operator.metrics") },
  { href: "/operator/usage", label: t("tenant.usage") },
  { href: "/operator/alert-rules", label: t("operator.alertRules") },
  { href: "/operator/alerts", label: t("operator.alerts") },
  { href: "/operator/operators", label: t("operator.operatorAccounts") },
  { href: "/operator/account", label: t("tenant.accountSecurity") },
]; }

/** OperatorShell renders only platform identity and navigation. / OperatorShell 仅渲染平台身份与导航。 */
export function OperatorShell({ operator, children }: { operator: OperatorIdentity; children: React.ReactNode }) {
  const { t } = useI18n();
  const navigation = navigationItems(t);
  const router = useRouter();
  const pathname = usePathname();
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  async function leave() {
    setPending(true);
    setError(null);
    try {
      await operatorSignOut();
      router.replace("/operator/login");
      router.refresh();
    } catch (failure) { setError(<ErrorMessage error={failure} />); setPending(false); }
  }
  return <div className="flex min-h-full flex-1 flex-col">
    <header className="border-b"><div className="mx-auto flex max-w-6xl flex-wrap items-center gap-3 px-4 py-3">
      <span className="font-heading font-semibold"><Message id="auth.aiserveweavePlatformOperations" /></span>
      <nav aria-label={t("operator.platformOperationsNavigation")} className="flex flex-wrap gap-1">{navigation.map(item => <Button key={item.href} variant={pathname === item.href ? "secondary" : "ghost"} size="sm" render={<Link href={item.href} />} aria-current={pathname === item.href ? "page" : undefined}>{item.label}</Button>)}</nav>
      <LanguageSwitcher />
      <Link href="/console" className="text-sm underline"><Message id="operator.tenantConsole" /></Link>
      <span className="ml-auto max-w-56 break-all text-sm">{operator.name || operator.email}</span>
      <Button variant="outline" size="sm" onClick={leave} disabled={pending} aria-busy={pending}><Message id="operator.signOutOfOperations" /></Button>
      <FormError message={error} />
    </div></header>
    <main className="mx-auto w-full min-w-0 max-w-6xl flex-1 wrap-anywhere px-4 py-6">{children}</main>
  </div>;
}

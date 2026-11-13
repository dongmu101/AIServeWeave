"use client";
import { ErrorMessage } from "@/components/i18n/error-message";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";
import { LanguageSwitcher } from "@/components/i18n/language-switcher";


import * as React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { LogOutIcon } from "lucide-react";
import { BrandMark } from "@/components/console/brand-mark";
import { Button, buttonVariants } from "@/components/ui/button";
import { FormError } from "@/components/console/states";
import { operatorSignOut } from "@/lib/console/operator-client";
import type { OperatorIdentity } from "@/lib/console/operator-session";

function navigationItems(t: Translator) { return [
  { href: "/operator/fleet", label: t("operator.fleetApprovals") },
  { href: "/operator/agent-enrollment", label: t("operator.agentEnrollment") },
  { href: "/operator/models", label: t("tenant.models_98fd0c") },
  { href: "/operator/routes", label: t("tenant.modelRoutes") },
  { href: "/operator/model-access", label: t("operator.modelAccess") },
  { href: "/operator/node-access", label: t("operator.nodeAccess") },
  { href: "/operator/workflow-templates", label: t("operator.workflowTemplates") },
  { href: "/operator/workflows", label: t("operator.releaseStatus") },
  { href: "/operator/api-docs", label: t("tenant.apiDocumentation") },
  { href: "/operator/comfyui-managed", label: "ComfyUI Managed" },
  { href: "/operator/model-pulls", label: t("operator.modelPulls") },
  { href: "/operator/node-providers", label: t("operator.nodeProviders") },
  { href: "/operator/audit", label: t("operator.operationsAudit") },
  { href: "/operator/requests", label: t("tenant.requestSearch") },
  { href: "/operator/metrics", label: t("operator.metrics") },
  { href: "/operator/usage", label: t("tenant.usage") },
  { href: "/operator/billing", label: t("operator.billing") },
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
  const navigationRef = React.useRef<HTMLElement>(null);
  // Keep the current entry in view when the phone row scrolls. / 手机上导航横向滚动时，让当前条目保持可见。
  React.useEffect(() => {
    navigationRef.current?.querySelector('[aria-current="page"]')?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [pathname]);
  async function leave() {
    setPending(true);
    setError(null);
    try {
      await operatorSignOut();
      router.replace("/operator/login");
      router.refresh();
    } catch (failure) { setError(<ErrorMessage error={failure} />); setPending(false); }
  }
  return <div className="flex min-h-full flex-1 flex-col bg-muted/40">
    <header className="border-b">
      <div className="mx-auto flex min-h-14 max-w-6xl flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2">
        <span className="mr-auto flex items-center gap-2 font-heading text-sm font-semibold"><BrandMark /><Message id="auth.aiserveweavePlatformOperations" /></span>
        <LanguageSwitcher />
        <Link href="/console" className={buttonVariants({ variant: "ghost", size: "sm" })}><Message id="operator.tenantConsole" /></Link>
        <span className="hidden max-w-40 truncate text-sm text-muted-foreground sm:inline" title={operator.name || operator.email}>{operator.name || operator.email}</span>
        <Button variant="outline" size="sm" onClick={leave} disabled={pending} aria-busy={pending}><LogOutIcon /><Message id="operator.signOutOfOperations" /></Button>
        <FormError message={error} />
      </div>
      {/* One scrolling row on a phone, wrapped rows from md up. / 手机上是一行横向滚动，md 起换行排列。 */}
      <nav ref={navigationRef} aria-label={t("operator.platformOperationsNavigation")} className="mx-auto flex max-w-6xl gap-1 overflow-x-auto px-4 pb-2 md:flex-wrap">{navigation.map(item => {
        const active = pathname === item.href;
        return <Link key={item.href} href={item.href} aria-current={active ? "page" : undefined} className={buttonVariants({ variant: "ghost", size: "sm", className: active ? "bg-primary text-primary-foreground hover:bg-primary hover:text-primary-foreground" : "text-muted-foreground" })}>{item.label}</Link>;
      })}</nav>
    </header>
    <main className="mx-auto w-full min-w-0 max-w-6xl flex-1 wrap-anywhere px-4 py-6">{children}</main>
  </div>;
}

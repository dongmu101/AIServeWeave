"use client";

import * as React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import {
  BellIcon,
  BellRingIcon,
  BookOpenIcon,
  BoxesIcon,
  ChartColumnIcon,
  ChartLineIcon,
  ContainerIcon,
  CreditCardIcon,
  DownloadIcon,
  LayoutDashboardIcon,
  LayoutTemplateIcon,
  LockKeyholeIcon,
  LogOutIcon,
  PlugIcon,
  RocketIcon,
  RouteIcon,
  ScrollTextIcon,
  SearchIcon,
  ServerIcon,
  ShieldCheckIcon,
  TerminalIcon,
  UserRoundIcon,
  UsersIcon,
} from "lucide-react";
import { toast } from "sonner";

import { ErrorMessage } from "@/components/i18n/error-message";
import { LanguageSwitcher } from "@/components/i18n/language-switcher";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { BrandMark } from "@/components/console/brand-mark";
import { TopNav, type NavEntry } from "@/components/console/top-nav";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { operatorSignOut } from "@/lib/console/operator-client";
import type { OperatorIdentity } from "@/lib/console/operator-session";
import type { Translator } from "@/lib/i18n/translate";

/**
 * navigationItems groups the platform pages by what an operator is looking
 * after: the machines, the model catalog, workflow releases, what is
 * happening, and what it costs. Operator accounts and the operator's own
 * security sit in the account menu, as they do in the tenant console.
 *
 * navigationItems 按运维关注的对象给平台页面分组：机器、模型目录、工作流发布、
 * 运行状况与费用。运维账户与本人账户安全与租户控制台一样放在账户菜单里。
 */
function navigationItems(t: Translator): readonly NavEntry[] { return [
  {
    label: t("operator.navNodes"),
    children: [
      { href: "/operator/fleet", label: t("operator.fleetApprovals"), icon: ServerIcon },
      { href: "/operator/agent-enrollment", label: t("operator.agentEnrollment"), icon: TerminalIcon },
      { href: "/operator/node-access", label: t("operator.sharingSettings"), icon: LockKeyholeIcon },
      { href: "/operator/node-providers", label: t("operator.nodeProviders"), icon: PlugIcon },
      { href: "/operator/model-pulls", label: t("operator.modelPulls"), icon: DownloadIcon },
      { href: "/operator/comfyui-managed", label: "ComfyUI Managed", icon: ContainerIcon },
    ],
  },
  {
    label: t("tenant.models_98fd0c"),
    children: [
      { href: "/operator/models", label: t("tenant.models_98fd0c"), icon: BoxesIcon },
      { href: "/operator/routes", label: t("tenant.modelRoutes"), icon: RouteIcon },
      { href: "/operator/model-access", label: t("operator.modelAccess"), icon: ShieldCheckIcon },
    ],
  },
  {
    label: t("tenant.workflows"),
    children: [
      { href: "/operator/workflow-templates", label: t("operator.workflowTemplates"), icon: LayoutTemplateIcon },
      { href: "/operator/workflows", label: t("operator.releaseStatus"), icon: RocketIcon },
    ],
  },
  {
    label: t("operator.navMonitoring"),
    children: [
      { href: "/operator/metrics", label: t("operator.metrics"), icon: ChartLineIcon },
      { href: "/operator/requests", label: t("tenant.requestSearch"), icon: SearchIcon },
      { href: "/operator/alerts", label: t("operator.alerts"), icon: BellRingIcon },
      { href: "/operator/alert-rules", label: t("operator.alertRules"), icon: BellIcon },
      { href: "/operator/audit", label: t("operator.operationsAudit"), icon: ScrollTextIcon },
    ],
  },
  {
    label: t("operator.navBilling"),
    children: [
      { href: "/operator/usage", label: t("tenant.usage"), icon: ChartColumnIcon },
      { href: "/operator/billing", label: t("operator.billing"), icon: CreditCardIcon },
    ],
  },
  { href: "/operator/api-docs", label: t("tenant.apiDocumentation"), icon: BookOpenIcon },
]; }

/** OperatorShell renders only platform identity and navigation. / OperatorShell 仅渲染平台身份与导航。 */
export function OperatorShell({ operator, children }: { operator: OperatorIdentity; children: React.ReactNode }) {
  const { t } = useI18n();
  const router = useRouter();
  const pathname = usePathname();
  const [leaving, setLeaving] = React.useState(false);
  const displayName = operator.name || operator.email;
  const initial = Array.from(displayName.trim())[0]?.toUpperCase() || "O";

  async function leave() {
    setLeaving(true);
    try {
      await operatorSignOut();
    } catch (failure) {
      toast.error(<ErrorMessage error={failure} />);
      setLeaving(false);
      return;
    }
    router.replace("/operator/login");
    router.refresh();
  }

  return (
    <div className="flex h-dvh shrink-0 flex-col overflow-hidden">
      <header className="h-14 shrink-0 border-b bg-muted/40">
        <div className="mx-auto flex h-full w-full max-w-[96rem] items-center gap-3 px-4">
          <Link href="/operator/fleet" className="flex shrink-0 items-center gap-2 whitespace-nowrap font-heading text-sm font-semibold">
            <BrandMark />
            <span className="sr-only sm:not-sr-only">
              AIServeWeave <span className="font-normal text-muted-foreground"><Message id="tenant.platformOperations" /></span>
            </span>
          </Link>
          <TopNav
            entries={navigationItems(t)}
            pathname={pathname}
            label={t("operator.platformOperationsNavigation")}
            menuLabel={t("operator.openNavigation")}
            fallbackLabel={t("tenant.navigation")}
          />
          <LanguageSwitcher />
          <DropdownMenu>
            <DropdownMenuTrigger
              aria-label={t("tenant.accountMenu")}
              disabled={leaving}
              render={<Button variant="outline" size="icon" className="shrink-0 rounded-full border-brand/50 bg-brand/20 text-xs font-semibold text-accent-foreground hover:bg-brand/30 hover:text-accent-foreground aria-expanded:bg-brand/30 aria-expanded:text-accent-foreground" />}
            >
              <span aria-hidden="true">{initial}</span>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-72 max-w-[calc(100vw-2rem)]" aria-label={t("tenant.accountMenu")}>
              <div className="space-y-1 px-3 py-2 text-xs">
                <p className="break-all text-sm font-medium">{displayName}</p>
                {operator.name ? <p className="break-all text-muted-foreground">{operator.email}</p> : null}
              </div>
              <DropdownMenuSeparator />
              <DropdownMenuItem render={<Link href="/operator/account" />} className="min-h-9"><UserRoundIcon /><Message id="tenant.accountSecurity" /></DropdownMenuItem>
              <DropdownMenuItem render={<Link href="/operator/operators" />} className="min-h-9"><UsersIcon /><Message id="operator.operatorAccounts" /></DropdownMenuItem>
              <DropdownMenuItem render={<Link href="/console" />} className="min-h-9"><LayoutDashboardIcon /><Message id="operator.tenantConsole" /></DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={leave} disabled={leaving} aria-busy={leaving} closeOnClick={false} className="min-h-9">
                <LogOutIcon />{leaving ? t("tenant.signingOut") : t("operator.signOutOfOperations")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>
      {/* Only the content scrolls beneath the fixed-height header. / 仅正文在固定高度的页首下方滚动。 */}
      <main className="relative min-h-0 min-w-0 flex-1 overflow-y-auto bg-muted/40">
        <div className="mx-auto w-full min-w-0 max-w-6xl wrap-anywhere px-4 py-6">{children}</div>
      </main>
    </div>
  );
}

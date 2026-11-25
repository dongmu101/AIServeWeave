"use client";




import { ErrorMessage } from "@/components/i18n/error-message";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";
import { LanguageSwitcher } from "@/components/i18n/language-switcher";


import * as React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import {
  BookOpenIcon,
  BoxesIcon,
  ChartColumnIcon,
  CreditCardIcon,
  GaugeIcon,
  HistoryIcon,
  KeyRoundIcon,
  LayoutDashboardIcon,
  LayoutTemplateIcon,
  ListChecksIcon,
  LogOutIcon,
  ScrollTextIcon,
  ServerIcon,
  SettingsIcon,
  ShieldCheckIcon,
  SparklesIcon,
  UserRoundIcon,
  UsersIcon,
} from "lucide-react";
import { toast } from "sonner";

import { BrandMark } from "@/components/console/brand-mark";
import { TopNav, type NavEntry } from "@/components/console/top-nav";
import { SupportLink } from "@/components/site/support-link";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { signOut } from "@/lib/console/api-client";
import { type Role } from "@/lib/console/contract";
import type { SessionUser } from "@/lib/console/session-payload";

/**
 * NAV_ITEMS is the tenant navigation: the views every signed-in role reaches,
 * each with a real Admin API behind it. A menu entry for a page with no data
 * source would be a promise the backend cannot keep, which is why metrics is
 * absent — it has no Admin API, and no store behind one.
 *
 * NAV_ITEMS 是租户导航：每个已登录角色都能到达的视图，每一个背后都确有 Admin API。给一个
 * 没有数据来源的页面挂菜单项，等于许下后端兑现不了的承诺，这正是指标不在其中的
 * 原因——它没有 Admin API，那个 API 背后也没有存储。
 */
function navigationItems(t: Translator): readonly NavEntry[] { return [
  { href: "/console", label: t("tenant.overview"), icon: LayoutDashboardIcon },
  { href: "/console/billing", label: t("tenant.billing"), icon: CreditCardIcon },
  { href: "/console/models", label: t("tenant.aiServicesModels"), icon: BoxesIcon },
  { href: "/console/users", label: t("tenant.users"), icon: UsersIcon },
  { href: "/console/keys", label: "API Key", icon: KeyRoundIcon },
  { href: "/console/audit", label: t("tenant.audit"), icon: ScrollTextIcon },
  { href: "/console/quota", label: t("tenant.quotas"), icon: GaugeIcon },
  { href: "/console/usage", label: t("tenant.usage"), icon: ChartColumnIcon },
  { href: "/console/nodes", label: t("tenant.myNodes"), icon: ServerIcon },
  {
    label: t("tenant.workflows"),
    children: [
      { href: "/console/workflows", label: t("tenant.workflowTemplates"), icon: LayoutTemplateIcon },
      { href: "/console/jobs", label: t("tenant.runs"), icon: ListChecksIcon },
      { href: "/console/jobs/history", label: t("tenant.runHistory"), icon: HistoryIcon },
    ],
  },
  { href: "/console/docs", label: t("tenant.apiDocumentation"), icon: BookOpenIcon },
]; }

/**
 * ConsoleShell is the frame every console page renders in: navigation, who is
 * signed in, and the way out.
 *
 * The role shown here decides which controls appear and nothing else. It comes
 * from the sealed session rather than from anything the browser can edit, but
 * that is not what makes an operation safe — the control plane authorizes
 * every call regardless of what this shell chose to render.
 *
 * ConsoleShell 是每个控制台页面所在的框架：导航、当前登录者，以及退出的出口。
 *
 * 这里展示的角色只决定出现哪些控件，别无其他。它来自密封的会话而不是浏览器能改动的
 * 任何东西，但让一次操作安全的并不是这一点——无论这个外壳选择渲染了什么，控制面都会对
 * 每一次调用做授权。
 */
export function ConsoleShell({
  user,
  studio,
  children,
}: {
  user: SessionUser;
  /** studio is the Studio entry: its address and whether this Console can
   * sign the user straight in. The entry is always shown.
   *
   * studio 是 Studio 入口：地址，以及本 Console 能否直接让用户登录。入口始终显示。 */
  studio: { url: string; handoff: boolean };
  children: React.ReactNode;
}) {
  const { t } = useI18n();
  const NAV_ITEMS = navigationItems(t);
  const router = useRouter();
  const pathname = usePathname();
  const [leaving, setLeaving] = React.useState(false);
  const displayName = user.name || user.email;
  const initial = Array.from(displayName.trim())[0]?.toUpperCase() || "U";

  async function leave() {
    setLeaving(true);
    try {
      await signOut();
    } catch (failure) {
      toast.error(<ErrorMessage error={failure} />);
      setLeaving(false);
      return;
    }
    // refresh() discards the router cache along with the rendered views, so
    // the next person at this browser cannot page back into the previous
    // tenant's data.
    //
    // refresh() 会连同已渲染的视图一起丢弃路由缓存，这样在这台浏览器前的下一个人
    // 无法用后退键翻回上一个租户的数据。
    router.replace("/login");
    router.refresh();
  }

  return (
    <div className="flex h-dvh shrink-0 flex-col overflow-hidden">
      <header className="h-14 shrink-0 border-b bg-muted/40">
        <div className="mx-auto flex h-full w-full max-w-[96rem] items-center gap-3 px-4">
          <Link href="/console" className="flex shrink-0 items-center gap-2 whitespace-nowrap font-heading text-sm font-semibold">
            <BrandMark />
            <span className="sr-only sm:not-sr-only">
              AIServeWeave <span className="font-normal text-muted-foreground"><Message id="tenant.console" /></span>
            </span>
          </Link>
          <TopNav
            entries={NAV_ITEMS}
            pathname={pathname}
            label={t("tenant.consoleNavigation")}
            menuLabel={t("tenant.openConsoleNavigation")}
            fallbackLabel={t("tenant.navigation")}
          />
          <a
            href={studio.url}
            target="_blank"
            rel="noopener noreferrer"
            onClick={(event) => openStudio(event, studio)}
            aria-label={t("tenant.studio")}
            title={t("tenant.studio")}
            className={buttonVariants({ variant: "ghost", size: "sm", className: "shrink-0" })}
          >
            <SparklesIcon aria-hidden="true" className="text-brand" />
            <span className="hidden sm:inline">{t("tenant.studio")}</span>
          </a>
          <SupportLink label={t("common.telegramSupport")} />
          <LanguageSwitcher />
          <DropdownMenu>
            <DropdownMenuTrigger
              aria-label={t("tenant.accountMenu")}
              disabled={leaving}
              render={<Button variant="outline" size="icon" className="rounded-full border-brand/50 bg-brand/20 text-xs font-semibold text-accent-foreground hover:bg-brand/30 hover:text-accent-foreground aria-expanded:bg-brand/30 aria-expanded:text-accent-foreground" />}
            >
              <span aria-hidden="true">{initial}</span>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-72 max-w-[calc(100vw-2rem)]" aria-label={t("tenant.accountMenu")}>
              <div className="space-y-2 px-3 py-2 text-xs">
                <p className="break-all text-sm font-medium">{displayName}</p>
                <p className="break-all text-muted-foreground">{user.email}</p>
                <Badge variant="outline">{roleLabel(user.role, t)}</Badge>
                <p className="break-all text-muted-foreground"><Message id="tenant.tenant_fb7dcb" values={{ code: <code className="font-mono">{user.tenantId}</code> }} /></p>
              </div>
              <DropdownMenuSeparator />
              <DropdownMenuItem render={<Link href="/console/account" />} className="min-h-9"><UserRoundIcon /><Message id="tenant.accountSecurity" /></DropdownMenuItem>
              <DropdownMenuItem render={<Link href="/console/settings" />} className="min-h-9"><SettingsIcon /><Message id="tenant.settings" /></DropdownMenuItem>
              <DropdownMenuItem render={<Link href="/operator" />} className="min-h-9"><ShieldCheckIcon /><Message id="tenant.platformOperations" /></DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={leave} disabled={leaving} aria-busy={leaving} closeOnClick={false} className="min-h-9">
                <LogOutIcon />{leaving ? t("tenant.signingOut") : t("tenant.signOut")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>
      {/* Only the content scrolls beneath the compact fixed-height menu. / 仅正文在紧凑的固定高度菜单下方滚动。 */}
      <main className="relative min-h-0 min-w-0 flex-1 overflow-y-auto bg-muted/40">
        {/* The API reference is a three-column page and gets a wider frame. / API 参考文档是三栏页面，使用更宽的框架。 */}
        <div
          className={`mx-auto w-full min-w-0 wrap-anywhere px-4 py-6 ${
            pathname === "/console/docs" || pathname.startsWith("/console/docs/") ? "max-w-[96rem]" : "max-w-6xl"
          }`}
        >
          {children}
        </div>
      </main>
    </div>
  );
}

/**
 * roleLabel translates a role for display, and shows an unfamiliar one as it
 * arrived rather than guessing: a role this build does not know about is
 * something to see, not something to round off to "member".
 *
 * roleLabel 把角色翻译成展示文案，遇到不认识的则原样展示而不猜测：本次构建不认识的
 * 角色是一件需要被看见的事，而不是一件可以被约等于「成员」的事。
 */
function roleLabel(role: string, t: Translator): string {
  const key = ({ owner: "common.owner", admin: "common.administrator", member: "common.member" } as const)[role as Role];
  return key ? t(key) : role;
}

/**
 * openStudio signs the user into Studio in a new tab without a password. The
 * tab is opened synchronously, inside the click, so no popup blocker stops
 * it; the Console server then mints a ticket and the tab receives it as a
 * form post (a body, never a URL). Any failure sends the tab to Studio's own
 * sign-in. Modified clicks keep the plain link.
 *
 * openStudio 无需密码即在新分页让用户登录 Studio。分页在点击当下同步打开，弹窗拦截不会
 * 阻止它；随后 Console 服务端签出票据，分页以表单提交（请求体，绝不放进 URL）接收它。任何
 * 失败都把分页送到 Studio 自己的登录页。带修饰键的点击保留普通链接行为。
 */
async function openStudio(
  event: React.MouseEvent<HTMLAnchorElement>,
  studio: { url: string; handoff: boolean }
) {
  if (!studio.handoff || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
    return;
  }
  event.preventDefault();
  const name = `aisw-studio-${Date.now()}`;
  const tab = window.open("about:blank", name);
  try {
    const response = await fetch("/api/studio-handoff", {
      method: "POST",
      credentials: "same-origin",
      cache: "no-store",
      headers: { Accept: "application/json" },
    });
    if (!response.ok) {
      throw new Error("handoff refused");
    }
    const { action, ticket } = (await response.json()) as { action?: unknown; ticket?: unknown };
    if (typeof action !== "string" || typeof ticket !== "string") {
      throw new Error("handoff contract");
    }
    const form = document.createElement("form");
    form.method = "POST";
    form.action = action;
    form.target = tab ? name : "_self";
    const input = document.createElement("input");
    input.type = "hidden";
    input.name = "ticket";
    input.value = ticket;
    form.append(input);
    document.body.append(form);
    form.submit();
    form.remove();
  } catch {
    if (tab) {
      tab.location.href = studio.url;
    } else {
      window.location.href = studio.url;
    }
  }
}

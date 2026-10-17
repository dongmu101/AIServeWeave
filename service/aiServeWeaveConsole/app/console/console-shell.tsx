"use client";

import * as React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { CheckIcon, ChevronDownIcon, LogOutIcon, MenuIcon, SettingsIcon, ShieldCheckIcon, UserRoundIcon } from "lucide-react";
import { toast } from "sonner";

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
import { ROLE_LABELS, type Role } from "@/lib/console/contract";
import { describe } from "@/lib/console/errors";
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
const NAV_ITEMS: readonly { href: string; label: string }[] = [
  { href: "/console", label: "概览" },
  { href: "/console/users", label: "用户" },
  { href: "/console/keys", label: "API Key" },
  { href: "/console/audit", label: "审计" },
  { href: "/console/requests", label: "请求检索" },
  { href: "/console/quota", label: "配额" },
  { href: "/console/usage", label: "用量" },
  { href: "/console/models", label: "AI 服务与模型" },
  { href: "/console/workflows", label: "工作流" },
  { href: "/console/docs", label: "API 文档" },
  { href: "/console/jobs", label: "运行" },
  { href: "/console/jobs/history", label: "运行历史" },
  { href: "/console/settings", label: "设置" },
  { href: "/console/account", label: "账户安全" },
];

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
  children,
}: {
  user: SessionUser;
  children: React.ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const [leaving, setLeaving] = React.useState(false);
  const activeItem = NAV_ITEMS.findLast(
    (item) => pathname === item.href || pathname.startsWith(`${item.href}/`)
  );
  const displayName = user.name || user.email;
  const initial = Array.from(displayName.trim())[0]?.toUpperCase() || "U";

  async function leave() {
    setLeaving(true);
    try {
      await signOut();
    } catch (failure) {
      toast.error(describe(failure));
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
      <header className="h-14 shrink-0 border-b bg-background">
        <div className="mx-auto flex h-full w-full max-w-[96rem] items-center gap-3 px-4">
          <Link href="/console" className="shrink-0 whitespace-nowrap font-heading text-sm font-semibold">
            AIServeWeave<span className="hidden sm:inline"> 控制台</span>
          </Link>
          <nav aria-label="控制台导航" className="flex min-w-0 flex-1 items-center justify-end sm:justify-start">
            <div className="hidden items-center gap-0.5 xl:flex">
              {NAV_ITEMS.map((item) => {
                const active = item.href === activeItem?.href;
                return (
                  <Link
                    key={item.href}
                    href={item.href}
                    className={buttonVariants({ variant: active ? "secondary" : "ghost", size: "sm", className: "px-2" })}
                    aria-current={active ? "page" : undefined}
                  >
                    {item.label}
                  </Link>
                );
              })}
            </div>
            <DropdownMenu>
              <DropdownMenuTrigger
                aria-label="打开控制台导航"
                render={<Button variant="ghost" className="min-w-0 shrink xl:hidden" />}
              >
                <MenuIcon />
                <span className="truncate">{activeItem?.label ?? "导航"}</span>
                <ChevronDownIcon className="size-3 text-muted-foreground" />
              </DropdownMenuTrigger>
              <DropdownMenuContent className="w-56" aria-label="控制台导航">
                {NAV_ITEMS.map((item) => (
                  <DropdownMenuItem
                    key={item.href}
                    render={<Link href={item.href} />}
                    aria-current={item.href === activeItem?.href ? "page" : undefined}
                    className="min-h-9"
                  >
                    {item.label}
                    {item.href === activeItem?.href ? <CheckIcon className="ml-auto" /> : null}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          </nav>
          <DropdownMenu>
            <DropdownMenuTrigger
              aria-label="账户菜单"
              disabled={leaving}
              render={<Button variant="outline" size="icon" className="rounded-full bg-muted text-xs" />}
            >
              <span aria-hidden="true">{initial}</span>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-72 max-w-[calc(100vw-2rem)]" aria-label="账户菜单">
              <div className="space-y-2 px-3 py-2 text-xs">
                <p className="break-all text-sm font-medium">{displayName}</p>
                <p className="break-all text-muted-foreground">{user.email}</p>
                <Badge variant="outline">{roleLabel(user.role)}</Badge>
                <p className="break-all text-muted-foreground">
                  租户 <code className="font-mono">{user.tenantId}</code>
                </p>
              </div>
              <DropdownMenuSeparator />
              <DropdownMenuItem render={<Link href="/console/account" />} className="min-h-9">
                <UserRoundIcon />账户安全
              </DropdownMenuItem>
              <DropdownMenuItem render={<Link href="/console/settings" />} className="min-h-9">
                <SettingsIcon />设置
              </DropdownMenuItem>
              <DropdownMenuItem render={<Link href="/operator" />} className="min-h-9">
                <ShieldCheckIcon />平台运维
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={leave} disabled={leaving} aria-busy={leaving} closeOnClick={false} className="min-h-9">
                <LogOutIcon />{leaving ? "正在退出…" : "退出"}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>
      {/* Only the content scrolls beneath the compact fixed-height menu. / 仅正文在紧凑的固定高度菜单下方滚动。 */}
      <main className="relative min-h-0 min-w-0 flex-1 overflow-y-auto">
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
function roleLabel(role: string): string {
  return ROLE_LABELS[role as Role] ?? role;
}

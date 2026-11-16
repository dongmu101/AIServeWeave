"use client";

import * as React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ChevronsUpDownIcon, LogOutIcon, MenuIcon, PanelLeftIcon, PlusIcon, SearchIcon, WorkflowIcon } from "lucide-react";
import { toast } from "sonner";
import { cn } from "cn";

import { BrandMark } from "@/components/studio/brand";
import { useStoredValue } from "@/components/studio/use-stored-value";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { errorMessage } from "@/lib/studio/contract";

export interface ShellUser {
  displayName: string;
  email: string;
  role: string;
}

const COLLAPSED_KEY = "aisw-studio:sidebar-collapsed";

const ROLE_LABEL: Record<string, string> = { owner: "所有者", admin: "管理员", member: "成员" };

/**
 * StudioShell is the signed-in frame. On wide screens the sidebar is pinned
 * and can be collapsed to an icon rail; on narrow screens it is a drawer.
 *
 * StudioShell 是已登录的框架。宽屏下侧边栏固定显示，可折叠成图标栏；窄屏下是抽屉。
 */
export function StudioShell({ user, children }: { user: ShellUser; children: React.ReactNode }) {
  const [stored, store] = useStoredValue(COLLAPSED_KEY);
  const collapsed = stored === "1";
  const [drawerOpen, setDrawerOpen] = React.useState(false);

  function toggleCollapsed() {
    store(collapsed ? "0" : "1");
  }

  return (
    <div className="flex min-h-svh flex-1">
      <aside
        className={cn(
          "sticky top-0 hidden h-svh shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground transition-[width] duration-200 md:flex",
          collapsed ? "w-14" : "w-64"
        )}
      >
        <SidebarContent user={user} collapsed={collapsed} onToggle={toggleCollapsed} />
      </aside>

      {drawerOpen ? (
        <div className="fixed inset-0 z-40 md:hidden">
          <button type="button" aria-label="关闭侧边栏" className="absolute inset-0 bg-black/40" onClick={() => setDrawerOpen(false)} />
          <aside className="absolute inset-y-0 left-0 flex w-72 flex-col border-r bg-sidebar text-sidebar-foreground shadow-xl">
            <SidebarContent user={user} collapsed={false} onToggle={() => setDrawerOpen(false)} onNavigate={() => setDrawerOpen(false)} />
          </aside>
        </div>
      ) : null}

      <main className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-12 items-center px-3 md:hidden">
          <button type="button" aria-label="打开侧边栏" onClick={() => setDrawerOpen(true)}
            className="inline-flex size-9 items-center justify-center rounded-lg text-muted-foreground hover:bg-muted hover:text-foreground">
            <MenuIcon className="size-5" />
          </button>
        </div>
        {children}
      </main>
    </div>
  );
}

function SidebarContent({
  user,
  collapsed,
  onToggle,
  onNavigate,
}: {
  user: ShellUser;
  collapsed: boolean;
  onToggle: () => void;
  onNavigate?: () => void;
}) {
  return (
    <>
      <div className={cn("flex h-14 items-center gap-2 px-3", collapsed && "justify-center px-0")}>
        {collapsed ? null : (
          <Link href="/" onClick={onNavigate} className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-1 font-heading text-[15px] font-semibold">
            <BrandMark className="size-6 shrink-0" />
            <span className="truncate">Studio</span>
          </Link>
        )}
        <button type="button" onClick={onToggle} aria-label={collapsed ? "展开侧边栏" : "收起侧边栏"}
          className="inline-flex size-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground">
          <PanelLeftIcon className="size-[18px]" />
        </button>
      </div>

      <nav className="grid gap-0.5 px-2" aria-label="主导航">
        <NavItem href="/" icon={<PlusIcon />} label="新建对话" collapsed={collapsed} onNavigate={onNavigate} prominent />
        <NavItem icon={<SearchIcon />} label="搜索" collapsed={collapsed} soon />
        <NavItem icon={<WorkflowIcon />} label="编排" collapsed={collapsed} soon />
      </nav>

      <div className="mt-6 min-h-0 flex-1 overflow-y-auto px-2">
        {collapsed ? null : (
          <>
            <p className="px-2 pb-2 text-xs font-medium text-muted-foreground">最近对话</p>
            <p className="px-2 text-sm text-muted-foreground/80">还没有对话。</p>
          </>
        )}
      </div>

      <UserMenu user={user} collapsed={collapsed} />
    </>
  );
}

function NavItem({
  href,
  icon,
  label,
  collapsed,
  soon,
  prominent,
  onNavigate,
}: {
  href?: string;
  icon: React.ReactElement;
  label: string;
  collapsed: boolean;
  soon?: boolean;
  prominent?: boolean;
  onNavigate?: () => void;
}) {
  const className = cn(
    "flex h-9 items-center gap-3 rounded-lg px-2.5 text-sm [&_svg]:size-[18px] [&_svg]:shrink-0",
    collapsed && "justify-center px-0",
    prominent ? "font-medium" : "text-sidebar-foreground/90",
    soon ? "cursor-not-allowed opacity-60" : "hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
  );
  const content = (
    <>
      {icon}
      {collapsed ? null : <span className="flex-1 truncate">{label}</span>}
      {collapsed || !soon ? null : <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">即将上线</span>}
    </>
  );
  if (soon || !href) {
    return (
      <span aria-disabled="true" title={collapsed ? `${label}（即将上线）` : undefined} className={className}>
        {content}
      </span>
    );
  }
  return (
    <Link href={href} onClick={onNavigate} title={collapsed ? label : undefined} className={className}>
      {content}
    </Link>
  );
}

function UserMenu({ user, collapsed }: { user: ShellUser; collapsed: boolean }) {
  const router = useRouter();
  const [pending, setPending] = React.useState(false);
  const initial = (user.displayName || user.email || "?").slice(0, 1).toUpperCase();

  async function signOut() {
    if (pending) return;
    setPending(true);
    try {
      const response = await fetch("/api/session", { method: "DELETE" });
      if (!response.ok) {
        const body = (await response.json().catch(() => ({}))) as { error?: string };
        toast.error(`退出失败：${errorMessage(body.error ?? "")}`);
        setPending(false);
        return;
      }
      router.replace("/login");
      router.refresh();
    } catch {
      toast.error("网络连接失败，请稍后重试。");
      setPending(false);
    }
  }

  return (
    <div className="border-t p-2">
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <button type="button" className={cn(
              "flex w-full items-center gap-2.5 rounded-lg p-1.5 text-left hover:bg-sidebar-accent",
              collapsed && "justify-center"
            )} />
          }
        >
          <span className="inline-flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-sm font-medium">{initial}</span>
          {collapsed ? null : (
            <>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium">{user.displayName}</span>
                <span className="block truncate text-xs text-muted-foreground">{ROLE_LABEL[user.role] ?? user.role}</span>
              </span>
              <ChevronsUpDownIcon className="size-4 text-muted-foreground" />
            </>
          )}
        </DropdownMenuTrigger>
        <DropdownMenuContent side="top" align="start" className="w-60">
          <DropdownMenuGroup>
            <DropdownMenuLabel className="truncate">{user.email}</DropdownMenuLabel>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={signOut} disabled={pending}>
            <LogOutIcon />
            退出登录
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

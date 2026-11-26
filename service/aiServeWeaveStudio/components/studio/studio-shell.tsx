"use client";

import * as React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { ChevronsUpDownIcon, EllipsisIcon, LogOutIcon, MenuIcon, PanelLeftIcon, PencilIcon, PlusIcon, SearchIcon, Trash2Icon, WorkflowIcon } from "lucide-react";
import { toast } from "sonner";
import { cn } from "cn";

import { BrandMark } from "@/components/studio/brand";
import { ConversationSearch, isSearchShortcut, useSearchShortcutLabel } from "@/components/studio/conversation-search";
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
import { sendToSignIn } from "@/components/studio/session-expired";
import { errorMessage } from "@/lib/studio/contract";
import type { Conversation } from "@/lib/studio/conversations";

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
export interface ShellConversations {
  items: Conversation[];
  error: string;
}

export function StudioShell({ user, conversations, children }: { user: ShellUser; conversations: ShellConversations; children: React.ReactNode }) {
  const [stored, store] = useStoredValue(COLLAPSED_KEY);
  const collapsed = stored === "1";
  const [drawerOpen, setDrawerOpen] = React.useState(false);
  const [searchOpen, setSearchOpen] = React.useState(false);

  function toggleCollapsed() {
    store(collapsed ? "0" : "1");
  }

  function openSearch() {
    setDrawerOpen(false);
    setSearchOpen(true);
  }

  React.useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (!isSearchShortcut(event)) return;
      event.preventDefault();
      setDrawerOpen(false);
      setSearchOpen((open) => !open);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div className="flex h-svh shrink-0 overflow-hidden">
      <aside
        className={cn(
          "sticky top-0 hidden h-svh shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground transition-[width] duration-200 md:flex",
          collapsed ? "w-14" : "w-64"
        )}
      >
        <SidebarContent user={user} conversations={conversations} collapsed={collapsed} onToggle={toggleCollapsed} onSearch={openSearch} />
      </aside>

      {drawerOpen ? (
        <div className="fixed inset-0 z-40 md:hidden">
          <button type="button" aria-label="关闭侧边栏" className="absolute inset-0 bg-black/40" onClick={() => setDrawerOpen(false)} />
          <aside className="absolute inset-y-0 left-0 flex w-72 flex-col border-r bg-sidebar text-sidebar-foreground shadow-xl">
            <SidebarContent user={user} conversations={conversations} collapsed={false} onToggle={() => setDrawerOpen(false)} onSearch={openSearch} onNavigate={() => setDrawerOpen(false)} />
          </aside>
        </div>
      ) : null}

      <main className="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto">
        <div className="flex h-12 items-center px-3 md:hidden">
          <button type="button" aria-label="打开侧边栏" onClick={() => setDrawerOpen(true)}
            className="inline-flex size-9 items-center justify-center rounded-lg text-muted-foreground hover:bg-muted hover:text-foreground">
            <MenuIcon className="size-5" />
          </button>
        </div>
        {children}
      </main>

      <ConversationSearch open={searchOpen} onOpenChange={setSearchOpen} />
    </div>
  );
}

function SidebarContent({
  user,
  conversations,
  collapsed,
  onToggle,
  onSearch,
  onNavigate,
}: {
  user: ShellUser;
  conversations: ShellConversations;
  collapsed: boolean;
  onToggle: () => void;
  onSearch: () => void;
  onNavigate?: () => void;
}) {
  const shortcut = useSearchShortcutLabel();
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
        <NavItem icon={<SearchIcon />} label="搜索" collapsed={collapsed} onClick={onSearch} hint={shortcut} />
        <NavItem href="/flows" icon={<WorkflowIcon />} label="编排" collapsed={collapsed} onNavigate={onNavigate} />
      </nav>

      <div className="mt-6 min-h-0 flex-1 overflow-y-auto px-2">
        {collapsed ? null : <RecentConversations conversations={conversations} onNavigate={onNavigate} />}
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
  hint,
  onClick,
  onNavigate,
}: {
  href?: string;
  icon: React.ReactElement;
  label: string;
  collapsed: boolean;
  soon?: boolean;
  prominent?: boolean;
  hint?: string;
  onClick?: () => void;
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
      {collapsed || soon || !hint ? null : <span className="text-xs text-muted-foreground max-md:hidden">{hint}</span>}
    </>
  );
  if (onClick && !soon) {
    return (
      <button type="button" onClick={onClick} title={collapsed ? label : undefined} aria-keyshortcuts="Meta+K Control+K" className={cn(className, "w-full text-left")}>
        {content}
      </button>
    );
  }
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

function RecentConversations({ conversations, onNavigate }: { conversations: ShellConversations; onNavigate?: () => void }) {
  return (
    <>
      <p className="px-2 pb-2 text-xs font-medium text-muted-foreground">最近对话</p>
      {conversations.error ? (
        <p className="px-2 text-sm text-muted-foreground/80">对话列表加载失败：{conversations.error}</p>
      ) : conversations.items.length === 0 ? (
        <p className="px-2 text-sm text-muted-foreground/80">还没有对话。</p>
      ) : (
        <ul className="grid gap-0.5" aria-label="最近对话">
          {conversations.items.map((conversation) => (
            <ConversationItem key={conversation.id} conversation={conversation} onNavigate={onNavigate} />
          ))}
        </ul>
      )}
    </>
  );
}

/** ConversationItem is one sidebar entry, with rename and delete.
 * ConversationItem 是侧边栏中的一项，可重命名与删除。 */
function ConversationItem({ conversation, onNavigate }: { conversation: Conversation; onNavigate?: () => void }) {
  const router = useRouter();
  const pathname = usePathname();
  const href = `/c/${conversation.id}`;
  const active = pathname === href;
  const [mode, setMode] = React.useState<"view" | "rename" | "confirm">("view");
  const [busy, setBusy] = React.useState(false);
  // The chosen action applies once the menu has closed, and that close does
  // not hand focus back: otherwise focus would land on the trigger (or the
  // composer) right after the rename input mounts, blurring, and so
  // committing, it at once.
  // 所选操作在菜单关闭后才生效，且这次关闭不交还焦点：否则焦点会在重命名输入框挂载后
  // 立刻回到触发按钮（或输入框），使其失焦并因此立刻提交。
  const chosen = React.useRef<"rename" | "confirm" | null>(null);
  const title = conversation.title || "新对话";

  async function call(method: "PATCH" | "DELETE", body?: unknown): Promise<boolean> {
    setBusy(true);
    try {
      const response = await fetch(`/api/studio/conversations/${conversation.id}`, {
        method,
        headers: body ? { "Content-Type": "application/json" } : undefined,
        body: body ? JSON.stringify(body) : undefined,
      });
      if (response.status === 401) {
        sendToSignIn("/");
        return false;
      }
      if (!response.ok) {
        const parsed = (await response.json().catch(() => ({}))) as { error?: string };
        toast.error(errorMessage(parsed.error ?? ""));
        return false;
      }
      return true;
    } catch {
      toast.error("网络连接失败，请稍后重试。");
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function rename(value: string) {
    const next = value.trim();
    setMode("view");
    if (next === "" || next === conversation.title) return;
    if (await call("PATCH", { title: next })) router.refresh();
  }

  async function remove() {
    if (!(await call("DELETE"))) return;
    setMode("view");
    if (active) router.push("/");
    router.refresh();
  }

  if (mode === "rename") {
    return (
      <li>
        <RenameInput initial={conversation.title} onCommit={(value) => void rename(value)} onCancel={() => setMode("view")} />
      </li>
    );
  }

  if (mode === "confirm") {
    return (
      <li className="flex h-9 items-center gap-1 rounded-lg bg-destructive/10 px-2.5 text-sm">
        <span className="min-w-0 flex-1 truncate">删除「{title}」？</span>
        <button type="button" disabled={busy} onClick={() => void remove()} className="shrink-0 rounded px-1.5 py-0.5 font-medium text-destructive hover:bg-destructive/10">
          删除
        </button>
        <button type="button" disabled={busy} onClick={() => setMode("view")} className="shrink-0 rounded px-1.5 py-0.5 hover:bg-sidebar-accent">
          取消
        </button>
      </li>
    );
  }

  return (
    <li className={cn("group/item relative flex h-9 items-center rounded-lg", active ? "bg-sidebar-accent text-sidebar-accent-foreground" : "hover:bg-sidebar-accent/70")}>
      <Link href={href} onClick={onNavigate} title={title} aria-current={active ? "page" : undefined} className="min-w-0 flex-1 truncate py-2 pl-2.5 pr-8 text-sm">
        {title}
      </Link>
      <DropdownMenu
        onOpenChangeComplete={(open) => {
          if (!open && chosen.current) {
            setMode(chosen.current);
            chosen.current = null;
          }
        }}
      >
        <DropdownMenuTrigger
          render={
            <button type="button" aria-label={`管理对话 ${title}`}
              className="absolute right-1 inline-flex size-7 items-center justify-center rounded-md text-muted-foreground opacity-0 hover:text-foreground focus-visible:opacity-100 group-hover/item:opacity-100 data-popup-open:opacity-100 max-md:opacity-100" />
          }
        >
          <EllipsisIcon className="size-4" />
        </DropdownMenuTrigger>
        <DropdownMenuContent side="bottom" align="start" className="w-36" finalFocus={() => chosen.current === null}>
          <DropdownMenuItem onClick={() => (chosen.current = "rename")}>
            <PencilIcon />
            重命名
          </DropdownMenuItem>
          <DropdownMenuItem variant="destructive" onClick={() => (chosen.current = "confirm")}>
            <Trash2Icon />
            删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </li>
  );
}

/** RenameInput commits on Enter or a click outside and cancels on Escape. It
 * does not act on blur: a closing menu may move focus programmatically right
 * after it mounts, and that is not the user leaving the field.
 *
 * RenameInput 在按 Enter 或点击外部时提交，按 Escape 取消。它不响应失焦：关闭中的菜单可能
 * 在它挂载后立刻以程序方式移动焦点，那并不是用户离开了输入框。 */
function RenameInput({ initial, onCommit, onCancel }: { initial: string; onCommit: (value: string) => void; onCancel: () => void }) {
  const ref = React.useRef<HTMLInputElement>(null);
  const commit = React.useEffectEvent(() => onCommit(ref.current?.value ?? initial));

  React.useEffect(() => {
    const frame = requestAnimationFrame(() => {
      ref.current?.focus();
      ref.current?.select();
    });
    function onPointerDown(event: PointerEvent) {
      if (ref.current && !ref.current.contains(event.target as Node)) commit();
    }
    document.addEventListener("pointerdown", onPointerDown, true);
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("pointerdown", onPointerDown, true);
    };
  }, []);

  return (
    <input
      ref={ref}
      defaultValue={initial}
      maxLength={200}
      aria-label="对话名称"
      onKeyDown={(event) => {
        if (event.key === "Enter" && !event.nativeEvent.isComposing) onCommit(event.currentTarget.value);
        if (event.key === "Escape") onCancel();
      }}
      className="h-9 w-full rounded-lg border bg-background px-2.5 text-sm outline-none focus:border-foreground/30"
    />
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

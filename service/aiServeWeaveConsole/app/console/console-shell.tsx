"use client";

import * as React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { LogOutIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { signOut } from "@/lib/console/api-client";
import { ROLE_LABELS, type Role } from "@/lib/console/contract";
import { describe } from "@/lib/console/errors";
import type { SessionUser } from "@/lib/console/session-payload";

/**
 * NAV_ITEMS is the tenant navigation: the views every signed-in role reaches,
 * each with a real Admin API behind it. A menu entry for a page with no data
 * source would be a promise the backend cannot keep, which is why metrics and
 * request search are absent — they have no Admin API, and no store behind one.
 *
 * NAV_ITEMS 是租户导航：每个已登录角色都能到达的视图，每一个背后都确有 Admin API。给一个
 * 没有数据来源的页面挂菜单项，等于许下后端兑现不了的承诺，这正是指标与请求检索不在其中的
 * 原因——它们既没有 Admin API，那个 API 背后也没有存储。
 */
const NAV_ITEMS: readonly { href: string; label: string }[] = [
  { href: "/console", label: "概览" },
  { href: "/console/users", label: "用户" },
  { href: "/console/keys", label: "API Key" },
  { href: "/console/audit", label: "审计" },
  { href: "/console/quota", label: "配额" },
  { href: "/console/workflows", label: "工作流" },
  { href: "/console/jobs", label: "运行" },
  { href: "/console/settings", label: "设置" },
];

/**
 * OPERATOR_NAV_ITEMS are the fleet views, shown only to an operator on an
 * operations console. They are a separate list rather than a flag on each row
 * because they are a separate audience: everything above is scoped to one
 * tenant, and everything here is shared infrastructure that belongs to none.
 *
 * OPERATOR_NAV_ITEMS 是机群视图，只对运维控制台上的运维展示。它们是一个独立的列表而不是
 * 每行加一个标志，因为它们面向的是另一批人：上面的一切都限定在单个租户内，而这里的一切
 * 都是不属于任何租户的共享基础设施。
 */
const OPERATOR_NAV_ITEMS: readonly { href: string; label: string }[] = [
  { href: "/console/fleet", label: "节点" },
  { href: "/console/models", label: "模型" },
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
  operator,
  children,
}: {
  user: SessionUser;
  /** operator is decided on the server. It only chooses what to draw: the
   * pages themselves answer 404 to anybody else, and the control plane's
   * fleet endpoints are behind a secret this browser never holds.
   *
   * operator 由服务端决定。它只选择画出什么：页面本身会对其他任何人回 404，而控制面
   * 的机群端点背后是一个本浏览器从不持有的密钥。 */
  operator: boolean;
  children: React.ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const [leaving, setLeaving] = React.useState(false);

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
    <div className="flex min-h-full flex-1 flex-col">
      <header className="border-b">
        <div className="mx-auto flex w-full max-w-6xl flex-wrap items-center gap-3 px-4 py-3">
          <span className="font-heading text-sm font-semibold">
            AIServeWeave 控制台
          </span>
          <nav aria-label="控制台导航" className="flex items-center gap-1">
            {[...NAV_ITEMS, ...(operator ? OPERATOR_NAV_ITEMS : [])].map((item) => {
              const active =
                pathname === item.href || pathname.startsWith(`${item.href}/`);
              return (
                <Button
                  key={item.href}
                  variant={active ? "secondary" : "ghost"}
                  size="sm"
                  render={<Link href={item.href} />}
                  aria-current={active ? "page" : undefined}
                >
                  {item.label}
                </Button>
              );
            })}
          </nav>
          <div className="ml-auto flex items-center gap-3">
            <div className="text-right text-xs leading-tight">
              <div className="font-medium">{user.name || user.email}</div>
              <div className="text-muted-foreground">
                租户 <code className="font-mono">{user.tenantId}</code>
              </div>
            </div>
            <Badge variant="outline">{roleLabel(user.role)}</Badge>
            <Button
              variant="outline"
              size="sm"
              onClick={leave}
              disabled={leaving}
              aria-busy={leaving}
            >
              <LogOutIcon />
              退出
            </Button>
          </div>
        </div>
      </header>
      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
        {children}
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

import type * as React from "react";
import { headers } from "next/headers";
import { redirect } from "next/navigation";

import { ConsoleShell } from "@/app/console/console-shell";
import { SiteFooter } from "@/components/site/site-footer";
import { SiteHeader } from "@/components/site/site-header";
import { PUBLIC_DOCS_HEADER, PUBLIC_DOCS_ROOT } from "@/lib/console/public-docs";
import { readSession } from "@/lib/server/session";

/**
 * Every console page renders inside this layout, and the session is read here
 * rather than in each page — this is the authoritative check the proxy's
 * cookie test only approximates. The identity handed down is the one that came
 * out of the sealed cookie, so a page renders from a session this server
 * issued, never from a value the browser supplied.
 *
 * The layout is dynamic by consequence, not by declaration: reading cookies
 * opts the route out of prerendering, which is what an authenticated view
 * requires anyway.
 *
 * 每个控制台页面都渲染在本布局之内，会话在这里读取而不是在各个页面里读取 —— 这是权威的
 * 检查，proxy 那次 cookie 存在性测试只是它的近似。向下传递的身份出自那个密封的 cookie，
 * 因此页面据以渲染的是本服务签发的会话，绝不是浏览器提供的值。
 *
 * 本布局是动态的，这是后果而非声明：读取 cookie 会让该路由退出预渲染，而这正是一个
 * 已认证视图本来就需要的。
 *
 * The API reference is the one exception: a visitor without a session reads
 * it inside the public site frame instead of being sent to sign-in. Its data
 * is public on the control plane too, so nothing behind it needs a session.
 *
 * API 参考文档是唯一的例外：没有会话的访客在公开站点框架内阅读它，而不是被送去登录。
 * 它的数据在控制面上同样是公开的，因此其背后没有任何东西需要会话。
 */
export default async function ConsoleLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const session = await readSession();
  if (!session) {
    if ((await headers()).get(PUBLIC_DOCS_HEADER) === "1") {
      return (
        <>
          <SiteHeader signInNext={PUBLIC_DOCS_ROOT} />
          <main className="mx-auto w-full max-w-[96rem] flex-1 px-4 py-6 sm:px-8">{children}</main>
          <SiteFooter />
        </>
      );
    }
    redirect("/login");
  }

  return (
    <ConsoleShell user={session.user}>
      {children}
    </ConsoleShell>
  );
}

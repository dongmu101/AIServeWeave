import type * as React from "react";
import { redirect } from "next/navigation";

import { ConsoleShell } from "@/app/console/console-shell";
import { operatorAccess } from "@/lib/server/operator";
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
 */
export default async function ConsoleLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }

  // Whether the operator pages exist for this person is decided here, on the
  // server, and passed down as a fact. A client component that worked it out
  // for itself would need the allowlist in the browser, and a menu item is not
  // worth shipping a deployment's operator roster to every tenant user.
  //
  // 运维页面对这个人是否存在，在这里、在服务端决定，并作为一个事实向下传递。若由客户端
  // 组件自行判断，就得把名单送进浏览器，而一个菜单项不值得把某个部署的运维名录发给每一
  // 个租户用户。
  return (
    <ConsoleShell user={session.user} operator={operatorAccess(session.user).allowed}>
      {children}
    </ConsoleShell>
  );
}

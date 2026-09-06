import { redirect } from "next/navigation";

import { UsersView } from "@/app/console/users/users-view";
import { readSession } from "@/lib/server/session";

/**
 * The users page. The role comes from the sealed session rather than from the
 * browser, and it decides which controls are drawn — the control plane decides
 * whether they work.
 *
 * 用户页。角色来自密封的会话而不是浏览器，它决定画出哪些控件——它们是否管用由控制面
 * 决定。
 */
export default async function UsersPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <UsersView role={session.user.role} />;
}

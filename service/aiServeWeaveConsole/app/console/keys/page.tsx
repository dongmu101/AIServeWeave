import { redirect } from "next/navigation";

import { KeysView } from "@/app/console/keys/keys-view";
import { readSession } from "@/lib/server/session";

/**
 * The API key page. Both the role and the user id come from the sealed
 * session: a member may revoke only the keys it created, and deciding that
 * from a value the browser could edit would put the wrong button on screen.
 *
 * API Key 页。角色与用户 id 都来自密封的会话：member 只能吊销自己创建的 key，若用一个
 * 浏览器可改的值来判断，屏幕上就会出现错误的按钮。
 */
export default async function KeysPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <KeysView role={session.user.role} userId={session.user.id} />;
}

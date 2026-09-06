import { redirect } from "next/navigation";

import { QuotaView } from "@/app/console/quota/quota-view";
import { readSession } from "@/lib/server/session";

/**
 * The quota page. Every role may read the tenant's limits; only owner and
 * admin may write them, which is the control plane's rule and the reason the
 * role is read from the session here rather than from the browser.
 *
 * 配额页。任何角色都可以读取本租户的限制；只有 owner 与 admin 可以写入，这是控制面的
 * 规则，也是这里从会话而不是从浏览器读取角色的原因。
 */
export default async function QuotaPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <QuotaView role={session.user.role} />;
}

import { redirect } from "next/navigation";

import { AuditTabs } from "@/app/console/audit/audit-tabs";
import { readSession } from "@/lib/server/session";

/**
 * The audit page, with request search as its second tab. Every signed-in role
 * may read its own tenant's trail and authenticated front-door requests — that
 * is the control plane's current rule, and the Console does not narrow it on
 * its own.
 *
 * 审计页，请求检索是它的第二个 Tab。任何已登录的角色都可以读取自己租户的审计线索与
 * 已通过鉴权的前门请求——这是控制面当前的规则，Console 不擅自收窄它。
 */
export default async function AuditPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <AuditTabs />;
}

import { redirect } from "next/navigation";

import { AuditView } from "@/app/console/audit/audit-view";
import { readSession } from "@/lib/server/session";

/**
 * The audit page. Every signed-in role may read its own tenant's trail — that
 * is the control plane's current rule, and the Console does not narrow it on
 * its own.
 *
 * 审计页。任何已登录的角色都可以读取自己租户的线索——这是控制面当前的规则，Console
 * 不擅自收窄它。
 */
export default async function AuditPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <AuditView />;
}

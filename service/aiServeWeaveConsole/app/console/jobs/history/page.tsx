import { redirect } from "next/navigation";

import { HistoryView } from "@/app/console/jobs/history/history-view";
import { readSession } from "@/lib/server/session";

/**
 * The persisted job history page. Every signed-in role may read its own
 * tenant's history — the same rule the live job view and the audit trail
 * already follow, and the control plane does not narrow it further.
 *
 * 持久化 job 历史页。任何已登录的角色都可以读取自己租户的历史——与实时 Job
 * 视图、审计线索遵循同一条规则，控制面没有进一步收窄它。
 */
export default async function JobHistoryPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <HistoryView />;
}

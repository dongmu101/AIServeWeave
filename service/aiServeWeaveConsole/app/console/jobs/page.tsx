import { redirect } from "next/navigation";

import { JobsView } from "@/app/console/jobs/jobs-view";
import { readSession } from "@/lib/server/session";

/**
 * Jobs are tenant data: the tenant comes from the session and travels to each
 * Gateway replica, so the filtering happens in the job table rather than in
 * this Console.
 *
 * Job 是租户数据：租户来自会话并被带到每个 Gateway 副本，因此过滤发生在 job 表里，
 * 而不是在本 Console 里。
 */
export default async function JobsPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <JobsView />;
}

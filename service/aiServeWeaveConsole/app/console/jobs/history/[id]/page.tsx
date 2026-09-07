import { redirect } from "next/navigation";

import { DetailView } from "@/app/console/jobs/history/[id]/detail-view";
import { readSession } from "@/lib/server/session";

/**
 * One persisted job's detail page. Like the history list it drills into, any
 * signed-in role of the owning tenant may read it — the control plane scopes
 * the read by session tenant already, so this page adds no narrower rule of
 * its own.
 *
 * 单个持久化 job 的详情页。与它所属的历史列表一样，所属租户的任何已登录角色都可以
 * 读取——控制面已经按会话租户限定了这次读取，本页不再额外收窄。
 */
export default async function JobHistoryDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  const { id } = await params;
  return <DetailView jobId={id} hasGatewayKey={Boolean(session.gatewayApiKey)} />;
}

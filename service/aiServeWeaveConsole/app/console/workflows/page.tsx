import { redirect } from "next/navigation";

import { WorkflowsView } from "@/app/console/workflows/workflows-view";
import { operatorAccess } from "@/lib/server/operator";
import { readSession } from "@/lib/server/session";

/**
 * The workflow menu is a tenant page: it is what a caller needs in order to
 * submit a run at all, so every signed-in role reaches it.
 *
 * An operator additionally sees the rollout — which replicas registered each
 * template and whether they agree — and that comes from a different endpoint
 * with a different guard. Whether to fetch it is decided here, on the server,
 * from the same allowlist the fleet pages use.
 *
 * 工作流菜单是租户页面：它是调用方提交一次运行所必需的东西，因此每个已登录角色都能到达。
 *
 * 运维额外看到发布状态——哪些副本注册了每个模板、它们是否一致——那来自另一个由不同守卫
 * 保护的端点。要不要去取它，在这里、在服务端，用与机群页面相同的那份名单决定。
 */
export default async function WorkflowsPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <WorkflowsView operator={operatorAccess(session.user).allowed} />;
}

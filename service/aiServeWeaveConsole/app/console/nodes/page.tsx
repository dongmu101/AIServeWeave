import { redirect } from "next/navigation";

import { NodesView } from "@/app/console/nodes/nodes-view";
import { readSession } from "@/lib/server/session";

/**
 * The tenant's own nodes. Every role may read the list; only owner and admin
 * may create a bundle or delete a node, which the control plane enforces and
 * the role read from the sealed session here merely reflects.
 *
 * 商户的自有节点。所有角色都可以查看列表；只有 owner 与 admin 可以生成接入包或删除节点
 * ——这由控制面强制执行，这里从密封会话读取的角色只是如实反映。
 */
export default async function NodesPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <NodesView role={session.user.role} />;
}

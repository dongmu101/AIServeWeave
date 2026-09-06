import { notFound, redirect } from "next/navigation";

import { FleetView } from "@/app/console/fleet/fleet-view";
import { operatorAccess } from "@/lib/server/operator";
import { readSession } from "@/lib/server/session";

/**
 * The fleet page exists only in a Console deployment configured as an
 * operations console, and only for somebody on its operator list. Everyone
 * else gets a 404: a node has no tenant, so "you may not see this" and "this
 * page is not here" must look the same from a tenant's session.
 *
 * 机群页面只存在于被配置为运维控制台的 Console 部署中，且只对其运维名单上的人存在。
 * 其余所有人得到 404：节点没有租户，因此从租户会话看过去，「你不能看这个」与「这里没有
 * 这个页面」必须长得一样。
 */
export default async function FleetPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  if (!operatorAccess(session.user).allowed) {
    notFound();
  }
  return <FleetView />;
}

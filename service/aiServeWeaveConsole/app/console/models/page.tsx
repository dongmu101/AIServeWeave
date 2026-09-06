import { notFound, redirect } from "next/navigation";

import { ModelsView } from "@/app/console/models/models-view";
import { operatorAccess } from "@/lib/server/operator";
import { readSession } from "@/lib/server/session";

/**
 * The model catalog is the same read as the fleet page, seen by model, and it
 * is gated identically: what a node serves is as much shared infrastructure as
 * the node itself.
 *
 * 模型目录与机群页面是同一次读取，只是按模型来看，并且守卫方式完全相同：一个节点提供
 * 什么，与那个节点本身一样，都属于共享的基础设施。
 */
export default async function ModelsPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  if (!operatorAccess(session.user).allowed) {
    notFound();
  }
  return <ModelsView />;
}

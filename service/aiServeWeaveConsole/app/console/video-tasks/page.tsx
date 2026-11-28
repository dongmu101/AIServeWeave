import { redirect } from "next/navigation";

import { VideoTasksView } from "@/app/console/video-tasks/video-tasks-view";
import { readSession } from "@/lib/server/session";

/**
 * The video generation tasks under the tenant's own Volcengine Ark key. Only
 * owner and admin manage that key, which the control plane enforces; the role
 * read from the sealed session here merely decides what is rendered.
 *
 * 租户自己的火山方舟 Key 下的视频生成任务。只有 owner 与 admin 管理这把 Key，由控制面强制
 * 执行；这里从密封会话读取的角色只决定渲染什么。
 */
export default async function VideoTasksPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <VideoTasksView role={session.user.role} />;
}

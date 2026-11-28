"use client";

import Link from "next/link";

import { ArkTaskList } from "@/components/console/ark-task-list";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Message } from "@/components/i18n/message";
import { buttonVariants } from "@/components/ui/button";
import { arkRequest } from "@/lib/console/ark";
import { canManageStorage } from "@/lib/console/permissions";

/**
 * VideoTasksView lists the tasks under the tenant's own Ark key. Without its
 * own key there is nothing to list here: the platform key's tasks belong to
 * every tenant, so the control plane lists them only to the operator.
 *
 * VideoTasksView 列出租户自己的方舟 Key 下的任务。没有自己的 Key 时这里没有可列的内容：
 * 平台 Key 下的任务属于所有租户，因此控制面只把它们列给运维。
 */
export function VideoTasksView({ role }: { role: string }) {
  return <div className="grid max-w-5xl gap-6">
    <div>
      <h1 className="font-heading text-xl font-semibold tracking-tight"><Message id="tenant.videoTasks" /></h1>
      <p className="mt-2 text-sm text-muted-foreground"><Message id="tenant.videoTasksIntro" /></p>
    </div>
    {canManageStorage(role) ? <OwnKeyTasks /> : <p className="text-sm text-muted-foreground"><Message id="tenant.arkOwnerOnly" /></p>}
  </div>;
}

function OwnKeyTasks() {
  const ark = useResource(arkRequest("admin"));
  if (ark.errorKind === "not_found") return <p className="text-sm text-muted-foreground"><Message id="tenant.arkDisabled" /></p>;
  if (ark.error) return <ErrorState message={ark.error} onRetry={ark.reload} />;
  if (!ark.data) return <LoadingState rows={4} />;
  if (!ark.data.enabled) return <p className="text-sm text-muted-foreground"><Message id="tenant.arkDisabled" /></p>;
  if (!ark.data.credential) {
    return <EmptyState
      title={<Message id="tenant.videoTasksNeedOwnKey" />}
      action={<Link href="/console/settings" className={buttonVariants({ variant: "outline", size: "sm" })}><Message id="tenant.videoTasksGoSettings" /></Link>}
    />;
  }
  return <ArkTaskList surface="admin" />;
}

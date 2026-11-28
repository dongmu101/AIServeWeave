"use client";

import * as React from "react";
import { toast } from "sonner";

import { arkFailure } from "@/components/console/ark-credential-panel";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { useResource } from "@/components/console/use-resource";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  ARK_TASK_STATUSES,
  arkTasksRequest,
  deleteArkTaskRequest,
  pageCount,
  taskAction,
  type ArkSurface,
  type ArkTask,
  type ArkTaskQuery,
  type ArkTaskStatus,
} from "@/lib/console/ark";
import { ApiError } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";
import type { MessageKey } from "@/lib/i18n/messages";

const STATUS_LABEL: Record<ArkTaskStatus, MessageKey> = {
  queued: "tenant.arkTaskStatusQueued",
  running: "tenant.arkTaskStatusRunning",
  succeeded: "tenant.arkTaskStatusSucceeded",
  failed: "tenant.arkTaskStatusFailed",
  cancelled: "tenant.arkTaskStatusCancelled",
  expired: "tenant.arkTaskStatusExpired",
  unknown: "tenant.arkTaskStatusUnknown",
};

const STATUS_VARIANT: Record<ArkTaskStatus, "default" | "secondary" | "destructive" | "outline"> = {
  queued: "outline",
  running: "default",
  succeeded: "secondary",
  failed: "destructive",
  cancelled: "outline",
  expired: "outline",
  unknown: "outline",
};

/**
 * ArkTaskList shows the video generation tasks under one scope's own Ark key,
 * read from Ark a page at a time (Ark pages by number and reports a total). A
 * queued task can be cancelled and a finished one's record deleted, each
 * after a confirmation and sent once; a running task offers nothing, since Ark
 * cannot cancel it.
 *
 * ArkTaskList 展示一个 scope 自己的方舟 Key 下的视频生成任务，每次从方舟读取一页（方舟按页码
 * 分页并报告总数）。排队中的任务可以取消、已结束的任务可以删除记录，都要先确认且只发送一次；
 * 执行中的任务不提供操作，因为方舟无法取消它。
 */
export function ArkTaskList({ surface }: { surface: ArkSurface }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [query, setQuery] = React.useState<ArkTaskQuery>({ pageNum: 1, status: "" });
  const tasks = useResource(arkTasksRequest(surface, query));
  const [target, setTarget] = React.useState<{ task: ArkTask; action: "cancel" | "delete" } | null>(null);
  const page = tasks.data;
  const pages = page ? pageCount(page.total, page.pageSize) : 1;

  async function act() {
    if (!target) return;
    try {
      await run(deleteArkTaskRequest(surface, target.task.id));
    } catch (failure) {
      // Already gone: the list just needs to catch up. / 已经不存在了：列表只需跟上现状。
      if (!(failure instanceof ApiError && failure.status === 404)) throw arkFailure(failure);
    }
    toast.success(<Message id={target.action === "cancel" ? "tenant.arkTaskCancelledToast" : "tenant.arkTaskDeletedToast"} />);
    tasks.reload();
  }

  return <div className="grid gap-3">
    <div className="flex flex-wrap items-end gap-2">
      <label className="grid gap-1 text-sm" htmlFor={`ark-tasks-${surface}-status`}><Message id="tenant.arkTaskStatus" />
        <select id={`ark-tasks-${surface}-status`} className="h-9 w-fit rounded-md border bg-background px-3" value={query.status}
          onChange={(e) => setQuery({ pageNum: 1, status: e.target.value as ArkTaskQuery["status"] })}>
          <option value="">{t("tenant.arkTaskStatusAll")}</option>
          {ARK_TASK_STATUSES.map((status) => <option key={status} value={status}>{t(STATUS_LABEL[status])}</option>)}
        </select>
      </label>
      <Button variant="outline" size="sm" onClick={tasks.reload} disabled={tasks.loading}><Message id="tenant.arkTaskRefresh" /></Button>
    </div>

    {tasks.error ? <ErrorState message={tasks.error} onRetry={tasks.reload} /> : !page ? <LoadingState rows={4} /> : page.items.length === 0 ? (
      <EmptyState title={<Message id="tenant.arkTaskEmpty" />} />
    ) : (
      <div className="overflow-x-auto rounded-xl border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead><Message id="tenant.arkTaskId" /></TableHead>
              <TableHead><Message id="tenant.arkTaskStatus" /></TableHead>
              <TableHead><Message id="tenant.arkTaskModel" /></TableHead>
              <TableHead><Message id="tenant.arkTaskCreated" /></TableHead>
              <TableHead><Message id="tenant.arkTaskDuration" /></TableHead>
              <TableHead><Message id="tenant.arkTaskResolution" /></TableHead>
              <TableHead className="text-right"><Message id="tenant.arkTaskActions" /></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {page.items.map((task) => {
              const action = taskAction(task.status);
              return <TableRow key={task.id}>
                <TableCell className="font-mono text-xs">{task.id}</TableCell>
                <TableCell>
                  <Badge variant={STATUS_VARIANT[task.status]}><Message id={STATUS_LABEL[task.status]} /></Badge>
                  {task.errorCode ? <span className="ml-2 font-mono text-xs text-muted-foreground">{task.errorCode}</span> : null}
                </TableCell>
                <TableCell className="font-mono text-xs">{task.model || "—"}</TableCell>
                <TableCell className="whitespace-nowrap text-xs">{task.createdAt ? formatDateTime(task.createdAt, undefined, locale) : "—"}</TableCell>
                <TableCell className="whitespace-nowrap text-xs">{task.duration > 0 ? t("tenant.arkTaskSeconds", { seconds: task.duration }) : "—"}</TableCell>
                <TableCell className="whitespace-nowrap text-xs">{[task.resolution, task.ratio].filter(Boolean).join(" · ") || "—"}</TableCell>
                <TableCell className="text-right">
                  {action ? (
                    <Button variant={action === "cancel" ? "outline" : "ghost"} size="sm" onClick={() => setTarget({ task, action })}>
                      <Message id={action === "cancel" ? "tenant.arkTaskCancel" : "tenant.arkTaskDelete"} />
                    </Button>
                  ) : null}
                </TableCell>
              </TableRow>;
            })}
          </TableBody>
        </Table>
      </div>
    )}

    {page ? (
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-xs text-muted-foreground"><Message id="tenant.arkTaskPage" values={{ page: page.pageNum, pages, total: page.total }} /></p>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" disabled={query.pageNum <= 1 || tasks.loading}
            onClick={() => setQuery((q) => ({ ...q, pageNum: Math.max(1, q.pageNum - 1) }))}><Message id="tenant.previousPage" /></Button>
          <Button variant="outline" size="sm" disabled={query.pageNum >= pages || tasks.loading}
            onClick={() => setQuery((q) => ({ ...q, pageNum: q.pageNum + 1 }))}><Message id="tenant.nextPage" /></Button>
        </div>
      </div>
    ) : null}

    <ConfirmDialog
      open={target !== null}
      onOpenChange={(open) => { if (!open) setTarget(null); }}
      title={<Message id={target?.action === "cancel" ? "tenant.arkTaskCancelTitle" : "tenant.arkTaskDeleteTitle"} />}
      description={<>
        <span className="block font-mono text-xs">{target?.task.id}</span>
        <Message id={target?.action === "cancel" ? "tenant.arkTaskCancelBody" : "tenant.arkTaskDeleteBody"} />
      </>}
      confirmLabel={<Message id={target?.action === "cancel" ? "tenant.arkTaskCancel" : "tenant.arkTaskDelete"} />}
      destructive
      onConfirm={act}
    />
  </div>;
}

"use client";

import * as React from "react";
import Link from "next/link";

import { Pager } from "@/components/console/pager";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { useUrlFilters } from "@/components/console/use-url-filters";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { parseJobHistoryEntries, type JobHistoryEntry } from "@/lib/console/contract";
import { formatDateTime } from "@/lib/console/format";

/**
 * The persisted job history — the counterpart to the live view at
 * `/console/jobs`, and the one the control plane's own jobs table backs
 * rather than a Gateway replica's bounded in-memory table. Unlike the live
 * view, a run does not disappear from here when a replica restarts or its
 * table fills up: this list is exactly what STATUS.md's J01-J06 exist to
 * make durable.
 *
 * Filtering and paging are the control plane's, the same division of labor
 * `/console/audit` already uses: a search here answers a question about the
 * tenant's whole history, not about the rows already downloaded.
 *
 * 持久化 job 历史——`/console/jobs` 实时视图的对应物，支撑它的是控制面自己的
 * jobs 表，而不是某个 Gateway 副本那张有上限的内存表。与实时视图不同，一次
 * 运行不会因为副本重启或表满而从这里消失：这份列表正是 STATUS.md 的
 * J01-J06 存在的意义所在——让它持久。
 *
 * 筛选与翻页归控制面，与 `/console/audit` 相同的分工：这里的一次搜索回答的是
 * 关于整个租户历史的问题，而不是关于已经下载到的那些行。
 */
const STATE_LABELS: Record<string, string> = {
  pending: "排队中",
  running: "运行中",
  succeeded: "成功",
  failed: "失败",
  cancelled: "已取消",
};

const STATES = ["pending", "running", "succeeded", "failed", "cancelled"] as const;

const PAGE_SIZES = ["50", "100", "200"] as const;

const NO_ENTRIES: JobHistoryEntry[] = [];

export function HistoryView() {
  const [filters, setFilters] = useUrlFilters([
    "state",
    "workflow_id",
    "since",
    "until",
    "size",
  ] as const);
  const [draftWorkflow, setDraftWorkflow] = React.useState(filters.workflow_id);
  const [lastUrlWorkflow, setLastUrlWorkflow] = React.useState(filters.workflow_id);
  // Reset during render, not in an effect, mirroring usePagedResource's own
  // filter-key reset: a click on "清除筛选" changes the URL, and the input
  // should show that on the very next paint rather than one render behind it.
  //
  // 在渲染期间重置，而不是放进 effect，与 usePagedResource 自己的筛选键重置
  // 一致：点击「清除筛选」会改变 URL，输入框应当在下一次绘制就体现它，而不是
  // 落后一次渲染。
  if (filters.workflow_id !== lastUrlWorkflow) {
    setLastUrlWorkflow(filters.workflow_id);
    setDraftWorkflow(filters.workflow_id);
  }

  const pageSize = PAGE_SIZES.includes(filters.size as (typeof PAGE_SIZES)[number])
    ? Number(filters.size)
    : 100;

  const history = usePagedResource<JobHistoryEntry>({
    path: "/admin/v1/jobs/history",
    filters: {
      state: filters.state,
      workflow_id: filters.workflow_id,
      since: startOfDay(filters.since),
      until: endOfDay(filters.until),
    },
    pageSize,
    parse: parseJobHistoryEntries,
  });

  const rows = history.items ?? NO_ENTRIES;
  const hasFilters =
    filters.state !== "" ||
    filters.workflow_id !== "" ||
    filters.since !== "" ||
    filters.until !== "";

  return (
    <div className="grid gap-4">
      <div>
        <h1 className="font-heading text-lg font-semibold">运行历史</h1>
        <p className="text-sm text-muted-foreground">
          本租户已持久化的 job 记录，来自控制面的 jobs 表。与「运行」页的实时视图不同，
          这里的记录不会因为网关副本重启或内存表满而消失；但状态仍是最后一次被网关观测
          到的快照,不代表后端此刻的真实状态。
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1">
          <label htmlFor="history-state" className="text-xs text-muted-foreground">
            状态
          </label>
          <Select
            value={filters.state === "" ? "all" : filters.state}
            onValueChange={(next) => {
              if (next !== null) {
                setFilters({ state: next === "all" ? "" : next });
              }
            }}
          >
            <SelectTrigger id="history-state">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部状态</SelectItem>
              {STATES.map((state) => (
                <SelectItem key={state} value={state}>
                  {STATE_LABELS[state]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="grid gap-1">
          <label htmlFor="history-workflow" className="text-xs text-muted-foreground">
            工作流 ID
          </label>
          <Input
            id="history-workflow"
            className="w-56 font-mono text-xs"
            value={draftWorkflow}
            onChange={(event) => setDraftWorkflow(event.target.value)}
            onBlur={() => setFilters({ workflow_id: draftWorkflow })}
          />
        </div>

        <div className="grid gap-1">
          <label htmlFor="history-since" className="text-xs text-muted-foreground">
            起始日期（含）
          </label>
          <Input
            id="history-since"
            type="date"
            className="w-40"
            value={filters.since}
            onChange={(event) => setFilters({ since: event.target.value })}
          />
        </div>

        <div className="grid gap-1">
          <label htmlFor="history-until" className="text-xs text-muted-foreground">
            结束日期（含）
          </label>
          <Input
            id="history-until"
            type="date"
            className="w-40"
            value={filters.until}
            onChange={(event) => setFilters({ until: event.target.value })}
          />
        </div>

        <div className="grid gap-1">
          <label htmlFor="history-size" className="text-xs text-muted-foreground">
            每页
          </label>
          <Select
            value={String(pageSize)}
            onValueChange={(next) => {
              if (next !== null) {
                setFilters({ size: next });
              }
            }}
          >
            <SelectTrigger id="history-size">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PAGE_SIZES.map((size) => (
                <SelectItem key={size} value={size}>
                  {size} 条
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {hasFilters ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setDraftWorkflow("");
              setFilters({ state: "", workflow_id: "", since: "", until: "" });
            }}
          >
            清除筛选
          </Button>
        ) : null}
      </div>

      {history.error ? (
        <ErrorState message={history.error} onRetry={history.reload} />
      ) : history.loading ? (
        <LoadingState label="正在加载运行历史" rows={6} />
      ) : rows.length === 0 ? (
        <EmptyState
          title={hasFilters ? "没有符合筛选条件的记录" : "该租户还没有已持久化的运行"}
        />
      ) : (
        <div className="rounded-xl border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Job ID</TableHead>
                <TableHead>工作流</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>提交时间</TableHead>
                <TableHead>最后更新</TableHead>
                <TableHead>结束时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((job) => (
                <TableRow key={job.jobId}>
                  <TableCell className="font-mono text-xs">
                    <Link
                      href={`/console/jobs/history/${encodeURIComponent(job.jobId)}`}
                      className="underline underline-offset-2"
                    >
                      {job.jobId}
                    </Link>
                  </TableCell>
                  <TableCell className="font-mono text-xs">{job.workflowId}</TableCell>
                  <TableCell>
                    <Badge
                      variant={job.state === "succeeded" ? "secondary" : "outline"}
                      className={job.state === "failed" ? "text-destructive" : undefined}
                    >
                      {STATE_LABELS[job.state] ?? job.state}
                    </Badge>
                    {job.errorSummary ? (
                      <p className="mt-1 text-xs text-destructive">{job.errorSummary}</p>
                    ) : null}
                  </TableCell>
                  <TableCell className="text-xs">{formatDateTime(job.createdAt)}</TableCell>
                  <TableCell className="text-xs">{formatDateTime(job.updatedAt)}</TableCell>
                  <TableCell className="text-xs">
                    {job.terminalAt ? formatDateTime(job.terminalAt) : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {history.error || history.loading ? null : (
        <Pager resource={history} loadedCount={rows.length} />
      )}
    </div>
  );
}

/**
 * startOfDay and endOfDay turn a local calendar day into the instants the
 * API takes — the same conversion `/console/audit` performs, and for the
 * same reason: `since` is inclusive and `until` is exclusive server-side.
 *
 * startOfDay 与 endOfDay 把本地日历上的一天转换成 API 所接受的时刻——与
 * `/console/audit` 相同的转换，理由也相同：服务端 `since` 含端点而
 * `until` 不含。
 */
function startOfDay(day: string): string {
  if (day === "") {
    return "";
  }
  const parsed = new Date(`${day}T00:00:00`);
  return Number.isNaN(parsed.getTime()) ? "" : parsed.toISOString();
}

function endOfDay(day: string): string {
  if (day === "") {
    return "";
  }
  const parsed = new Date(`${day}T00:00:00`);
  if (Number.isNaN(parsed.getTime())) {
    return "";
  }
  parsed.setDate(parsed.getDate() + 1);
  return parsed.toISOString();
}

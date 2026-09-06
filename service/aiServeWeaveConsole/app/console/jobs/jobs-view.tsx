"use client";

import * as React from "react";

import { FleetFreshness } from "@/components/console/fleet-freshness";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { parseJobView, type JobView, type WorkflowJob } from "@/lib/console/fleet";
import {
  formatDateTime,
  isTerminalJobState,
  matchesQuery,
  observationAge,
} from "@/lib/console/format";

/**
 * The job list — and the whole page is built around one fact it must not let
 * a reader forget: this is what is running now, not what has run.
 *
 * The Gateway's job table is in process memory, bounded, and per replica. A
 * run disappears when its replica restarts, when the table's bound pushes it
 * out, and it was never visible from another replica to begin with. So the
 * page says so at the top, and says it again whenever the response reports a
 * gap. Persistent history needs a jobs table in the control plane and a write
 * path from the Gateway; until that exists, a page that presented this list as
 * "your runs" would be answering a question nobody can answer yet.
 *
 * Job 列表——整个页面都围绕一个不能让读者忘记的事实构建：这是现在正在跑的，不是跑过的。
 *
 * Gateway 的 job 表位于进程内存、有上限、且每副本各自持有。一次运行会在它所在副本重启时
 * 消失、会被表的上限挤出去，而且它从一开始就不曾在别的副本上可见。因此页面在顶部说明
 * 这一点，并在响应报告出缺口时再说一次。持久化历史需要控制面里的一张 jobs 表和一条来自
 * Gateway 的写路径；在那之前，把这份列表呈现为「你的运行记录」的页面，是在回答一个目前
 * 还没人能回答的问题。
 */
const NO_JOBS: WorkflowJob[] = [];

/** STATE_LABELS mirrors runtime.Workflow* states. An unfamiliar state is shown
 * as it arrived: a state this build does not know is something to see.
 *
 * STATE_LABELS 镜像 runtime.Workflow* 的各状态。不认识的状态原样展示：本次构建不认识的
 * 状态是一件需要被看见的事。 */
const STATE_LABELS: Record<string, string> = {
  pending: "排队中",
  running: "运行中",
  succeeded: "成功",
  failed: "失败",
  cancelled: "已取消",
};

export function JobsView() {
  const view = useResource<JobView>({
    method: "GET",
    path: "/admin/v1/jobs",
    parse: parseJobView,
  });
  const [query, setQuery] = React.useState("");

  // One instant for the whole render, so two rows observed at the same moment
  // never report different ages.
  //
  // 整次渲染共用一个时刻，这样在同一瞬间被观测到的两行，不会报出不同的时长。
  const now = React.useMemo(
    () => (view.data ? new Date(view.data.collectedAt) : new Date()),
    [view.data]
  );
  const jobs = view.data?.jobs ?? NO_JOBS;
  const shown = React.useMemo(
    () =>
      jobs.filter((job) =>
        matchesQuery([job.id, job.workflowId, job.state], query)
      ),
    [jobs, query]
  );

  return (
    <div className="grid gap-4">
      <div>
        <h1 className="font-heading text-lg font-semibold">工作流运行</h1>
        <p className="text-sm text-muted-foreground">
          本租户当前的运行。这里有两条限制，都不是措辞而是事实：
        </p>
        <ul className="mt-2 grid gap-1 text-sm text-muted-foreground">
          <li>
            <strong>这是实时视图，不是历史。</strong>
            网关的 Job 表在进程内存里、有条数上限、且每个副本各自持有，因此副本重启、条数
            超限，或某个副本没有作答，都会让运行从这里消失。持久化的历史需要控制面新建
            Job 表与写入路径，尚未实现。
          </li>
          <li>
            <strong>状态是「最后观测状态」，不是此刻的状态。</strong>
            网关只在提交方轮询 <code className="font-mono text-xs">GET /v1/jobs/&#123;id&#125;</code>
            或挂着事件流时才得知运行有了进展，后台没有对账。因此提交方一旦停止查询，
            即使后端已经完成，这里也会一直停在最后看到的那个状态——下面对未结束的运行标出
            了观测时间，就是为了让这一点可见。
          </li>
        </ul>
        <p className="text-sm text-muted-foreground">
        </p>
      </div>

      {view.error ? (
        <ErrorState message={view.error} onRetry={view.reload} />
      ) : view.loading || !view.data ? (
        <LoadingState label="正在采集运行列表" rows={5} />
      ) : (
        <>
          <FleetFreshness
            collectedAt={view.data.collectedAt}
            replicas={view.data.replicas}
            partial={view.data.partial}
          />

          {view.data.truncated ? (
            <div
              role="alert"
              className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive"
            >
              有副本的 Job 表已达条数上限并丢弃过较早的运行。该表由本部署的所有租户共享，
              因此这里缺少的运行不一定是本租户造成的。
            </div>
          ) : null}

          <div className="flex flex-wrap items-center gap-3">
            <Input
              className="max-w-xs"
              placeholder="筛选 Job ID、工作流或状态"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              aria-label="筛选已采集的运行"
            />
            <span className="text-xs text-muted-foreground">
              显示 {shown.length} / 已采集 {jobs.length} 条；筛选只作用于本次采集到的内容。
            </span>
          </div>

          {shown.length === 0 ? (
            <EmptyState
              title={jobs.length === 0 ? "当前没有正在保留的运行" : "没有符合筛选条件的运行"}
              description={
                jobs.length === 0
                  ? "这不等于本租户从未提交过工作流——已经结束并被表挤出的运行不在这里。"
                  : undefined
              }
            />
          ) : (
            <div className="rounded-xl border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Job ID</TableHead>
                    <TableHead>工作流</TableHead>
                    <TableHead>最后观测状态</TableHead>
                    <TableHead>提交时间</TableHead>
                    <TableHead>观测时间</TableHead>
                    <TableHead>产物</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {shown.map((job) => (
                    <TableRow key={job.id}>
                      <TableCell className="font-mono text-xs">{job.id}</TableCell>
                      <TableCell className="font-mono text-xs">{job.workflowId}</TableCell>
                      <TableCell>
                        <div className="flex flex-wrap items-center gap-2">
                          <Badge
                            variant={job.state === "running" || job.state === "succeeded" ? "secondary" : "outline"}
                            className={job.state === "failed" ? "text-destructive" : undefined}
                          >
                            {STATE_LABELS[job.state] ?? job.state}
                          </Badge>
                          {/* A non-terminal state is shown with its age,
                              because the Gateway stops learning about a run
                              the moment its submitter stops asking. Without
                              this, "运行中" from three hours ago reads exactly
                              like "运行中" from three seconds ago. */}
                          {/* 未结束的状态附上它的观测时间，因为提交方一停止询问，网关就
                              不再得知这次运行的进展。没有它，三小时前的「运行中」与三秒前
                              的「运行中」长得一模一样。 */}
                          {isTerminalJobState(job.state) ? null : (
                            <span className="text-xs text-muted-foreground">
                              观测于 {observationAge(job.updatedAt, now)?.label ?? "未知时间"}
                            </span>
                          )}
                          {/* Shown whenever the backend reported one, not only
                              while pending: a position the backend sent is
                              data, and dropping it because this build expected
                              a different state would be hiding a fact to keep
                              a guess tidy. */}
                          {/* 只要后端报告了就展示，而不是只在 pending 时展示：后端发来的
                              位置是数据，因为本次构建预期的是另一个状态就把它丢掉，等于
                              为了让一个猜测显得整齐而藏起一个事实。 */}
                          {job.queuePosition > 0 ? (
                            <span className="text-xs text-muted-foreground">
                              队列第 {job.queuePosition} 位
                            </span>
                          ) : null}
                        </div>
                        {job.errorSummary ? (
                          <p className="mt-1 text-xs text-destructive">{job.errorSummary}</p>
                        ) : null}
                      </TableCell>
                      <TableCell className="text-xs">{formatDateTime(job.createdAt)}</TableCell>
                      <TableCell className="text-xs">{formatDateTime(job.updatedAt)}</TableCell>
                      <TableCell className="text-xs">
                        {job.artifactIds.length === 0 ? (
                          <span className="text-muted-foreground">无</span>
                        ) : (
                          <span title={job.artifactIds.join("\n")}>
                            {job.artifactIds.length} 个
                          </span>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}

          <p className="text-xs text-muted-foreground">
            产物要用本租户自己的 API Key 从网关数据面下载，本控制台不持有那个 Key，因此这里
            只列出产物 id 而不提供下载。取消运行同理，也走网关数据面。运行落在哪个网关副本上
            属于基础设施信息，与节点清单一样不在租户视图中呈现。
          </p>
        </>
      )}
    </div>
  );
}

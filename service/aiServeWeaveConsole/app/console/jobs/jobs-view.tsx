"use client";
import { ErrorMessage } from "@/components/i18n/error-message";
import { MessageError } from "@/lib/i18n/problem";



import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";



import * as React from "react";
import Link from "next/link";
import { toast } from "sonner";

import { FleetFreshness } from "@/components/console/fleet-freshness";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
const STATE_LABELS = (t: Translator): Record<string, string> => ({
  pending: t("tenant.queued"),
  running: t("tenant.running"),
  succeeded: t("tenant.succeeded"),
  failed: t("tenant.failed"),
  cancelled: t("tenant.canceled"),
});

export function JobsView({ hasGatewayKey }: { hasGatewayKey: boolean }) {
  const { locale, t } = useI18n();
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
    <div className="grid grid-cols-1 gap-4">
      <div>
        <h1 className="font-heading text-lg font-semibold"><Message id="tenant.workflowRuns" /></h1>
        <p className="text-sm text-muted-foreground">
          <Message id="tenant.currentRunsForThisTenantThisViewHas" /></p>
        <ul className="mt-2 grid gap-1 text-sm text-muted-foreground">
          <li><Message id="tenant.eachGatewayReplicaKeepsItsOwnJobTable_ed5df5" values={{ strong: <strong><Message id="tenant.thisIsALiveViewNotAHistory" /></strong> }} /></li>
          <li><Message id="tenant.theGatewayLearnsAboutProgressOnlyWhenThe_2ed359" values={{ strong: <strong><Message id="tenant.statusMeansLastObservedStatusNotCurrentStatus" /></strong>, code: <code className="font-mono text-xs">GET /v1/jobs/&#123;id&#125;</code> }} /></li>
        </ul>
        <p className="text-sm text-muted-foreground">
        </p>
      </div>

      {view.error ? (
        <ErrorState message={view.error} onRetry={view.reload} />
      ) : view.loading || !view.data ? (
        <LoadingState label={t("tenant.collectingRunList")} rows={5} />
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
              <Message id="tenant.aReplicaSJobTableHasReachedIts" /></div>
          ) : null}

          <div className="flex flex-wrap items-center gap-3">
            <Input
              className="max-w-xs"
              placeholder={t("tenant.filterByJobIdWorkflowOrStatus")}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              aria-label={t("tenant.filterCollectedRuns")}
            />
            <span className="text-xs text-muted-foreground"><Message id="tenant.recordsShownCollectedFiltersApplyOnlyToThe" values={{ count: shown.length, count2: jobs.length }} /></span>
          </div>

          {shown.length === 0 ? (
            <EmptyState
              title={jobs.length === 0 ? t("tenant.noRunsAreCurrentlyRetained") : t("tenant.noRunsMatchTheFilters")}
              description={
                jobs.length === 0
                  ? t("tenant.thisDoesNotMeanTheTenantHasNever")
                  : undefined
              }
            />
          ) : (
            <div className="rounded-xl border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Job ID</TableHead>
                    <TableHead><Message id="tenant.workflows" /></TableHead>
                    <TableHead><Message id="tenant.lastObservedStatus" /></TableHead>
                    <TableHead><Message id="tenant.submittedAt" /></TableHead>
                    <TableHead><Message id="tenant.observedAt" /></TableHead>
                    <TableHead><Message id="tenant.artifacts" /></TableHead>
                    <TableHead><Message id="tenant.actions" /></TableHead>
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
                            {STATE_LABELS(t)[job.state] ?? job.state}
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
                            <span className="text-xs text-muted-foreground"><Message id="tenant.observedAt_dcb238" values={{ value: observationAge(job.updatedAt, now, locale)?.label ?? t("tenant.unknownTime") }} /></span>
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
                            <span className="text-xs text-muted-foreground"><Message id="tenant.queuePosition" values={{ queuePosition: job.queuePosition }} /></span>
                          ) : null}
                        </div>
                        {job.errorSummary ? (
                          <p className="mt-1 text-xs text-destructive">{job.errorSummary}</p>
                        ) : null}
                      </TableCell>
                      <TableCell className="text-xs">{formatDateTime(job.createdAt, undefined, locale)}</TableCell>
                      <TableCell className="text-xs">{formatDateTime(job.updatedAt, undefined, locale)}</TableCell>
                      <TableCell className="text-xs">
                        {job.artifactIds.length === 0 ? (
                          <span className="text-muted-foreground"><Message id="tenant.none" /></span>
                        ) : hasGatewayKey ? (
                          <ArtifactLinks artifactIds={job.artifactIds} />
                        ) : (
                          <span title={job.artifactIds.join("\n")}><Message id="tenant.items_e8aa60" values={{ count: job.artifactIds.length }} /></span>
                        )}
                      </TableCell>
                      <TableCell>
                        {isTerminalJobState(job.state) ? (
                          <span className="text-xs text-muted-foreground">—</span>
                        ) : hasGatewayKey ? (
                          <CancelButton jobId={job.id} onCancelled={view.reload} />
                        ) : (
                          <span className="text-xs text-muted-foreground">
                            <Message id="tenant.noKeyConfigured" /></span>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}

          <p className="text-xs text-muted-foreground"><Message id="tenant.theGatewayReplicaRunningAJobIsInfrastructure_21a180" values={{ value: hasGatewayKey
              ? t("tenant.cancellationAndArtifactDownloadsUseTheGatewayData")
              : t("tenant.cancellationAndArtifactDownloadsUseTheGatewayData_292c9a"), value2: hasGatewayKey ? null : (
              <>
                {" "}
                <Link href="/console/settings" className="underline underline-offset-2">
                  <Message id="tenant.goToSettings" /></Link>
              </>
            ) }} /></p>
        </>
      )}
    </div>
  );
}

/**
 * CancelButton posts to the Gateway-proxy cancel route and reloads the list
 * on success. It does not optimistically mark the row cancelled: cancel is a
 * request, not a conclusion — ComfyUI's own interrupt is asynchronous, and
 * the Gateway itself still answers with whatever it last knew until the real
 * result comes back. Claiming "cancelled" here before the Gateway does would
 * be this console fabricating a result nobody reported, the same failure
 * mode the Gateway's own README explicitly rules out for itself.
 *
 * CancelButton 向经由网关的取消代理路由发起 POST，成功后重新加载列表。它不会
 * 乐观地把这一行标记为已取消：取消是一次请求，不是一个结论——ComfyUI 自己的
 * 中断是异步的，在真正的结果回来之前，Gateway 本身仍然只会回答它最后知道的
 * 状态。在 Gateway 之前就在这里宣称"已取消"，会是本控制台在编造一个没人报告过
 * 的结果，而这正是 Gateway 自己的 README 明确为自己排除掉的那种失败模式。
 */
export function CancelButton({
  jobId,
  onCancelled,
}: {
  jobId: string;
  onCancelled: () => void;
}) {
  const {t } = useI18n();
  const [pending, setPending] = React.useState(false);

  async function cancel() {
    if (pending) {
      return;
    }
    setPending(true);
    try {
      const response = await fetch(
        `/api/gateway/jobs/${encodeURIComponent(jobId)}/cancel`,
        { method: "POST" }
      );
      if (response.ok) {
        toast.success(<Message id={"tenant.cancellationRequestSent"} />);
        onCancelled();
        return;
      }
      const body = (await response.json().catch(() => null)) as
        | { error?: string }
        | null;
      toast.error(<ErrorMessage error={gatewayFailure(body?.error, response.status)} />);
    } catch {
      toast.error(<Message id={"tenant.unableToConnectToTheGatewayCheckYour"} />);
    } finally {
      setPending(false);
    }
  }

  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      disabled={pending}
      onClick={cancel}
    >
      {pending ? t("tenant.canceling") : t("common.cancel")}
    </Button>
  );
}

/** ArtifactLinks renders one download link per artifact id, each a plain
 * navigation to the Gateway-proxy artifact route rather than a fetch: the
 * body can be a multi-megabyte image or video, and a browser download link
 * streams it without this component ever holding the bytes.
 *
 * ArtifactLinks 为每个产物 id 渲染一条下载链接，每一条都是对经由网关的产物代理
 * 路由的一次普通导航，而不是一次 fetch：响应体可能是数兆字节的图片或视频，
 * 浏览器的下载链接会把它串流下来，本组件从不持有这些字节。 */
export function ArtifactLinks({ artifactIds }: { artifactIds: string[] }) {
  return (
    <div className="flex flex-col gap-0.5">
      {artifactIds.map((id) => (
        <a
          key={id}
          href={`/api/gateway/artifacts/${encodeURIComponent(id)}`}
          className="underline underline-offset-2"
          title={id}
        >
          <Message id="tenant.download" /></a>
      ))}
    </div>
  );
}

/** gatewayFailure turns the error code the two Gateway-proxy routes
 * report into on-screen text. These routes do not go through
 * lib/console/api-client.ts's ApiErrorKind, so the mapping lives here rather
 * than in lib/console/errors.ts — see describeSettingsError in
 * app/console/settings/settings-view.tsx for the same reasoning applied to
 * the Gateway Key form.
 *
 * gatewayFailure 把两个网关代理路由报告的错误代号转换成屏幕上的文案。这些
 * 路由不经由 lib/console/api-client.ts 的 ApiErrorKind，因此这份映射放在这里，
 * 而不是 lib/console/errors.ts——同样的推理用在了 Gateway Key 表单上，见
 * app/console/settings/settings-view.tsx 的 describeSettingsError。 */
export function gatewayFailure(code: string | undefined, status: number): MessageError {
  switch (code) {
    case "gateway_key_missing":
      return new MessageError("tenant.noGatewayKeyIsConfiguredConfigureOneOn");
    case "gateway_key_rejected":
      return new MessageError("tenant.theConfiguredGatewayKeyIsNoLongerValid");
    case "forbidden_origin":
      return new MessageError("tenant.requestOriginValidationFailedRefreshThePageAnd");
    default:
      return status === 504 || status === 502
        ? new MessageError("tenant.unableToConnectToTheGatewayPleaseTry")
        : new MessageError("tenant.theOperationFailedPleaseTryAgainLater");
  }
}

"use client";

import Link from "next/link";

import { CancelButton } from "@/app/console/jobs/jobs-view";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { parseJobHistoryDetail, type JobArtifact, type JobHistoryEntry } from "@/lib/console/contract";
import { formatDateTime, isTerminalJobState } from "@/lib/console/format";

/**
 * One persisted job's detail: the fields `/console/jobs/history`'s list view
 * has no room for, plus its recorded artifacts.
 *
 * Artifacts here come from the control plane's job_artifacts table — a
 * genuinely separate fact from the live view's `artifactIds`, which the
 * Gateway mints from its own in-memory listing and never persists under the
 * same id space the two happen to look alike. They line up because the
 * Gateway now reports every minted artifact id to the control plane
 * (`jobPersister.persistArtifacts`), but a job whose replica restarted before
 * that report went out can still show fewer artifacts here than the live
 * view once had.
 *
 * 一个持久化 job 的详情：`/console/jobs/history` 列表页放不下的字段，加上它已
 * 记录的产物。
 *
 * 这里的产物来自控制面的 job_artifacts 表——与实时视图的 `artifactIds` 是两个
 * 确实独立的事实，只是碰巧长得很像：Gateway 从自己内存里的列举铸造后者，从不
 * 持久化到同一个 id 空间。两者对得上，是因为 Gateway 现在会把每一个铸造出的
 * 产物 id 都上报给控制面（`jobPersister.persistArtifacts`），但一个副本在
 * 上报发出之前就重启过的 job，这里显示的产物仍可能比实时视图当初看到的更少。
 */
const STATE_LABELS: Record<string, string> = {
  pending: "排队中",
  running: "运行中",
  succeeded: "成功",
  failed: "失败",
  cancelled: "已取消",
};

export function DetailView({
  jobId,
  hasGatewayKey,
}: {
  jobId: string;
  hasGatewayKey: boolean;
}) {
  const job = useResource<JobHistoryEntry>({
    method: "GET",
    path: `/admin/v1/jobs/history/${encodeURIComponent(jobId)}`,
    parse: parseJobHistoryDetail,
  });

  return (
    <div className="grid gap-4">
      <div>
        <Link
          href="/console/jobs/history"
          className="text-sm text-muted-foreground underline underline-offset-2"
        >
          ← 返回运行历史
        </Link>
        <h1 className="mt-1 font-heading text-lg font-semibold">
          <span className="font-mono">{jobId}</span>
        </h1>
      </div>

      {job.error ? (
        <ErrorState message={job.error} onRetry={job.reload} />
      ) : job.loading || !job.data ? (
        <LoadingState label="正在加载 job 详情" rows={4} />
      ) : (
        <>
          <dl className="grid grid-cols-2 gap-x-6 gap-y-3 rounded-xl border p-4 text-sm sm:grid-cols-3">
            <div>
              <dt className="text-xs text-muted-foreground">工作流</dt>
              <dd className="font-mono text-xs">
                {job.data.workflowId}
                {job.data.workflowVersion ? `@${job.data.workflowVersion}` : ""}
              </dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">状态</dt>
              <dd>
                <Badge
                  variant={job.data.state === "succeeded" ? "secondary" : "outline"}
                  className={job.data.state === "failed" ? "text-destructive" : undefined}
                >
                  {STATE_LABELS[job.data.state] ?? job.data.state}
                </Badge>
              </dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">提交时间</dt>
              <dd className="text-xs">{formatDateTime(job.data.createdAt)}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">最后更新</dt>
              <dd className="text-xs">{formatDateTime(job.data.updatedAt)}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">结束时间</dt>
              <dd className="text-xs">
                {job.data.terminalAt ? formatDateTime(job.data.terminalAt) : "尚未结束"}
              </dd>
            </div>
            {job.data.errorSummary ? (
              <div className="col-span-full">
                <dt className="text-xs text-muted-foreground">错误摘要</dt>
                <dd className="text-xs text-destructive">{job.data.errorSummary}</dd>
              </div>
            ) : null}
          </dl>

          {isTerminalJobState(job.data.state) ? null : (
            <div className="flex items-center gap-2">
              {hasGatewayKey ? (
                <CancelButton jobId={jobId} onCancelled={job.reload} />
              ) : (
                <p className="text-xs text-muted-foreground">
                  该运行尚未结束，但未配置 Gateway Key，无法在此取消；前往
                  <Link href="/console/settings" className="underline underline-offset-2">
                    设置
                  </Link>
                  页配置。
                </p>
              )}
            </div>
          )}

          <div>
            <h2 className="text-sm font-semibold">产物</h2>
            {job.data.artifacts === null || job.data.artifacts.length === 0 ? (
              <EmptyState
                title="没有已记录的产物"
                description="控制面的 job_artifacts 表里还没有这个 job 的记录——它可能确实没有产出，也可能是持久化尚未追上（见上方页面顶部的说明）。"
              />
            ) : hasGatewayKey ? (
              <div className="mt-2 grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4">
                {job.data.artifacts.map((artifact) => (
                  <ArtifactPreview key={artifact.artifactId} artifact={artifact} />
                ))}
              </div>
            ) : (
              <p className="mt-2 text-xs text-muted-foreground">
                共 {job.data.artifacts.length} 个产物；预览与下载走网关数据面，需要先在
                <Link href="/console/settings" className="underline underline-offset-2">
                  设置
                </Link>
                页配置 Gateway Key。
              </p>
            )}
          </div>
        </>
      )}
    </div>
  );
}

/** MEDIA_EXTENSIONS maps a filename's extension to how it should be
 * previewed. An extension this build does not recognize falls back to a
 * plain download link — guessing wrong about a binary body is worse than not
 * guessing.
 *
 * MEDIA_EXTENSIONS 把文件名的扩展名映射到应当如何预览。本次构建不认识的扩展名
 * 一律退回普通下载链接——对一段二进制内容猜错，比不猜更糟。 */
const MEDIA_EXTENSIONS: Record<string, "image" | "video"> = {
  png: "image",
  jpg: "image",
  jpeg: "image",
  gif: "image",
  webp: "image",
  bmp: "image",
  mp4: "video",
  webm: "video",
  mov: "video",
};

function guessMediaKind(filename: string): "image" | "video" | null {
  const dot = filename.lastIndexOf(".");
  if (dot < 0) {
    return null;
  }
  return MEDIA_EXTENSIONS[filename.slice(dot + 1).toLowerCase()] ?? null;
}

/** ArtifactPreview renders an inline image or video for a recognized
 * extension, falling back to a plain download link otherwise. Either way the
 * browser fetches the bytes directly from the Gateway-proxy artifact route —
 * this component never holds them.
 *
 * ArtifactPreview 为可识别的扩展名渲染内联的图片或视频，否则退回普通下载链接。
 * 无论哪种情形，浏览器都直接从经由网关的产物代理路由取字节——本组件从不持有
 * 它们。 */
function ArtifactPreview({ artifact }: { artifact: JobArtifact }) {
  const href = `/api/gateway/artifacts/${encodeURIComponent(artifact.artifactId)}`;
  const kind = guessMediaKind(artifact.filename);

  return (
    <a
      href={href}
      title={artifact.filename}
      className="grid gap-1 rounded-lg border p-2 text-xs hover:bg-muted/40"
    >
      {kind === "image" ? (
        // eslint-disable-next-line @next/next/no-img-element -- streamed straight from the Gateway route, not a Next-optimized asset
        <img src={href} alt={artifact.filename} className="aspect-square rounded object-cover" />
      ) : kind === "video" ? (
        <video src={href} controls className="aspect-square rounded object-cover" />
      ) : (
        <div className="flex aspect-square items-center justify-center rounded bg-muted text-muted-foreground">
          下载
        </div>
      )}
      <span className="truncate font-mono">{artifact.filename}</span>
    </a>
  );
}

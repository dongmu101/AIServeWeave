"use client";

import * as React from "react";
import Link from "next/link";
import { ArrowLeftIcon, PencilIcon } from "lucide-react";
import { toast } from "sonner";

import { failureText, studioCall } from "@/components/studio/flows/api";
import { StatusBadge } from "@/components/studio/flows/run-view";
import { Button } from "@/components/ui/button";
import { duration } from "@/lib/studio/flow-run";
import { parseRunPage, runErrorText, type Run } from "@/lib/studio/flows";

/**
 * RunHistory lists a flow's runs, newest first, with a button for older ones.
 * RunHistory 列出编排的运行记录，最新的在前，并可加载更早的记录。
 */
export function RunHistory({ flowId, flowName, runs: first, next: firstNext, error }: { flowId: string; flowName: string; runs: Run[]; next: string; error: string }) {
  const [runs, setRuns] = React.useState(first);
  const [next, setNext] = React.useState(firstNext);
  const [busy, setBusy] = React.useState(false);

  async function more() {
    setBusy(true);
    const result = await studioCall("GET", `flows/${flowId}/runs?cursor=${encodeURIComponent(next)}&limit=30`, undefined, parseRunPage);
    setBusy(false);
    if (!result.ok) {
      toast.error(failureText(result.code));
      return;
    }
    setRuns([...runs, ...result.data.runs]);
    setNext(result.data.next);
  }

  return (
    <div className="mx-auto grid w-full max-w-4xl gap-6 px-4 py-8 sm:px-6">
      <div className="flex items-center gap-3">
        <Link href="/flows" aria-label="返回编排列表" className="inline-flex size-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-muted hover:text-foreground">
          <ArrowLeftIcon className="size-4" />
        </Link>
        <div className="min-w-0 flex-1">
          <h1 className="truncate font-heading text-2xl font-medium">{flowName}</h1>
          <p className="text-sm text-muted-foreground">运行记录</p>
        </div>
        <Link href={`/flows/${flowId}`} className="inline-flex h-8 items-center gap-1.5 rounded-lg border px-2.5 text-sm hover:bg-muted">
          <PencilIcon className="size-4" />
          编辑
        </Link>
      </div>
      {error ? <p className="text-sm text-muted-foreground">运行记录加载失败：{error}</p> : null}
      {!error && runs.length === 0 ? <p className="rounded-2xl border border-dashed px-6 py-12 text-center text-sm text-muted-foreground">还没有运行过。在编辑页点「运行」开始第一次运行。</p> : null}
      {runs.length > 0 ? (
        <ul className="divide-y rounded-2xl border" aria-label="运行记录">
          {runs.map((run) => (
            <li key={run.id}>
              <Link href={`/flows/${flowId}/runs/${run.id}`} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3 hover:bg-muted/50">
                <StatusBadge status={run.status} />
                <span className="text-sm">{run.createdAt ? new Date(run.createdAt).toLocaleString("zh-CN") : "—"}</span>
                <span className="text-xs text-muted-foreground">第 {run.flowVersion} 版</span>
                <span className="text-xs text-muted-foreground">{duration(run.createdAt, run.finishedAt) || (run.status === "running" ? "进行中" : "")}</span>
                <span className="ml-auto text-xs text-muted-foreground">
                  {run.usage.promptTokens + run.usage.completionTokens > 0 ? `${run.usage.promptTokens + run.usage.completionTokens} tokens` : ""}
                </span>
                {run.status === "failed" && run.errorCode ? <span className="w-full text-xs text-destructive">{runErrorText(run.errorCode)}</span> : null}
              </Link>
            </li>
          ))}
        </ul>
      ) : null}
      {next ? (
        <Button variant="outline" className="justify-self-center" disabled={busy} onClick={() => void more()}>
          {busy ? "加载中…" : "加载更多"}
        </Button>
      ) : null}
    </div>
  );
}

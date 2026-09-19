"use client";

import * as React from "react";

import { ErrorState, FormError } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  modelPullStatusRequest,
  modelPullTriggerRequest,
  type ModelPullStatusResult,
} from "@/lib/console/model-pull";
import { describe } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";
import { nodePathSegment } from "@/lib/console/node-path";

const STATE_LABELS: Record<string, string> = {
  pending: "排队中", downloading: "下载中", done: "已完成", failed: "失败", unspecified: "未知",
};

const REASON_LABELS: Record<string, string> = {
  unknown_name: "名字不在该节点本地清单里",
  invalid_spec: "清单条目本身格式不对",
  not_allowlisted: "来源不在该节点本地白名单内",
  quota_exceeded: "单次运行的字节预算已耗尽",
  ledger_quota_exceeded: "跨重启的累计账本已耗尽",
  disk_space_low: "目标磁盘剩余空间过低",
  fetch_failed: "网络请求失败",
  unexpected_http_status: "来源返回了非预期的 HTTP 状态",
  checksum_mismatch: "校验和不匹配",
  storage_error: "本地磁盘写入失败",
  ollama_unconfigured: "该节点未配置 Ollama",
  ollama_pull_failed: "Ollama 服务器自己报告了错误",
};

/** formatBytes renders a byte count as a human-scaled string, or "—" for an
 * unknown total (BytesTotal is 0 when the manifest entry's size was never
 * declared).
 *
 * formatBytes 把字节数渲染成人类可读的量级字符串；未知的总量（清单条目从
 * 未声明大小时 BytesTotal 为 0）渲染为 "—"。 */
function formatBytes(bytes: number): string {
  if (bytes <= 0) {
    return "—";
  }
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

/** ModelPullView is the per-node lookup + trigger panel described in
 * page.tsx's doc comment.
 *
 * ModelPullView 是 page.tsx 文档注释所述的按节点查询与触发面板。 */
export function ModelPullView() {
  const run = useConsoleRequest();
  const [nodeIdInput, setNodeIdInput] = React.useState("");
  const [nodeId, setNodeId] = React.useState<string | null>(null);
  const [status, setStatus] = React.useState<ModelPullStatusResult | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [queryError, setQueryError] = React.useState<string | null>(null);
  const [notice, setNotice] = React.useState<string | null>(null);
  const [namesInput, setNamesInput] = React.useState("");
  const [triggerPending, setTriggerPending] = React.useState(false);
  const [triggerError, setTriggerError] = React.useState<string | null>(null);

  async function query(event: React.FormEvent) {
    event.preventDefault();
    const id = nodeIdInput.trim();
    if (!nodePathSegment(id)) {
      setQueryError("节点 ID 不能为空，且不能包含路径分隔符或控制字符。");
      return;
    }
    setLoading(true);
    setQueryError(null);
    setNotice(null);
    try {
      const result = await run(modelPullStatusRequest(id));
      setStatus(result);
      setNodeId(id);
    } catch (failure) {
      setQueryError(describe(failure));
      setStatus(null);
      setNodeId(null);
    } finally {
      setLoading(false);
    }
  }

  async function reload() {
    if (!nodeId) {
      return;
    }
    setLoading(true);
    setQueryError(null);
    try {
      setStatus(await run(modelPullStatusRequest(nodeId)));
    } catch (failure) {
      setQueryError(describe(failure));
    } finally {
      setLoading(false);
    }
  }

  async function triggerPull(event: React.FormEvent) {
    event.preventDefault();
    if (!nodeId) {
      return;
    }
    const names = namesInput
      .split(/[\n,]/)
      .map((name) => name.trim())
      .filter((name) => name.length > 0);
    if (names.length === 0) {
      setTriggerError("至少填写一个名字。");
      return;
    }
    setTriggerPending(true);
    setTriggerError(null);
    setNotice(null);
    try {
      const outcome = await run(modelPullTriggerRequest(nodeId, names));
      const connected = outcome.replicas.filter((replica) => replica.connected).length;
      setNotice(`已下发触发 ${names.join(", ")}：${connected}/${outcome.replicas.length} 个已配置副本报告该节点已连接。是否真的开始下载需要重新查询状态确认。`);
      setNamesInput("");
    } catch (failure) {
      setTriggerError(describe(failure));
    } finally {
      setTriggerPending(false);
    }
  }

  return <div className="grid gap-8">
    <section className="grid gap-4" aria-labelledby="model-pull-title">
      <div>
        <h1 id="model-pull-title" className="font-heading text-lg font-semibold">模型拉取</h1>
        <p className="text-sm text-muted-foreground">
          查询并触发某个节点本地清单里已声明模型的拉取（STATUS.md 的 P2 模型分发）。触发只在该节点连到本次询问所到达的某个已配置 Gateway 副本时才生效；一次触发的 202 只确认已下发到隧道，不确认下载已经真的开始或完成——请之后重新查询状态确认结果。来源 URL、校验和、白名单与配额永远只存在于该节点自己的本地配置，这个页面从不能指定它们，只能按名字触发该节点本地清单里已经声明过的条目。
        </p>
      </div>
      <form onSubmit={query} className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1.5">
          <label htmlFor="node-id" className="text-sm font-medium">节点 ID</label>
          <Input id="node-id" className="max-w-xs" value={nodeIdInput} onChange={(event) => setNodeIdInput(event.target.value)} placeholder="例如 mac-mini-01" aria-label="节点 ID" />
        </div>
        <SubmitButton pending={loading}>查询状态</SubmitButton>
        {nodeId ? <Button type="button" variant="outline" disabled={loading} onClick={reload}>重新查询</Button> : null}
      </form>
      <FormError message={queryError} />
      {notice ? <p role="status" className="text-sm">{notice}</p> : null}
    </section>

    {nodeId && !queryError ? <section className="grid gap-4" aria-labelledby="model-pull-node-title">
      <h2 id="model-pull-node-title" className="font-heading text-base font-semibold">
        <code className="break-all">{nodeId}</code>
      </h2>

      <div className="grid gap-2">
        <h3 className="text-sm font-medium">已知拉取</h3>
        {status && status.pulls.length > 0 ? <ul className="grid gap-2">
          {status.pulls.map((pull) => <li key={pull.name} className="grid gap-1 rounded-xl border p-3 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <code>{pull.name}</code>
              <Badge variant={pull.state === "done" ? "default" : pull.state === "failed" ? "destructive" : "outline"}>
                {STATE_LABELS[pull.state] ?? pull.state}
              </Badge>
              <span className="text-xs text-muted-foreground">更新于 {formatDateTime(pull.updatedAt)}</span>
            </div>
            <div className="text-xs text-muted-foreground">
              {formatBytes(pull.bytesDownloaded)} / {formatBytes(pull.bytesTotal)}
              {pull.reason ? <> — {REASON_LABELS[pull.reason] ?? pull.reason}</> : null}
            </div>
          </li>)}
        </ul> : <p className="text-sm text-muted-foreground">该节点尚未上报过任何拉取状态。</p>}
      </div>

      <form onSubmit={triggerPull} className="grid gap-3 rounded-xl border p-4">
        <h3 className="text-sm font-medium">触发拉取</h3>
        <p className="text-xs text-muted-foreground">按逗号或换行分隔多个名字；每个名字都必须已经声明在该节点自己的本地清单里，这里从不能指定来源 URL。</p>
        <div className="grid gap-1.5">
          <label htmlFor="pull-names" className="text-sm font-medium">名字</label>
          <textarea
            id="pull-names"
            className="min-h-20 max-w-md rounded-md border bg-transparent px-3 py-2 text-sm"
            value={namesInput}
            onChange={(event) => setNamesInput(event.target.value)}
            placeholder={"qwen3-coder:30b\nllama3.1:8b"}
            aria-label="要触发的模型名字"
          />
        </div>
        <div>
          <SubmitButton pending={triggerPending}>触发</SubmitButton>
        </div>
        <FormError message={triggerError} />
      </form>

      <div className="grid gap-2">
        <h3 className="text-sm font-medium">已配置副本</h3>
        {status && status.replicas.length > 0 ? <ul className="grid gap-1 text-sm">
          {status.replicas.map((replica) => <li key={replica.endpoint}>
            <code>{replica.endpoint}</code> — <Badge variant={replica.connected ? "default" : "outline"}>{replica.connected ? "已连接" : "未连接"}</Badge>
            {replica.error ? <span className="text-muted-foreground"> {replica.error}</span> : null}
          </li>)}
        </ul> : <p className="text-sm text-muted-foreground">没有副本报告过该节点。</p>}
      </div>
    </section> : null}

    {queryError ? <ErrorState message={queryError} onRetry={nodeId ? reload : undefined} /> : null}
  </div>;
}

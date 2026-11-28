"use client";

import * as React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { CopyIcon, DownloadIcon, EllipsisIcon, PencilIcon, PlusIcon, Trash2Icon, UploadIcon, WorkflowIcon } from "lucide-react";
import { toast } from "sonner";

import { failureText, studioCall } from "@/components/studio/flows/api";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { formatUpdatedAt } from "@/lib/studio/conversation-search";
import { newDefinition } from "@/lib/studio/flow-graph";
import { issueText, parseFlow, type FlowDefinition, type FlowIssue, type FlowSummary } from "@/lib/studio/flows";
import { flowFile, readFlowFile } from "@/lib/studio/run-inputs";

/**
 * FlowList is the user's flows: open, create, import, rename, duplicate,
 * export and delete.
 *
 * FlowList 是用户的编排列表：打开、新建、导入、重命名、复制、导出与删除。
 */
export function FlowList({ flows, error }: { flows: FlowSummary[]; error: string }) {
  const router = useRouter();
  const [busy, setBusy] = React.useState(false);
  const file = React.useRef<HTMLInputElement>(null);
  const [now] = React.useState(() => new Date());

  async function create(name: string, definition: FlowDefinition, editor?: Window | null): Promise<boolean> {
    setBusy(true);
    const result = await studioCall("POST", "flows", { name, definition }, parseFlow);
    setBusy(false);
    if (!result.ok) {
      editor?.close();
      toast.error(result.code === "invalid_flow" ? invalidText(result.issues) : failureText(result.code));
      return false;
    }
    const href = `/flows/${result.data.id}`;
    if (editor) {
      if (!editor.closed) editor.location.replace(new URL(href, window.location.origin));
      router.refresh();
    } else {
      router.push(href);
    }
    return true;
  }

  function createNew() {
    // Reserve the tab during the click, before waiting for the new flow ID.
    // 在点击时先打开标签页，再等待服务端返回编排 ID。
    const editor = window.open("about:blank", "_blank");
    if (editor) editor.opener = null;
    void create("未命名编排", newDefinition(), editor);
  }

  async function importFile(chosen: File | undefined) {
    if (!chosen) return;
    if (chosen.size > 1 << 20) {
      toast.error("文件太大，编排文件不能超过 1 MiB。");
      return;
    }
    const parsed = readFlowFile(await chosen.text());
    if (!parsed) {
      toast.error("这不是一个编排文件。");
      return;
    }
    await create(parsed.name || chosen.name.replace(/\.json$/i, "") || "导入的编排", parsed.definition);
  }

  return (
    <div className="mx-auto grid w-full max-w-4xl gap-6 px-4 py-8 sm:px-6">
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-0 flex-1">
          <h1 className="font-heading text-2xl font-medium">编排</h1>
          <p className="mt-1 text-sm text-muted-foreground">把多次模型调用、条件判断和文本拼接串成一条可以反复运行的流程。</p>
        </div>
        <input ref={file} type="file" accept="application/json,.json" className="hidden" onChange={(event) => {
          void importFile(event.target.files?.[0]);
          event.target.value = "";
        }} />
        <Button variant="outline" disabled={busy} onClick={() => file.current?.click()}>
          <UploadIcon />
          导入
        </Button>
        <Button disabled={busy} onClick={createNew}>
          <PlusIcon />
          新建编排
        </Button>
      </div>

      {error ? <p className="text-sm text-muted-foreground">编排列表加载失败：{error}</p> : null}
      {!error && flows.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-2xl border border-dashed px-6 py-16 text-center">
          <WorkflowIcon className="size-8 text-muted-foreground" />
          <p className="font-medium">还没有编排</p>
          <p className="max-w-sm text-sm text-muted-foreground">新建一个编排，在画布上用 LLM、文本模板和条件节点组装流程，保存后就能带着输入反复运行。</p>
          <Button disabled={busy} onClick={createNew}>
            <PlusIcon />
            新建编排
          </Button>
        </div>
      ) : null}
      {flows.length > 0 ? (
        <ul className="divide-y rounded-2xl border" aria-label="编排列表">
          {flows.map((flow) => (
            <FlowRow key={flow.id} flow={flow} now={now} onDuplicate={create} />
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function invalidText(issues: FlowIssue[]): string {
  return issues.length > 0 ? `编排有 ${issues.length} 个问题，例如：${issueText(issues[0])}` : "编排有错误。";
}

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = `${name.trim() || "flow"}.json`;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function FlowRow({ flow, now, onDuplicate }: { flow: FlowSummary; now: Date; onDuplicate: (name: string, definition: FlowDefinition) => Promise<boolean> }) {
  const router = useRouter();
  const [mode, setMode] = React.useState<"view" | "rename" | "confirm">("view");
  const [draft, setDraft] = React.useState(flow.name);
  const [busy, setBusy] = React.useState(false);

  async function load() {
    const result = await studioCall("GET", `flows/${flow.id}`, undefined, parseFlow);
    if (!result.ok) toast.error(failureText(result.code));
    return result.ok ? result.data : null;
  }

  async function rename() {
    const next = draft.trim();
    setMode("view");
    if (next === "" || next === flow.name) return;
    const result = await studioCall("PATCH", `flows/${flow.id}`, { name: next });
    if (!result.ok) toast.error(failureText(result.code));
    router.refresh();
  }

  async function remove() {
    setBusy(true);
    const result = await studioCall("DELETE", `flows/${flow.id}`);
    setBusy(false);
    setMode("view");
    if (!result.ok) toast.error(failureText(result.code));
    router.refresh();
  }

  if (mode === "rename") {
    return (
      <li className="flex items-center gap-2 px-4 py-3">
        <input autoFocus value={draft} maxLength={200} aria-label="新名称" onChange={(event) => setDraft(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") void rename();
            if (event.key === "Escape") setMode("view");
          }}
          className="h-8 min-w-0 flex-1 rounded-lg border border-input bg-background px-2.5 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50" />
        <Button size="sm" onClick={() => void rename()}>保存</Button>
        <Button size="sm" variant="ghost" onClick={() => setMode("view")}>取消</Button>
      </li>
    );
  }
  if (mode === "confirm") {
    return (
      <li className="flex items-center gap-2 bg-destructive/5 px-4 py-3 text-sm">
        <span className="min-w-0 flex-1">删除「{flow.name}」？它的所有版本与运行记录也会一并删除。</span>
        <Button size="sm" variant="destructive" disabled={busy} onClick={() => void remove()}>删除</Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => setMode("view")}>取消</Button>
      </li>
    );
  }
  return (
    <li className="group/item relative flex items-center gap-3 px-4 py-3 hover:bg-muted/50">
      <WorkflowIcon className="size-5 shrink-0 text-muted-foreground" />
      <Link href={`/flows/${flow.id}`} target="_blank" rel="noopener noreferrer" className="min-w-0 flex-1 after:absolute after:inset-0">
        <span className="block truncate font-medium">{flow.name || "未命名编排"}</span>
        <span className="block text-xs text-muted-foreground">
          第 {flow.version} 版 · {flow.updatedAt ? formatUpdatedAt(flow.updatedAt, now) : ""}
        </span>
      </Link>
      <Link href={`/flows/${flow.id}/runs`} className="relative z-10 rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-muted hover:text-foreground max-sm:hidden">
        运行记录
      </Link>
      <DropdownMenu>
        <DropdownMenuTrigger render={<button type="button" aria-label={`管理编排 ${flow.name}`} className="relative z-10 inline-flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground" />}>
          <EllipsisIcon className="size-4" />
        </DropdownMenuTrigger>
        <DropdownMenuContent side="bottom" align="end" className="w-36">
          <DropdownMenuItem onClick={() => { setDraft(flow.name); setMode("rename"); }}>
            <PencilIcon />
            重命名
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => void load().then((full) => full && onDuplicate(`${full.name} 副本`, full.definition))}>
            <CopyIcon />
            复制
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => void load().then((full) => full && download(full.name, flowFile(full.name, full.definition)))}>
            <DownloadIcon />
            导出 JSON
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onClick={() => setMode("confirm")}>
            <Trash2Icon />
            删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </li>
  );
}

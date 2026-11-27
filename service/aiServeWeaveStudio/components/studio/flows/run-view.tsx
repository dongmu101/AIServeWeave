"use client";

import * as React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Background, Controls, ReactFlow, ReactFlowProvider, type Edge } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { ArrowLeftIcon, CopyIcon, HistoryIcon, PencilIcon, RotateCcwIcon, SquareIcon } from "lucide-react";
import { toast } from "sonner";
import { cn } from "cn";

import { failureText, studioCall } from "@/components/studio/flows/api";
import { ARIA_LABELS, FLOW_NODE_TYPES, NODE_KIND, type FlowCanvasNode } from "@/components/studio/flows/flow-node";
import { ArtifactValue } from "@/components/studio/flows/artifact-view";
import { RunForm } from "@/components/studio/flows/run-form";
import { useHydrated } from "@/components/studio/flows/use-hydrated";
import { sendToSignIn } from "@/components/studio/session-expired";
import { Button } from "@/components/ui/button";
import { artifactFieldShape, artifactOutputs } from "@/lib/studio/artifacts";
import { toCanvasEdges } from "@/lib/studio/flow-graph";
import { applyFrame, duration, initialRunState, type RunState } from "@/lib/studio/flow-run";
import { issueText, parseRun, runErrorText, statusText, type FlowDefinition, type Run, type RunStep } from "@/lib/studio/flows";
import { SSEParser } from "@/lib/studio/sse";

/**
 * RunView shows one run on the canvas of the version it ran: each node's
 * state, a selected node's inputs, outputs and usage, the text of a model
 * call as it arrives, and the result. While the run is going it follows the
 * event stream; a stream that ends early is reopened, and each reopening
 * starts from a fresh snapshot.
 *
 * RunView 在其所运行版本的画布上展示一次运行：每个节点的状态、选中节点的输入、输出与用量、
 * 模型调用到达中的文本，以及运行结果。运行进行时跟随事件流；提前结束的流会重新打开，每次
 * 重新打开都从新的快照开始。
 */
export function RunView(props: { flowId: string; flowName: string; run: Run; definition: FlowDefinition }) {
  return (
    <ReactFlowProvider>
      <View {...props} />
    </ReactFlowProvider>
  );
}

const STATUS_TONE: Record<string, string> = {
  running: "bg-sky-500/10 text-sky-700 dark:text-sky-300",
  succeeded: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
  failed: "bg-destructive/10 text-destructive",
  canceled: "bg-muted text-muted-foreground",
  interrupted: "bg-amber-500/10 text-amber-700 dark:text-amber-300",
  skipped: "bg-muted text-muted-foreground",
};

export function StatusBadge({ status }: { status: string }) {
  return <span className={cn("inline-flex h-6 items-center rounded-full px-2 text-xs font-medium", STATUS_TONE[status])}>{statusText(status)}</span>;
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

function useNow(active: boolean): number {
  const [now, setNow] = React.useState(() => Date.now());
  React.useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [active]);
  return now;
}

function View({ flowId, flowName, run, definition }: { flowId: string; flowName: string; run: Run; definition: FlowDefinition }) {
  const router = useRouter();
  const hydrated = useHydrated();
  const [state, setState] = React.useState<RunState>(() => initialRunState(run));
  const [selected, setSelected] = React.useState<string | null>(run.failedNode || null);
  const [rerun, setRerun] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [rerunErrors, setRerunErrors] = React.useState<Record<string, string>>({});
  const now = useNow(!state.finished);

  React.useEffect(() => {
    if (run.status !== "running") return;
    const controller = new AbortController();
    const signal = controller.signal;
    (async () => {
      let current = initialRunState(run);
      let wait = 1000;
      while (!signal.aborted && !current.finished) {
        try {
          const response = await fetch(`/api/studio/runs/${run.id}/events`, { signal, cache: "no-store" });
          if (response.status === 401) {
            sendToSignIn(window.location.pathname);
            return;
          }
          if (response.status === 404) return;
          if (response.ok && response.body) {
            const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
            const parser = new SSEParser();
            for (;;) {
              const { done, value } = await reader.read();
              if (done) break;
              for (const frame of parser.push(value)) {
                current = applyFrame(current, frame);
                wait = 1000;
              }
              setState(current);
            }
          }
        } catch {
          if (signal.aborted) return;
        }
        if (current.finished) break;
        await sleep(wait, signal);
        wait = Math.min(wait * 2, 5000);
      }
      if (current.finished && !signal.aborted) router.refresh();
    })();
    return () => controller.abort();
  }, [run, router]);

  const nodes: FlowCanvasNode[] = React.useMemo(
    () =>
      definition.nodes.map((node) => ({
        id: node.id,
        type: "flow" as const,
        position: node.position,
        selected: node.id === selected,
        draggable: false,
        connectable: false,
        deletable: false,
        data: { node, definition, issues: 0, warning: "", state: state.nodes[node.id] },
      })),
    [definition, selected, state.nodes]
  );
  const edges: Edge[] = React.useMemo(
    () =>
      toCanvasEdges(definition).map((e) => ({
        id: e.id,
        source: e.source,
        target: e.target,
        sourceHandle: e.sourceHandle,
        label: e.label,
        animated: state.nodes[e.target] === "running",
        style: state.nodes[e.target] === "skipped" || state.nodes[e.source] === "skipped" ? { opacity: 0.35 } : undefined,
      })),
    [definition, state.nodes]
  );

  async function cancel() {
    setBusy(true);
    const result = await studioCall("POST", `runs/${run.id}/cancel`);
    setBusy(false);
    if (!result.ok) toast.error(failureText(result.code));
    else toast.success("已请求停止。");
  }

  async function startRerun(inputs: Record<string, string | number | boolean>) {
    setBusy(true);
    const result = await studioCall("POST", `flows/${flowId}/runs`, { inputs }, parseRun);
    setBusy(false);
    if (result.ok) {
      router.push(`/flows/${flowId}/runs/${result.data.id}`);
      return;
    }
    if (result.code === "invalid_inputs") {
      const errors: Record<string, string> = {};
      for (const issue of result.issues) errors[/^inputs\.([A-Za-z_][A-Za-z0-9_]*)$/.exec(issue.field)?.[1] ?? ""] = issueText({ ...issue, field: "" });
      setRerunErrors(errors);
      return;
    }
    setRerunErrors({ "": failureText(result.code) });
  }

  const current = state.run;
  const end = current.finishedAt || (state.finished ? "" : new Date(now).toISOString());
  const step = selected ? current.steps.find((s) => s.nodeId === selected) : undefined;
  const selectedNode = selected ? definition.nodes.find((n) => n.id === selected) : undefined;
  const label = (id: string) => definition.nodes.find((n) => n.id === id)?.label || id;
  const artifacts = React.useMemo(() => artifactOutputs(definition), [definition]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b px-3">
        <Link href={`/flows/${flowId}/runs`} aria-label="返回运行记录" className="inline-flex size-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-muted hover:text-foreground">
          <ArrowLeftIcon className="size-4" />
        </Link>
        <div className="min-w-0 flex-1">
          <p className="truncate font-heading text-[15px] font-medium">{flowName}</p>
          <p className="truncate text-xs text-muted-foreground">第 {current.flowVersion} 版的一次运行</p>
        </div>
        <StatusBadge status={current.status} />
        {!state.finished ? (
          <Button variant="destructive" disabled={busy} onClick={() => void cancel()}>
            <SquareIcon />
            停止
          </Button>
        ) : (
          <Button variant="outline" disabled={busy} onClick={() => { setRerunErrors({}); setRerun(true); }}>
            <RotateCcwIcon />
            重跑
          </Button>
        )}
        <Link href={`/flows/${flowId}`} className="inline-flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-sm text-muted-foreground hover:bg-muted hover:text-foreground">
          <PencilIcon className="size-4" />
          <span className="max-sm:hidden">编辑</span>
        </Link>
        <Link href={`/flows/${flowId}/runs`} aria-label="运行记录" className="inline-flex size-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-muted hover:text-foreground sm:hidden">
          <HistoryIcon className="size-4" />
        </Link>
      </header>

      <div className="flex min-h-0 flex-1 max-md:flex-col">
        <div className="relative min-h-[300px] min-w-0 flex-1">
          {hydrated ? (
            <ReactFlow<FlowCanvasNode>
              nodes={nodes}
              edges={edges}
              nodeTypes={FLOW_NODE_TYPES}
              nodesDraggable={false}
              nodesConnectable={false}
              elementsSelectable
              deleteKeyCode={null}
              onNodeClick={(_, node) => setSelected(node.id)}
              onPaneClick={() => setSelected(null)}
              colorMode="system"
              ariaLabelConfig={ARIA_LABELS}
              fitView
              fitViewOptions={{ maxZoom: 1, padding: 0.2 }}
              minZoom={0.2}
              proOptions={{ hideAttribution: true }}
            >
              <Background gap={20} />
              <Controls showInteractive={false} />
            </ReactFlow>
          ) : null}
        </div>

        <aside aria-label="运行详情" className="shrink-0 overflow-y-auto border-t max-md:max-h-[50%] md:w-96 md:border-t-0 md:border-l">
          <div className="grid gap-4 p-4 text-sm">
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
              <dt className="text-muted-foreground">开始</dt>
              <dd>{current.createdAt ? new Date(current.createdAt).toLocaleString("zh-CN") : "—"}</dd>
              <dt className="text-muted-foreground">耗时</dt>
              <dd>{duration(current.createdAt, end) || "—"}</dd>
              <dt className="text-muted-foreground">用量</dt>
              <dd>
                输入 {current.usage.promptTokens} · 输出 {current.usage.completionTokens} tokens
              </dd>
            </dl>

            {current.status === "failed" ? (
              <p className="rounded-lg bg-destructive/10 px-3 py-2 text-xs text-destructive">
                {current.failedNode ? (
                  <button type="button" className="font-medium hover:underline" onClick={() => setSelected(current.failedNode)}>
                    {label(current.failedNode)}：
                  </button>
                ) : null}
                {runErrorText(current.errorCode)}
              </p>
            ) : null}
            {current.status === "interrupted" ? (
              <p className="rounded-lg bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">运行所在的服务器停止或重启了，这次运行没有完成。可以重跑。</p>
            ) : null}

            {state.finished && Object.keys(current.outputs).length > 0 ? (
              <section className="grid gap-2">
                <p className="font-medium">结果</p>
                {Object.entries(current.outputs).map(([key, value]) =>
                  artifacts[key] ? (
                    <ArtifactValue key={key} runId={run.id} label={key} value={value} shape={artifacts[key]} />
                  ) : (
                    <ValueBlock key={key} label={key} value={value} copyable />
                  )
                )}
              </section>
            ) : null}

            {selectedNode ? (
              <NodeDetail runId={run.id} nodeType={selectedNode.type} label={label(selectedNode.id)} kind={NODE_KIND[selectedNode.type].label} step={step} live={state.live[selectedNode.id]}
                started={state.started[selectedNode.id]} running={state.nodes[selectedNode.id] === "running"} now={now} />
            ) : (
              <p className="text-xs text-muted-foreground">点选画布上的节点，查看它的输入、输出与用量。</p>
            )}
          </div>
        </aside>
      </div>

      {rerun ? (
        <RunForm inputs={definition.inputs} previous={current.inputs} busy={busy} serverErrors={rerunErrors}
          onSubmit={(inputs) => void startRerun(inputs)} onClose={() => setRerun(false)} />
      ) : null}
    </div>
  );
}

function NodeDetail({ runId, nodeType, label, kind, step, live, started, running, now }: {
  runId: string;
  nodeType: string;
  label: string;
  kind: string;
  step?: RunStep;
  live?: string;
  started?: string;
  running: boolean;
  now: number;
}) {
  return (
    <section className="grid gap-3 border-t pt-4">
      <div className="flex items-center justify-between gap-2">
        <p className="font-medium">
          {label} <span className="font-normal text-muted-foreground">· {kind}</span>
        </p>
        {step ? <StatusBadge status={step.status} /> : running ? <StatusBadge status="running" /> : <span className="text-xs text-muted-foreground">尚未运行</span>}
      </div>
      {step ? (
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          {step.model ? (
            <>
              <dt className="text-muted-foreground">{nodeType === "comfyui_template" ? "模板" : "模型"}</dt>
              <dd className="font-mono">{step.model}</dd>
            </>
          ) : null}
          <dt className="text-muted-foreground">耗时</dt>
          <dd>{duration(step.startedAt, step.finishedAt) || "—"}</dd>
          {step.usage.promptTokens || step.usage.completionTokens ? (
            <>
              <dt className="text-muted-foreground">用量</dt>
              <dd>
                输入 {step.usage.promptTokens} · 输出 {step.usage.completionTokens} tokens
              </dd>
            </>
          ) : null}
        </dl>
      ) : running && started ? (
        <p className="text-xs text-muted-foreground">已运行 {duration(started, new Date(now).toISOString()) || "片刻"}</p>
      ) : null}
      {step?.errorCode ? <p className="rounded-lg bg-destructive/10 px-3 py-2 text-xs text-destructive">{runErrorText(step.errorCode)}</p> : null}
      {running && (nodeType === "image_generation" || nodeType === "comfyui_template") ? (
        <p className="rounded-lg bg-muted px-3 py-2 text-xs text-muted-foreground">正在生成，完成后会在这里显示结果。</p>
      ) : running ? (
        <ValueBlock label="正在生成" value={live ?? ""} placeholder="等待模型开始输出…" />
      ) : null}
      {step && Object.keys(step.inputs).length > 0 ? (
        <div className="grid gap-2">
          <p className="text-xs font-medium text-muted-foreground">输入</p>
          {Object.entries(step.inputs).map(([key, value]) => (
            <ValueBlock key={key} label={INPUT_LABEL[key] ?? key} value={value} />
          ))}
        </div>
      ) : null}
      {step && Object.keys(step.outputs).length > 0 ? (
        <div className="grid gap-2">
          <p className="text-xs font-medium text-muted-foreground">输出</p>
          {Object.entries(step.outputs).map(([key, value]) => {
            const shape = artifactFieldShape(nodeType, key);
            return shape ? <ArtifactValue key={key} runId={runId} label={key} value={value} shape={shape} /> : <ValueBlock key={key} label={key} value={value} copyable />;
          })}
        </div>
      ) : null}
    </section>
  );
}

const INPUT_LABEL: Record<string, string> = { system: "系统提示词", prompt: "提示词", left: "比较的值", right: "与之比较", size: "尺寸" };

function ValueBlock({ label, value, copyable, placeholder }: { label: string; value: string; copyable?: boolean; placeholder?: string }) {
  return (
    <div className="grid gap-1">
      <div className="flex items-center justify-between">
        <span className="font-mono text-[11px] text-muted-foreground">{label}</span>
        {copyable && value ? (
          <button type="button" aria-label={`复制 ${label}`} className="inline-flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground"
            onClick={() => void navigator.clipboard.writeText(value).then(() => toast.success("已复制"))}>
            <CopyIcon className="size-3.5" />
          </button>
        ) : null}
      </div>
      <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-muted px-3 py-2 font-sans text-xs leading-relaxed">
        {value || <span className="text-muted-foreground">{placeholder ?? "（空）"}</span>}
      </pre>
    </div>
  );
}

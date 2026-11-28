"use client";

import * as React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  Background,
  Controls,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Connection,
  type Edge,
  type EdgeChange,
  type NodeChange,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { ArrowLeftIcon, CopyIcon, DownloadIcon, EllipsisIcon, HistoryIcon, PlayIcon, SaveIcon, Trash2Icon, TriangleAlertIcon } from "lucide-react";
import { toast } from "sonner";
import { cn } from "cn";

import { failureText, startFlowRun, studioCall } from "@/components/studio/flows/api";
import { ARIA_LABELS, FLOW_NODE_TYPES, NODE_KIND, type FlowCanvasNode } from "@/components/studio/flows/flow-node";
import { NodeInspector } from "@/components/studio/flows/node-inspector";
import { RunForm } from "@/components/studio/flows/run-form";
import { useHydrated } from "@/components/studio/flows/use-hydrated";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import type { StudioModel } from "@/lib/studio/contract";
import {
  addNode,
  artifactReferenceIssues,
  catalogWarnings,
  connect,
  EDITABLE_TYPES,
  llmConfig,
  mergeIssues,
  moveNode,
  removeEdges,
  removeNodes,
  serialize,
  toCanvasEdges,
  unavailableModels,
} from "@/lib/studio/flow-graph";
import { parseImageModels, parseVideoModels, parseWorkflowTemplates, type FlowCatalog, type ImageModelCatalog, type VideoModelCatalog, type WorkflowTemplate } from "@/lib/studio/flow-catalog";
import { issueText, parseFlow, type Flow, type FlowDefinition, type FlowIssue, type NodeType } from "@/lib/studio/flows";
import { flowFile, serverInputErrors, type BuiltRun } from "@/lib/studio/run-inputs";

/**
 * FlowEditor is the canvas editor of one flow. The definition in memory is
 * the single source of truth; the canvas and the inspector are views of it.
 * Saving sends the whole definition with the version it was loaded at, so a
 * save made elsewhere in between is refused rather than overwritten.
 *
 * FlowEditor 是单个编排的画布编辑器。内存中的定义是唯一事实来源，画布与配置面板都是它的
 * 视图。保存时连同加载时的版本号一起发送整份定义，因此期间在别处的保存会使本次保存被拒绝，
 * 而不是被覆盖。
 */
export function FlowEditor(props: EditorProps) {
  return (
    <ReactFlowProvider>
      <Editor {...props} />
    </ReactFlowProvider>
  );
}

/** NODE_DRAG_TYPE carries a node type dragged from the palette to the canvas.
 * NODE_DRAG_TYPE 承载从节点栏拖到画布上的节点类型。 */
const NODE_DRAG_TYPE = "application/x-aiserveweave-flow-node";

interface EditorProps {
  flow: Flow;
  models: StudioModel[];
  modelsError: string;
  /** null when the list failed to load / 加载失败时为 null */
  imageModels: ImageModelCatalog | null;
  templates: WorkflowTemplate[] | null;
  videoModels: VideoModelCatalog | null;
  catalogError: string;
}

function fingerprint(name: string, def: FlowDefinition): string {
  return JSON.stringify([name.trim(), serialize(def)]);
}

function warningText(issue: FlowIssue | undefined): string {
  return issue ? issueText(issue) : "";
}

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = `${name.trim() || "flow"}.json`;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function Editor({ flow, models, modelsError, imageModels: initialImageModels, templates: initialTemplates, videoModels: initialVideoModels, catalogError }: EditorProps) {
  const router = useRouter();
  const reactFlow = useReactFlow();
  const hydrated = useHydrated();
  const canvas = React.useRef<HTMLDivElement>(null);

  const [def, setDef] = React.useState<FlowDefinition>(flow.definition);
  const [name, setName] = React.useState(flow.name);
  const [version, setVersion] = React.useState(flow.version);
  const [saved, setSaved] = React.useState(() => fingerprint(flow.name, flow.definition));
  const [selected, setSelected] = React.useState<string | null>(null);
  const [selectedEdges, setSelectedEdges] = React.useState<string[]>([]);
  const [issues, setIssues] = React.useState<FlowIssue[]>([]);
  const [busy, setBusy] = React.useState<"save" | "run" | null>(null);
  const [runOpen, setRunOpen] = React.useState(false);
  const [runErrors, setRunErrors] = React.useState<Record<string, string>>({});
  const [catalog, setCatalog] = React.useState<FlowCatalog>({ imageModels: initialImageModels, templates: initialTemplates, videoModels: initialVideoModels });
  const dirty = fingerprint(name, def) !== saved;

  const warningsFor = React.useCallback(
    (from: FlowCatalog) => [...(models.length > 0 ? unavailableModels(def, models.map((m) => m.id)) : []), ...catalogWarnings(def, from)],
    [def, models]
  );
  const warnings = React.useMemo(() => warningsFor(catalog), [warningsFor, catalog]);
  // Server issues plus the ones the editor can see itself, such as an artifact
  // referenced where only an output may.
  // 服务端的问题，加上编辑器自己能看出的，例如在只有输出才能引用产物的地方引用了产物。
  const shownIssues = React.useMemo(() => mergeIssues(def, issues, artifactReferenceIssues(def)), [def, issues]);
  const issueCount = React.useMemo(() => {
    const counts: Record<string, number> = {};
    for (const issue of shownIssues) counts[issue.node] = (counts[issue.node] ?? 0) + 1;
    return counts;
  }, [shownIssues]);

  // The canvas measures each node once it renders and needs those sizes
  // back; everything else about a node comes from the definition.
  // 画布在节点渲染后测量其尺寸，并需要拿回这些尺寸；节点的其他一切都来自定义。
  const [measured, setMeasured] = React.useState<Record<string, { width: number; height: number }>>({});
  const nodes: FlowCanvasNode[] = React.useMemo(
    () =>
      def.nodes.map((node) => ({
        id: node.id,
        type: "flow" as const,
        position: node.position,
        selected: node.id === selected,
        deletable: node.type !== "input" && node.type !== "output",
        measured: measured[node.id],
        data: { node, definition: def, issues: issueCount[node.id] ?? 0, warning: warningText(warnings.find((w) => w.node === node.id)) },
      })),
    [def, selected, issueCount, warnings, measured]
  );

  const edges: Edge[] = React.useMemo(
    () =>
      toCanvasEdges(def).map((e) => ({
        id: e.id,
        source: e.source,
        target: e.target,
        sourceHandle: e.sourceHandle,
        label: e.label,
        selected: selectedEdges.includes(e.id),
        style: e.label ? { stroke: e.sourceHandle === "true" ? "var(--color-emerald-500)" : "var(--color-rose-500)" } : undefined,
      })),
    [def, selectedEdges]
  );

  function onNodesChange(changes: NodeChange<FlowCanvasNode>[]) {
    const sizes = changes.flatMap((c) => (c.type === "dimensions" && c.dimensions ? [[c.id, c.dimensions] as const] : []));
    if (sizes.length > 0) setMeasured((prev) => ({ ...prev, ...Object.fromEntries(sizes) }));
    let next = def;
    const removed: string[] = [];
    for (const change of changes) {
      if (change.type === "position" && change.position) next = moveNode(next, change.id, change.position);
      if (change.type === "select") {
        if (change.selected) setSelected(change.id);
        else setSelected((current) => (current === change.id ? null : current));
      }
      if (change.type === "remove") removed.push(change.id);
    }
    if (removed.length > 0) {
      next = removeNodes(next, removed);
      setSelected((current) => (current && removed.includes(current) ? null : current));
    }
    if (next !== def) setDef(next);
  }

  function onEdgesChange(changes: EdgeChange[]) {
    const removed = changes.filter((c) => c.type === "remove").map((c) => c.id);
    if (removed.length > 0) setDef(removeEdges(def, removed));
    setSelectedEdges((current) => {
      let next = current.filter((id) => !removed.includes(id));
      for (const change of changes) {
        if (change.type !== "select") continue;
        next = change.selected ? [...next.filter((id) => id !== change.id), change.id] : next.filter((id) => id !== change.id);
      }
      return next;
    });
  }

  function onConnect(connection: Connection) {
    const result = connect(def, connection.source, connection.target, connection.sourceHandle ?? null);
    if (!result.ok) {
      toast.error(result.reason);
      return;
    }
    setDef(result.def);
  }

  // add places a node of type centred on the screen point at, or near the
  // canvas centre when clicked from the palette.
  // add 把 type 类型的节点居中放在屏幕坐标 at 处；从节点栏点击时放在画布中心附近。
  function add(type: NodeType, at?: { x: number; y: number }) {
    let point: { x: number; y: number };
    if (at) {
      point = reactFlow.screenToFlowPosition(at);
    } else {
      const box = canvas.current?.getBoundingClientRect();
      const center = box ? reactFlow.screenToFlowPosition({ x: box.left + box.width / 2, y: box.top + box.height / 2 }) : { x: 0, y: 0 };
      const offset = (def.nodes.length % 5) * 24;
      point = { x: center.x + offset, y: center.y + offset };
    }
    const model = [...def.nodes].reverse().map((n) => (n.type === "llm" ? llmConfig(n).model : "")).find((m) => m !== "") ?? models[0]?.id ?? "";
    const imageModel = catalog.imageModels && !catalog.imageModels.defaultAvailable ? (catalog.imageModels.models[0]?.id ?? "") : "";
    const videoModel = catalog.videoModels?.models[0]?.id ?? "";
    const added = addNode(def, type, { x: point.x - 112, y: point.y - 40 }, model, imageModel, videoModel);
    setDef(added.def);
    setSelected(added.id);
  }

  function draggedType(event: React.DragEvent): NodeType | null {
    const type = event.dataTransfer.getData(NODE_DRAG_TYPE);
    return (EDITABLE_TYPES as readonly string[]).includes(type) ? (type as NodeType) : null;
  }

  function onDragOver(event: React.DragEvent) {
    if (!event.dataTransfer.types.includes(NODE_DRAG_TYPE)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "copy";
  }

  function onDrop(event: React.DragEvent) {
    const type = draggedType(event);
    if (!type) return;
    event.preventDefault();
    add(type, { x: event.clientX, y: event.clientY });
  }

  async function save(): Promise<boolean> {
    if (name.trim() === "") {
      toast.error("请填写编排名称。");
      return false;
    }
    setBusy("save");
    const result = await studioCall("PATCH", `flows/${flow.id}`, { name: name.trim(), definition: serialize(def), base_version: version }, parseFlow);
    setBusy(null);
    if (!result.ok) {
      if (result.code === "invalid_flow" && result.issues.length > 0) {
        setIssues(result.issues);
        toast.error(`编排有 ${result.issues.length} 个问题，已在画布上标出。`);
      } else {
        toast.error(failureText(result.code));
      }
      return false;
    }
    setIssues([]);
    setSaved(fingerprint(name, def));
    toast.success(result.data.version !== version ? `已保存为第 ${result.data.version} 版` : "已保存");
    setVersion(result.data.version);
    return true;
  }

  /** refreshCatalog reads the catalogs again, so a template unpublished since
   * the page loaded is caught before a run; a list that cannot be read keeps
   * what the editor had.
   * refreshCatalog 重新读取目录，这样页面加载后被取消发布的模板能在运行前被发现；读不到的
   * 列表保留编辑器已有的内容。 */
  async function refreshCatalog(): Promise<FlowCatalog> {
    const [imageModels, templates, videoModels] = await Promise.all([
      studioCall("GET", "image-models", undefined, parseImageModels),
      studioCall("GET", "workflow-templates", undefined, parseWorkflowTemplates),
      studioCall("GET", "video-models", undefined, parseVideoModels),
    ]);
    const next: FlowCatalog = {
      imageModels: imageModels.ok ? imageModels.data : catalog.imageModels,
      templates: templates.ok ? templates.data : catalog.templates,
      videoModels: videoModels.ok ? videoModels.data : catalog.videoModels,
    };
    setCatalog(next);
    return next;
  }

  async function openRun() {
    if (dirty && !(await save())) return;
    const blocked = warningsFor(await refreshCatalog());
    if (blocked.length > 0) {
      toast.error("有节点暂时无法运行（模型或模板不可用、必填项未设置），请先处理。");
      setSelected(blocked[0].node);
      return;
    }
    setRunErrors({});
    setRunOpen(true);
  }

  async function startRun(built: BuiltRun) {
    setBusy("run");
    const result = await startFlowRun(flow.id, built);
    setBusy(null);
    if (result.ok) {
      router.push(`/flows/${flow.id}/runs/${result.data.id}`);
      return;
    }
    if (result.code === "invalid_inputs") {
      setRunErrors(serverInputErrors(result.issues));
      return;
    }
    if (
      (result.code === "model_unavailable" || result.code === "template_unavailable" || result.code === "image_generation_unavailable" || result.code === "video_generation_unavailable") &&
      result.issues.length > 0
    ) {
      setRunOpen(false);
      setIssues(result.issues);
      toast.error("有节点的模型或模板当前不可用，已在画布上标出。");
      return;
    }
    setRunErrors({ "": failureText(result.code) });
  }

  async function duplicate() {
    const result = await studioCall("POST", "flows", { name: `${name.trim() || flow.name} 副本`, definition: serialize(def) }, parseFlow);
    if (!result.ok) {
      toast.error(result.code === "invalid_flow" ? "当前编排有错误，请先修正再复制。" : failureText(result.code));
      return;
    }
    router.push(`/flows/${result.data.id}`);
  }

  async function remove() {
    if (!window.confirm(`删除编排「${flow.name}」？它的所有版本与运行记录也会一并删除。`)) return;
    const result = await studioCall("DELETE", `flows/${flow.id}`);
    if (!result.ok) {
      toast.error(failureText(result.code));
      return;
    }
    setSaved(fingerprint(name, def) + "deleted");
    router.push("/flows");
    router.refresh();
  }

  const saveRef = React.useRef(save);
  React.useEffect(() => {
    saveRef.current = save;
  });
  React.useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "s") {
        event.preventDefault();
        void saveRef.current();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  React.useEffect(() => {
    if (!dirty) return;
    function onLeave(event: BeforeUnloadEvent) {
      event.preventDefault();
    }
    window.addEventListener("beforeunload", onLeave);
    return () => window.removeEventListener("beforeunload", onLeave);
  }, [dirty]);

  const selectedIssues = selected ? shownIssues.filter((issue) => issue.node === selected) : [];
  const selectedWarnings = selected ? warnings.filter((issue) => issue.node === selected) : [];
  const flowIssues = shownIssues.filter((issue) => !issue.node || !def.nodes.some((n) => n.id === issue.node));

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b px-3">
        <Link href="/flows" aria-label="返回编排列表" className="inline-flex size-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-muted hover:text-foreground">
          <ArrowLeftIcon className="size-4" />
        </Link>
        <input value={name} maxLength={200} aria-label="编排名称" onChange={(event) => setName(event.target.value)}
          className="h-8 min-w-0 max-w-xs flex-1 rounded-md bg-transparent px-2 font-heading text-[15px] font-medium outline-none hover:bg-muted focus-visible:bg-muted" />
        <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">第 {version} 版</span>
        {dirty ? <span className="shrink-0 text-xs text-amber-600 max-sm:hidden">未保存</span> : null}
        <div className="flex-1" />
        <Link href={`/flows/${flow.id}/runs`} className="inline-flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-sm text-muted-foreground hover:bg-muted hover:text-foreground">
          <HistoryIcon className="size-4" />
          <span className="max-sm:hidden">运行记录</span>
        </Link>
        <Button variant="outline" disabled={busy !== null} onClick={() => void save()} title="保存（⌘S / Ctrl+S）">
          <SaveIcon />
          <span className="max-sm:hidden">{busy === "save" ? "保存中…" : "保存"}</span>
        </Button>
        <Button disabled={busy !== null} onClick={() => void openRun()}>
          <PlayIcon />
          运行
        </Button>
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="ghost" size="icon" aria-label="更多操作" />}>
            <EllipsisIcon />
          </DropdownMenuTrigger>
          <DropdownMenuContent side="bottom" align="end" className="w-40">
            <DropdownMenuItem onClick={() => download(name, flowFile(name.trim() || flow.name, serialize(def)))}>
              <DownloadIcon />
              导出 JSON
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => void duplicate()}>
              <CopyIcon />
              复制编排
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onClick={() => void remove()}>
              <Trash2Icon />
              删除编排
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </header>

      <div className="flex min-h-0 flex-1 max-md:flex-col">
        <nav aria-label="添加节点" className="flex shrink-0 gap-1 overflow-auto border-b p-2 md:w-28 md:flex-col md:border-r md:border-b-0">
          {EDITABLE_TYPES.map((type) => (
            <button key={type} type="button" onClick={() => add(type)} title={`添加${NODE_KIND[type].label}节点（点击或拖到画布）`} aria-label={`添加${NODE_KIND[type].label}节点`}
              draggable
              onDragStart={(event) => {
                event.dataTransfer.setData(NODE_DRAG_TYPE, type);
                event.dataTransfer.effectAllowed = "copy";
              }}
              className="flex cursor-grab items-center gap-2 whitespace-nowrap rounded-lg px-2 py-1.5 text-xs hover:bg-muted active:cursor-grabbing md:flex-col md:gap-1 md:py-2">
              <span className={cn("inline-flex size-7 items-center justify-center rounded-md [&_svg]:size-4", NODE_KIND[type].tint)}>{NODE_KIND[type].icon}</span>
              {NODE_KIND[type].label}
            </button>
          ))}
        </nav>

        <aside aria-label="配置" className="shrink-0 overflow-y-auto border-t max-md:order-last max-md:max-h-[45%] md:w-80 md:border-t-0 md:border-r">
          {selected ? (
            <NodeInspector
              def={def}
              nodeId={selected}
              models={models}
              catalog={catalog}
              issues={selectedIssues}
              warnings={selectedWarnings}
              onChange={setDef}
              onDelete={(id) => {
                setDef(removeNodes(def, [id]));
                setSelected(null);
              }}
            />
          ) : (
            <Overview def={def} issues={shownIssues} flowIssues={flowIssues} warnings={warnings} modelsError={modelsError} catalogError={catalogError} onSelect={setSelected} />
          )}
        </aside>

        <div ref={canvas} onDragOver={onDragOver} onDrop={onDrop} className="relative min-h-[320px] min-w-0 flex-1">
          {hydrated ? (
            <ReactFlow<FlowCanvasNode>
              nodes={nodes}
              edges={edges}
              nodeTypes={FLOW_NODE_TYPES}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              onConnect={onConnect}
              onPaneClick={() => setSelected(null)}
              deleteKeyCode={["Backspace", "Delete"]}
              multiSelectionKeyCode={null}
              colorMode="system"
              ariaLabelConfig={ARIA_LABELS}
              fitView
              fitViewOptions={{ maxZoom: 1, padding: 0.2 }}
              minZoom={0.2}
              proOptions={{ hideAttribution: true }}
            >
              <Background gap={20} />
              <Controls showInteractive={false} />
              <MiniMap pannable zoomable className="max-md:!hidden" />
            </ReactFlow>
          ) : null}
        </div>
      </div>

      {runOpen ? (
        <RunForm inputs={def.inputs} busy={busy === "run"} serverErrors={runErrors} onSubmit={(built) => void startRun(built)} onClose={() => setRunOpen(false)} />
      ) : null}
    </div>
  );
}

function Overview({
  def,
  issues,
  flowIssues,
  warnings,
  modelsError,
  catalogError,
  onSelect,
}: {
  def: FlowDefinition;
  issues: FlowIssue[];
  flowIssues: FlowIssue[];
  warnings: FlowIssue[];
  modelsError: string;
  catalogError: string;
  onSelect: (id: string) => void;
}) {
  const label = (id: string) => def.nodes.find((n) => n.id === id)?.label || id;
  return (
    <div className="grid gap-4 p-4 text-sm">
      <div>
        <p className="font-medium">编排概览</p>
        <p className="mt-1 text-muted-foreground">
          {def.nodes.length} 个节点，{def.edges.length} 条连线，{def.inputs.length} 个输入，{def.outputs.length} 个结果字段。
        </p>
      </div>
      {modelsError ? <p className="text-xs text-amber-600">模型列表加载失败：{modelsError}</p> : null}
      {catalogError ? <p className="text-xs text-amber-600">{catalogError}。</p> : null}
      {issues.length > 0 ? (
        <div className="grid gap-1.5">
          <p className="font-medium text-destructive">发现 {issues.length} 个问题</p>
          <ul className="grid gap-1">
            {flowIssues.map((issue, i) => (
              <li key={`f${i}`} className="text-xs text-destructive">{issueText(issue)}</li>
            ))}
            {issues.filter((issue) => !flowIssues.includes(issue)).map((issue, i) => (
              <li key={i}>
                <button type="button" onClick={() => onSelect(issue.node)} className="text-left text-xs text-destructive hover:underline">
                  {label(issue.node)}：{issueText(issue)}
                </button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {warnings.length > 0 ? (
        <div className="grid gap-1">
          {warnings.map((warning) => (
            <button key={warning.node} type="button" onClick={() => onSelect(warning.node)} className="flex items-center gap-1.5 text-left text-xs text-amber-600 hover:underline">
              <TriangleAlertIcon className="size-3.5 shrink-0" />
              {label(warning.node)}：{issueText(warning)}
            </button>
          ))}
        </div>
      ) : null}
      <div className="grid gap-1.5 text-xs text-muted-foreground">
        <p>从左侧添加节点，从节点右侧的圆点拖到另一个节点即可连线。</p>
        <p>选中节点后在这里编辑；选中节点或连线后按 Delete 删除。</p>
        <p>在提示词里点「插入变量」引用输入或上游节点的结果。</p>
        <p>图像生成与 ComfyUI 模板产出的图片或文件，只能在输出节点的结果字段里整个引用。</p>
        <p>按 ⌘S / Ctrl+S 保存，每次保存都会生成新版本。</p>
      </div>
    </div>
  );
}

"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import * as React from "react";
import {
  Background,
  Controls,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { cn } from "cn";
import {
  buildGraphPreview,
  NODE_HEADER_HEIGHT,
  NODE_PADDING,
  NODE_ROW_HEIGHT,
  NODE_WIDTH,
  type GraphBindings,
  type GraphNode,
  type ParsedGraphText,
} from "@/lib/console/comfy-graph";

type ComfyFlowNode = Node<{ node: GraphNode }, "comfy">;

const MAX_ISSUES_SHOWN = 20;

/** ComfyNodeCard draws one ComfyUI node: consumed output slots on the right, input fields on the left, template bindings highlighted.
 * ComfyNodeCard 绘制一个 ComfyUI 节点：右侧是被消费的输出槽，左侧是输入字段，模板绑定高亮显示。 */
function ComfyNodeCard({ data, selected }: NodeProps<ComfyFlowNode>) {
  const { t } = useI18n();
  const { node } = data;
  return <div
    style={{ width: NODE_WIDTH, paddingBlock: NODE_PADDING }}
    className={cn(
      "rounded-lg border bg-card text-card-foreground shadow-sm",
      node.outputs.length > 0 && "border-emerald-500",
      node.diff === "changed" && "border-amber-500 ring-2 ring-amber-500/30",
      node.diff === "only-here" && "border-sky-500 ring-2 ring-sky-500/30",
      selected && "ring-2 ring-ring",
    )}
  >
    <div style={{ height: NODE_HEADER_HEIGHT }} className="flex flex-col justify-center border-b px-2">
      <div className="flex items-center gap-1 text-sm font-semibold">
        <span className="truncate">{node.title ?? node.classType}</span>
        {node.diff === "changed" && <span className="shrink-0 rounded bg-amber-500/15 px-1 text-[10px] font-normal text-amber-700"><Message id="common.different" /></span>}
        {node.diff === "only-here" && <span className="shrink-0 rounded bg-sky-500/15 px-1 text-[10px] font-normal text-sky-700"><Message id="common.thisSideOnly" /></span>}
        {node.outputs.length > 0 && <span className="shrink-0 truncate rounded bg-emerald-500/15 px-1 text-[10px] font-normal text-emerald-700"><Message id="common.output_32484b" values={{ value: node.outputs.join(", ") }} /></span>}
      </div>
      <div className="truncate text-[11px] text-muted-foreground">#{node.id}{node.title ? ` · ${node.classType}` : ""}</div>
    </div>
    {node.outputSlots.map((slot) => <div key={`out-${slot}`} style={{ height: NODE_ROW_HEIGHT }} className="relative flex items-center justify-end px-2 text-[11px] text-muted-foreground"><Message id="common.outputSlot_36cf1a" values={{ slot: slot, content: <Handle type="source" position={Position.Right} id={`out-${slot}`} isConnectable={false} /> }} /></div>)}
    {node.fields.map((field) => <div
      key={field.name}
      style={{ height: NODE_ROW_HEIGHT }}
      title={field.bound.length > 0 ? t("common.templateInputWritesToThisField", { value: field.bound.join(", ") }) : undefined}
      className={cn("relative flex items-center gap-2 px-2 text-[11px]", field.bound.length > 0 && "bg-primary/10 font-medium")}
    >
      {field.kind === "link" && !field.dangling && <Handle type="target" position={Position.Left} id={`in-${field.name}`} isConnectable={false} />}
      <span className="shrink-0 text-muted-foreground">{field.name}</span>
      <span className={cn("min-w-0 flex-1 truncate text-right font-mono", field.dangling && "text-destructive")}>{field.text}</span>
      {field.bound.length > 0 && <span className="shrink-0 truncate rounded bg-primary px-1 text-[10px] text-primary-foreground">{field.bound.join(", ")}</span>}
    </div>)}
  </div>;
}

const nodeTypes = { comfy: ComfyNodeCard };

/** FitOnShapeChange refits the viewport whenever the set of node ids changes, but not on every edit of a value, so typing in the draft does not keep yanking the view.
 * FitOnShapeChange 只在节点 id 集合变化时重新适配视口，而不是每改一个取值都适配，避免编辑草稿时视图不停跳动。 */
function FitOnShapeChange({ shape }: { shape: string }) {
  const flow = useReactFlow();
  React.useEffect(() => {
    const frame = requestAnimationFrame(() => { void flow.fitView({ padding: 0.1 }); });
    return () => cancelAnimationFrame(frame);
  }, [flow, shape]);
  return null;
}

/** ComfyGraphPreviewProps configures one read-only graph canvas. / ComfyGraphPreviewProps 配置一块只读的图画布。 */
export interface ComfyGraphPreviewProps {
  graph: ParsedGraphText;
  bindings: GraphBindings;
  /** Graph to diff against; nodes that differ are marked. / 用于比较的图；有差异的节点会被标出。 */
  compareTo?: unknown;
  height?: number;
}

/** ComfyGraphPreview renders an API-format ComfyUI graph as a pannable, zoomable node canvas with template bindings overlaid. It is a viewer only: the textarea stays the single place the graph is edited, and the layout is reconstructed because API format stores no positions.
 *
 * The graph shown here is operator-only material: this component must not be used on tenant pages.
 *
 * ComfyGraphPreview 把 API Format 的 ComfyUI 图渲染为可平移、可缩放的节点画布，并叠加模板绑定。它只是查看器：图的唯一编辑入口仍是文本框；由于 API Format 不保存坐标，布局是重建出来的。
 *
 * 这里展示的图只属于运维视角：本组件不得用在租户页面上。 */
export function ComfyGraphPreview({ graph, bindings, compareTo, height = 520 }: ComfyGraphPreviewProps) {
  const { locale, t } = useI18n();
  const preview = React.useMemo(
    () => graph.ok ? buildGraphPreview(graph.graph, bindings, compareTo, locale) : { ok: false as const, reason: "invalid_json" as const },
    [graph, bindings, compareTo, locale],
  );
  const [selectedID, setSelectedID] = React.useState<string | null>(null);

  const flow = React.useMemo(() => {
    if (!preview.ok) return null;
    const nodes: ComfyFlowNode[] = preview.nodes.map((node) => ({ id: node.id, type: "comfy", position: { x: node.x, y: node.y }, data: { node }, width: NODE_WIDTH, height: node.height }));
    const edges: Edge[] = preview.edges.map((edge) => ({ id: edge.id, source: edge.source, sourceHandle: `out-${edge.sourceSlot}`, target: edge.target, targetHandle: `in-${edge.targetField}` }));
    return { nodes, edges, shape: preview.nodes.map((n) => n.id).join(",") };
  }, [preview]);

  if (!preview.ok) {
    const message = {
      invalid_json: t("common.theGraphJsonCannotBeParsedYetA"),
      not_object: t("common.theGraphMustBeAnObjectKeyedBy"),
      empty: t("common.theGraphIsEmptyNoNodesToPreview"),
      too_many_nodes: t("common.theGraphHasANodeCountOfExceeding", { value: preview.count ?? t("common.tooMany") }),
    }[preview.reason];
    return <div style={{ height: 120 }} className="flex items-center justify-center rounded-lg border border-dashed text-sm text-muted-foreground">{message}</div>;
  }

  const selected = preview.nodes.find((n) => n.id === selectedID);
  return <div className="space-y-2">
    <div style={{ height }} className="overflow-hidden rounded-lg border bg-muted/20">
      <ReactFlowProvider>
        <ReactFlow
          nodes={flow!.nodes}
          edges={flow!.edges}
          nodeTypes={nodeTypes}
          nodesDraggable={false}
          nodesConnectable={false}
          edgesFocusable={false}
          onlyRenderVisibleElements
          minZoom={0.1}
          fitView
          colorMode="light"
          ariaLabelConfig={{
            "node.a11yDescription.default": t("flow.nodeHelp"),
            "node.a11yDescription.keyboardDisabled": t("flow.nodeHelp"),
            "node.a11yDescription.ariaLiveMessage": () => t("flow.nodeHelp"),
            "edge.a11yDescription.default": t("flow.edgeHelp"),
            "controls.ariaLabel": t("flow.controls"),
            "controls.zoomIn.ariaLabel": t("flow.zoomIn"),
            "controls.zoomOut.ariaLabel": t("flow.zoomOut"),
            "controls.fitView.ariaLabel": t("flow.fitView"),
            "controls.interactive.ariaLabel": t("flow.toggleInteractive"),
            "minimap.ariaLabel": t("flow.minimap"),
            "handle.ariaLabel": t("flow.handle"),
          }}
          onNodeClick={(_, node) => setSelectedID(node.id)}
          onPaneClick={() => setSelectedID(null)}
        >
          <FitOnShapeChange shape={flow!.shape} />
          <Background />
          <Controls showInteractive={false} />
          <MiniMap pannable zoomable />
        </ReactFlow>
      </ReactFlowProvider>
    </div>
    <p className="text-xs text-muted-foreground"><Message id="common.layoutIsRebuiltAutomaticallyFromConnectionsApiFormat" /></p>
    {preview.missingHere.length > 0 && <p className="text-xs text-sky-700"><Message id="common.nodesPresentInTheComparisonButMissingOn_3f3379" values={{ value: preview.missingHere.join(", ") }} /></p>}
    {preview.issues.length > 0 && <ul className="list-disc space-y-0.5 pl-5 text-xs text-destructive">
      {preview.issues.slice(0, MAX_ISSUES_SHOWN).map((issue, i) => <li key={i}>{issue}</li>)}
      {preview.issues.length > MAX_ISSUES_SHOWN && <li><Message id="common.additionalIssuesNotShown" values={{ value: preview.issues.length - MAX_ISSUES_SHOWN }} /></li>}
    </ul>}
    {selected && <div className="space-y-1 rounded-lg border p-3 text-xs">
      <p className="font-semibold">#{selected.id} · {selected.classType}{selected.title ? ` · ${selected.title}` : ""}</p>
      {selected.fields.length === 0 && <p className="text-muted-foreground"><Message id="common.thisNodeHasNoInputFields" /></p>}
      <dl className="grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1">
        {selected.fields.map((field) => <React.Fragment key={field.name}>
          <dt className="text-muted-foreground">{field.name}{field.bound.length > 0 ? t("common.input", { value: field.bound.join(", ") }) : ""}</dt>
          <dd className="max-h-40 overflow-auto whitespace-pre-wrap break-all font-mono">{field.full}</dd>
        </React.Fragment>)}
      </dl>
    </div>}
  </div>;
}

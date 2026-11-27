"use client";

import * as React from "react";
import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import {
  CheckIcon,
  CircleDashedIcon,
  CircleSlashIcon,
  FlagIcon,
  GitBranchIcon,
  ImageIcon,
  LoaderCircleIcon,
  LogInIcon,
  SparklesIcon,
  TriangleAlertIcon,
  TypeIcon,
  WorkflowIcon,
  XIcon,
} from "lucide-react";
import { cn } from "cn";

import { comfyConfig, conditionConfig, imageConfig, llmConfig, OPERATORS, templateConfig } from "@/lib/studio/flow-graph";
import type { NodeState } from "@/lib/studio/flow-run";
import type { FlowDefinition, FlowNode as Definition, NodeType } from "@/lib/studio/flows";

/**
 * FlowNodeCard is how a node looks on the canvas, in the editor and in the
 * run view alike: its kind, its name, a one-line summary, and — when there is
 * one — a validation problem, an unavailable model, or how it ran.
 *
 * FlowNodeCard 是节点在画布上的样子，编辑器与运行视图共用：类型、名称、一行摘要，以及
 * （如有）校验问题、不可用的模型或运行情况。
 */

export type FlowCanvasNodeData = {
  node: Definition;
  definition: FlowDefinition;
  issues: number;
  /** why the node cannot run now (an unavailable model or template…), or "" */
  warning: string;
  state?: NodeState;
};

export type FlowCanvasNode = Node<FlowCanvasNodeData, "flow">;

export const NODE_KIND: Record<NodeType, { label: string; icon: React.ReactElement; tint: string }> = {
  input: { label: "输入", icon: <LogInIcon />, tint: "bg-sky-500/10 text-sky-700 dark:text-sky-300" },
  llm: { label: "LLM", icon: <SparklesIcon />, tint: "bg-violet-500/10 text-violet-700 dark:text-violet-300" },
  template: { label: "文本模板", icon: <TypeIcon />, tint: "bg-amber-500/10 text-amber-700 dark:text-amber-300" },
  condition: { label: "条件", icon: <GitBranchIcon />, tint: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300" },
  image_generation: { label: "图像生成", icon: <ImageIcon />, tint: "bg-pink-500/10 text-pink-700 dark:text-pink-300" },
  comfyui_template: { label: "ComfyUI 模板", icon: <WorkflowIcon />, tint: "bg-cyan-500/10 text-cyan-700 dark:text-cyan-300" },
  output: { label: "输出", icon: <FlagIcon />, tint: "bg-rose-500/10 text-rose-700 dark:text-rose-300" },
};

const STATE_BADGE: Record<NodeState, { label: string; icon: React.ReactElement; className: string }> = {
  running: { label: "运行中", icon: <LoaderCircleIcon className="animate-spin" />, className: "text-sky-600 dark:text-sky-400" },
  succeeded: { label: "成功", icon: <CheckIcon />, className: "text-emerald-600 dark:text-emerald-400" },
  failed: { label: "失败", icon: <XIcon />, className: "text-destructive" },
  canceled: { label: "已取消", icon: <CircleSlashIcon />, className: "text-muted-foreground" },
  skipped: { label: "已跳过", icon: <CircleDashedIcon />, className: "text-muted-foreground" },
};

/** nodeSummary is the one line a card shows under the name.
 * nodeSummary 是卡片在名称下方显示的一行摘要。 */
export function nodeSummary(node: Definition, definition: FlowDefinition): string {
  switch (node.type) {
    case "input":
      return definition.inputs.length === 0 ? "无输入变量" : definition.inputs.map((i) => i.label || i.name).join("、");
    case "output":
      return definition.outputs.length === 0 ? "未声明结果字段" : definition.outputs.map((o) => o.name).join("、");
    case "llm": {
      const c = llmConfig(node);
      return c.model ? `${c.model}${c.json_fields ? " · JSON" : ""}` : "未选择模型";
    }
    case "template":
      return firstLine(templateConfig(node).template) || "空模板";
    case "image_generation": {
      const c = imageConfig(node);
      return `${c.model || "部署默认"}${c.size ? ` · ${c.size}` : ""}`;
    }
    case "comfyui_template":
      return comfyConfig(node).template || "未选择模板";
    case "condition": {
      const c = conditionConfig(node);
      const op = OPERATORS.find((o) => o.value === c.op);
      return `${firstLine(c.left) || "…"} ${op?.label ?? c.op}${op?.unary ? "" : ` ${firstLine(c.right ?? "") || "…"}`}`;
    }
  }
}

function firstLine(text: string): string {
  return text.split("\n").find((line) => line.trim() !== "")?.trim() ?? "";
}

export function FlowNodeCard({ data, selected }: NodeProps<FlowCanvasNode>) {
  const { node, definition, issues, warning, state } = data;
  const kind = NODE_KIND[node.type];
  const badge = state ? STATE_BADGE[state] : null;
  return (
    <div
      className={cn(
        "w-56 rounded-xl border bg-card text-card-foreground shadow-sm transition-shadow",
        selected && "ring-2 ring-ring",
        issues > 0 ? "border-destructive" : warning ? "border-amber-500" : "",
        state === "skipped" && "opacity-60",
        state === "running" && "shadow-md shadow-sky-500/20"
      )}
    >
      {node.type !== "input" ? <Handle type="target" position={Position.Left} className="!size-2.5 !border-2 !bg-background" /> : null}
      <div className="flex items-center gap-2 px-3 pt-2.5">
        <span className={cn("inline-flex size-6 shrink-0 items-center justify-center rounded-md [&_svg]:size-3.5", kind.tint)}>{kind.icon}</span>
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium leading-tight">{node.label || kind.label}</p>
          <p className="truncate font-mono text-[10px] text-muted-foreground">{node.id}</p>
        </div>
        {badge ? (
          <span title={badge.label} className={cn("inline-flex size-5 items-center justify-center [&_svg]:size-4", badge.className)}>
            {badge.icon}
          </span>
        ) : issues > 0 ? (
          <span title={`${issues} 个问题`} className="inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-destructive px-1 text-[10px] font-medium text-white">
            {issues}
          </span>
        ) : warning ? (
          <span title={warning} className="text-amber-600 [&_svg]:size-4">
            <TriangleAlertIcon />
          </span>
        ) : null}
      </div>
      <p className="truncate px-3 pt-1 pb-2.5 text-xs text-muted-foreground">{nodeSummary(node, definition)}</p>
      {node.type === "condition" ? (
        <>
          <Handle id="true" type="source" position={Position.Right} style={{ top: "38%" }} className="!size-2.5 !border-2 !border-emerald-500 !bg-background" />
          <Handle id="false" type="source" position={Position.Right} style={{ top: "72%" }} className="!size-2.5 !border-2 !border-rose-500 !bg-background" />
          <span className="pointer-events-none absolute -right-5 top-[38%] -translate-y-1/2 text-[10px] text-emerald-600">是</span>
          <span className="pointer-events-none absolute -right-5 top-[72%] -translate-y-1/2 text-[10px] text-rose-600">否</span>
        </>
      ) : node.type !== "output" ? (
        <Handle type="source" position={Position.Right} className="!size-2.5 !border-2 !bg-background" />
      ) : null}
    </div>
  );
}

export const FLOW_NODE_TYPES = { flow: FlowNodeCard };

/** ARIA_LABELS gives the canvas's built-in controls Chinese names.
 * ARIA_LABELS 为画布内置控件提供中文名称。 */
export const ARIA_LABELS = {
  "node.a11yDescription.default": "按 Enter 或空格选择节点，按 Delete 删除，方向键移动。",
  "node.a11yDescription.keyboardDisabled": "按 Enter 或空格选择节点。",
  "node.a11yDescription.ariaLiveMessage": ({ direction, x, y }: { direction: string; x: number; y: number }) => `节点已向${direction}移动到 ${x}, ${y}`,
  "edge.a11yDescription.default": "按 Enter 或空格选择连线，按 Delete 删除。",
  "controls.ariaLabel": "画布控件",
  "controls.zoomIn.ariaLabel": "放大",
  "controls.zoomOut.ariaLabel": "缩小",
  "controls.fitView.ariaLabel": "适应画布",
  "controls.interactive.ariaLabel": "锁定或解锁画布",
  "minimap.ariaLabel": "缩略图",
  "handle.ariaLabel": "连接点",
};

import { coreTranslator } from "../i18n/core.ts";
import { DEFAULT_LOCALE, type Locale } from "../i18n/locale.ts";
/** MAX_PREVIEW_NODES bounds how many nodes the graph preview lays out. A
 * ComfyUI workflow is typically tens of nodes; past this the canvas would be
 * unreadable anyway, so the preview declines instead of freezing the tab.
 *
 * MAX_PREVIEW_NODES 限制图预览参与布局的节点数。ComfyUI 工作流通常只有几十个节点；
 * 超过这个数画布本来也无法阅读，因此预览直接拒绝，而不是卡死标签页。 */
export const MAX_PREVIEW_NODES = 500;

/** PREVIEW_LITERAL_CHARS bounds one literal value's on-canvas text; the full value stays in the detail panel.
 * PREVIEW_LITERAL_CHARS 限制单个字面量在画布上的文本长度；完整取值留在详情面板里。 */
export const PREVIEW_LITERAL_CHARS = 48;

/** Layout constants shared with the canvas component so handle rows and computed heights agree.
 * 与画布组件共用的布局常量，保证句柄所在行与计算出的高度一致。 */
export const NODE_WIDTH = 260;
export const NODE_HEADER_HEIGHT = 44;
export const NODE_ROW_HEIGHT = 22;
export const NODE_PADDING = 8;
const GAP_X = 80;
const GAP_Y = 32;

/** GraphField is one entry of a node's inputs: either a link to another node's output slot or a literal.
 * GraphField 是节点 inputs 中的一项：要么是指向另一节点输出槽的连线，要么是字面量。 */
export interface GraphField {
  name: string;
  kind: "link" | "literal";
  /** Short on-canvas text. / 画布上的简短文本。 */
  text: string;
  /** Full value for the detail panel. / 详情面板中的完整取值。 */
  full: string;
  /** Names of template inputs bound to this field. / 绑定到该字段的模板输入名。 */
  bound: string[];
  /** A link whose source node is not in the graph. / 源节点不在图中的连线。 */
  dangling: boolean;
}

/** GraphNodeDiff marks a node against the graph it is compared with: present only on this side, or present on both with different content.
 * GraphNodeDiff 标记节点相对于比较对象的差异：只在本侧存在，或两侧都有但内容不同。 */
export type GraphNodeDiff = "only-here" | "changed";

/** GraphNode is one laid-out ComfyUI node. / GraphNode 是一个已完成布局的 ComfyUI 节点。 */
export interface GraphNode {
  id: string;
  classType: string;
  title?: string;
  fields: GraphField[];
  /** Output slot indices some other node consumes, ascending. / 被其他节点消费的输出槽序号，升序。 */
  outputSlots: number[];
  /** Names of template outputs declared on this node. / 声明在该节点上的模板输出名。 */
  outputs: string[];
  diff?: GraphNodeDiff;
  x: number;
  y: number;
  height: number;
}

/** GraphEdge is one link from an output slot to an input field. / GraphEdge 是从输出槽到输入字段的一条连线。 */
export interface GraphEdge {
  id: string;
  source: string;
  sourceSlot: number;
  target: string;
  targetField: string;
}

/** GraphPreview is the renderable result, or the reason there is none.
 * GraphPreview 是可渲染的结果，或者无法渲染的原因。 */
export type GraphPreview =
  | { ok: true; nodes: GraphNode[]; edges: GraphEdge[]; issues: string[]; missingHere: string[] }
  | { ok: false; reason: "invalid_json" | "not_object" | "empty" | "too_many_nodes"; count?: number };

/** GraphBindings are the template declarations the preview overlays on the graph.
 * GraphBindings 是预览叠加到图上的模板声明。 */
export interface GraphBindings {
  inputs: { name: string; node: string; field: string }[];
  outputs: { name: string; node: string }[];
}

/** ParsedGraphText is the outcome of reading the draft textarea. / ParsedGraphText 是读取草稿文本框的结果。 */
export type ParsedGraphText = { ok: true; graph: unknown } | { ok: false };

/** parseGraphText decodes the graph textarea without throwing, so a half-typed draft renders as a notice, not an error.
 * parseGraphText 不抛异常地解码图文本框，使输入到一半的草稿显示为提示而不是错误。 */
export function parseGraphText(text: string): ParsedGraphText {
  try { return { ok: true, graph: JSON.parse(text) }; } catch { return { ok: false }; }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isLink(value: unknown): value is [string, number] {
  return Array.isArray(value) && value.length === 2 && typeof value[0] === "string" && Number.isInteger(value[1]) && (value[1] as number) >= 0;
}

/** compareIDs orders ComfyUI node ids numerically when both are numeric, as ComfyUI assigns them, and lexically otherwise.
 * compareIDs 在两个节点 id 都是数字时按数值排序（与 ComfyUI 的分配方式一致），否则按字典序。 */
export function compareIDs(a: string, b: string): number {
  const na = /^\d+$/.test(a), nb = /^\d+$/.test(b);
  if (na && nb) return Number(a) - Number(b) || a.localeCompare(b);
  if (na !== nb) return na ? -1 : 1;
  return a.localeCompare(b);
}

/** stableStringify renders JSON with sorted object keys, so two nodes that differ only in key order compare equal.
 * stableStringify 以排序后的对象键输出 JSON，使只有键顺序不同的两个节点被视为相同。 */
export function stableStringify(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(",")}]`;
  if (isRecord(value)) return `{${Object.keys(value).sort().map((k) => `${JSON.stringify(k)}:${stableStringify(value[k])}`).join(",")}}`;
  return JSON.stringify(value) ?? "null";
}

function literalText(value: unknown): { text: string; full: string } {
  const full = typeof value === "string" ? value : JSON.stringify(value) ?? "null";
  const flat = full.replace(/\s+/g, " ").trim();
  const text = flat.length > PREVIEW_LITERAL_CHARS ? `${flat.slice(0, PREVIEW_LITERAL_CHARS)}…` : flat;
  return { text: typeof value === "string" && text === "" ? "\"\"" : text, full };
}

/** nodeHeight is the canvas height of a node with the given output-slot and field rows.
 * nodeHeight 是带有给定输出槽行与字段行的节点在画布上的高度。 */
export function nodeHeight(outputSlotRows: number, fieldRows: number): number {
  return NODE_HEADER_HEIGHT + (outputSlotRows + fieldRows) * NODE_ROW_HEIGHT + NODE_PADDING * 2;
}

/** buildGraphPreview turns an API-format ComfyUI graph into laid-out nodes and edges for a read-only canvas. It never throws: malformed entries become issues, and whole-graph problems become a reason.
 *
 * Layout is layered left to right: a node's column is the longest link path from any source, and rows within a column are ordered by the mean row of their upstream nodes. API format carries no positions, so this is a reconstruction, not the author's original arrangement.
 *
 * buildGraphPreview 把 API Format 的 ComfyUI 图转换为只读画布所需的已布局节点与连线。它从不抛异常：格式不对的条目记为问题，整图层面的问题记为原因。
 *
 * 布局自左向右分层：节点所在列是从任一源节点出发的最长连线路径，同列内按上游节点平均行号排序。API Format 不携带坐标，所以这是重建的布局，不是作者原本的摆放。 */
export function buildGraphPreview(graph: unknown, bindings: GraphBindings, compareTo?: unknown, locale: Locale = DEFAULT_LOCALE): GraphPreview {
  const t = coreTranslator(locale);
  if (!isRecord(graph)) return { ok: false, reason: "not_object" };
  const ids = Object.keys(graph).sort(compareIDs);
  if (ids.length === 0) return { ok: false, reason: "empty" };
  if (ids.length > MAX_PREVIEW_NODES) return { ok: false, reason: "too_many_nodes", count: ids.length };

  const issues: string[] = [];
  const nodes = new Map<string, GraphNode>();
  for (const id of ids) {
    const raw = graph[id];
    if (!isRecord(raw)) { issues.push(t("common.nodeIsNotAnObjectSkipped", { id: id })); continue; }
    const classType = typeof raw.class_type === "string" && raw.class_type !== "" ? raw.class_type : "?";
    if (classType === "?") issues.push(t("common.nodeIsMissingClassType", { id: id }));
    const meta = isRecord(raw._meta) ? raw._meta : undefined;
    const title = meta && typeof meta.title === "string" && meta.title !== "" && meta.title !== classType ? meta.title : undefined;
    if (!isRecord(raw.inputs)) issues.push(t("common.nodeHasNoInputsObjectPublicationValidationWill", { id: id }));
    const inputs = isRecord(raw.inputs) ? raw.inputs : {};
    const fields: GraphField[] = Object.keys(inputs).map((name) => {
      const value = inputs[name];
      if (isLink(value)) return { name, kind: "link", text: `← ${value[0]}#${value[1]}`, full: JSON.stringify(value), bound: [], dangling: false };
      return { name, kind: "literal", ...literalText(value), bound: [], dangling: false };
    });
    nodes.set(id, { id, classType, title, fields, outputSlots: [], outputs: [], x: 0, y: 0, height: 0 });
  }

  const edges: GraphEdge[] = [];
  const upstream = new Map<string, Set<string>>();
  for (const node of nodes.values()) upstream.set(node.id, new Set());
  for (const node of nodes.values()) {
    for (const field of node.fields) {
      if (field.kind !== "link") continue;
      const [source, slot] = JSON.parse(field.full) as [string, number];
      const from = nodes.get(source);
      if (!from) { field.dangling = true; issues.push(t("common.nodeSReferencesNonexistentNode", { id: node.id, name: field.name, source: source })); continue; }
      if (!from.outputSlots.includes(slot)) from.outputSlots.push(slot);
      if (source === node.id) issues.push(t("common.nodeSLinksBackToItselfComfyuiCannot", { id: node.id, name: field.name }));
      else upstream.get(node.id)!.add(source);
      edges.push({ id: `${source}:${slot}->${node.id}.${field.name}`, source, sourceSlot: slot, target: node.id, targetField: field.name });
    }
  }
  for (const node of nodes.values()) node.outputSlots.sort((a, b) => a - b);

  for (const input of bindings.inputs) {
    const field = nodes.get(input.node)?.fields.find((f) => f.name === input.field);
    if (field) field.bound.push(input.name || t("common.unnamed"));
    else issues.push(t("common.inputIsBoundToWhichIsNotIn", { value: input.name || t("common.unnamed"), value2: input.node || "?", value3: input.field || "?" }));
  }
  for (const output of bindings.outputs) {
    const node = nodes.get(output.node);
    if (node) node.outputs.push(output.name || t("common.unnamed"));
    else issues.push(t("common.outputPointsToNodeWhichIsNotIn", { value: output.name || t("common.unnamed"), value2: output.node || "?" }));
  }

  // Longest-path layering via Kahn's algorithm; whatever is left belongs to a cycle.
  // 用 Kahn 算法做最长路径分层；剩下没处理到的节点属于某个环。
  const rank = new Map<string, number>();
  const pending = new Map<string, number>();
  const downstream = new Map<string, string[]>();
  for (const [id, ups] of upstream) {
    pending.set(id, ups.size);
    for (const up of ups) downstream.set(up, [...(downstream.get(up) ?? []), id]);
  }
  const queue = [...nodes.keys()].filter((id) => pending.get(id) === 0);
  for (const id of queue) rank.set(id, 0);
  for (let i = 0; i < queue.length; i++) {
    const id = queue[i];
    for (const next of downstream.get(id) ?? []) {
      rank.set(next, Math.max(rank.get(next) ?? 0, rank.get(id)! + 1));
      pending.set(next, pending.get(next)! - 1);
      if (pending.get(next) === 0) queue.push(next);
    }
  }
  const cyclic = [...nodes.keys()].filter((id) => !rank.has(id));
  if (cyclic.length > 0) {
    issues.push(t("common.theGraphContainsACycleInvolvingNodesComfyui", { value: cyclic.join(", ") }));
    const last = Math.max(0, ...rank.values()) + 1;
    for (const id of cyclic) rank.set(id, last);
  }

  const columns: string[][] = [];
  for (const id of nodes.keys()) (columns[rank.get(id)!] ??= []).push(id);
  const row = new Map<string, number>();
  for (const column of columns) {
    if (!column) continue;
    const key = (id: string) => {
      const ups = [...upstream.get(id)!].filter((u) => row.has(u));
      return ups.length === 0 ? Number.POSITIVE_INFINITY : ups.reduce((sum, u) => sum + row.get(u)!, 0) / ups.length;
    };
    column.sort((a, b) => key(a) - key(b) || compareIDs(a, b));
    column.forEach((id, i) => row.set(id, i));
  }
  columns.forEach((column, c) => {
    let y = 0;
    for (const id of column ?? []) {
      const node = nodes.get(id)!;
      node.height = nodeHeight(node.outputSlots.length, node.fields.length);
      node.x = c * (NODE_WIDTH + GAP_X);
      node.y = y;
      y += node.height + GAP_Y;
    }
  });

  const missingHere: string[] = [];
  if (isRecord(compareTo)) {
    for (const node of nodes.values()) {
      if (!(node.id in compareTo)) node.diff = "only-here";
      else if (stableStringify(graph[node.id]) !== stableStringify(compareTo[node.id])) node.diff = "changed";
    }
    for (const id of Object.keys(compareTo).sort(compareIDs)) if (!(id in graph)) missingHere.push(id);
  }

  edges.sort((a, b) => a.id.localeCompare(b.id));
  return { ok: true, nodes: [...nodes.values()], edges, issues, missingHere };
}

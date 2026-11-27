import { artifactFieldShape } from "./artifacts.ts";
import type { FlowCatalog } from "./flow-catalog.ts";
import { templateRefs } from "./flow-template.ts";
import type {
  Branch,
  ComfyUIConfig,
  ConditionConfig,
  FlowDefinition,
  FlowEdge,
  FlowIssue,
  FlowNode,
  ImageConfig,
  JSONField,
  LLMConfig,
  NodeType,
  Operator,
  TemplateConfig,
} from "./flows.ts";

/**
 * The editor's model of a flow: pure functions over a FlowDefinition that the
 * canvas calls, and the conversion of edges to the canvas's shape. Every
 * function returns a new definition; none mutates its argument. The rules
 * here give immediate feedback while editing; the authority on validity is
 * common/flowdef, applied when the flow is saved.
 *
 * 编辑器对编排的模型：画布调用的、作用于 FlowDefinition 的纯函数，以及把连线转换为画布
 * 形状的函数。每个函数都返回新的定义，从不修改实参。这里的规则在编辑时给出即时反馈；
 * 有效性的权威是保存时执行的 common/flowdef。
 */

/** CanvasEdge is an edge in the shape the canvas draws.
 * CanvasEdge 是画布所绘连线的形状。 */
export interface CanvasEdge {
  id: string;
  source: string;
  target: string;
  sourceHandle: string | null;
  label?: string;
}

/** EditableTypes are the node types a user can add; the input and output
 * nodes come with every flow and cannot be added or removed.
 *
 * EditableTypes 是用户可以添加的节点类型；输入与输出节点随每个编排自带，不能添加或删除。 */
export const EDITABLE_TYPES: NodeType[] = ["llm", "template", "condition", "image_generation", "comfyui_template"];

/** MAX_IMAGE_SIDE bounds each side of an image node's size, as in flowdef.
 * MAX_IMAGE_SIDE 是图像节点尺寸每一边的上限，与 flowdef 一致。 */
const MAX_IMAGE_SIDE = 16384;

/** isImageSize mirrors flowdef's size rule: "<width>x<height>", each side a
 * decimal integer from 1 to 16384 without sign or leading zero.
 * isImageSize 对应 flowdef 的尺寸规则：「宽x高」，每边是 1 到 16384 的十进制整数，无符号、
 * 无前导零。 */
export function isImageSize(value: string): boolean {
  const match = /^([1-9][0-9]{0,4})x([1-9][0-9]{0,4})$/.exec(value);
  return match !== null && Number(match[1]) <= MAX_IMAGE_SIDE && Number(match[2]) <= MAX_IMAGE_SIDE;
}

export const OPERATORS: { value: Operator; label: string; unary?: boolean; numeric?: boolean }[] = [
  { value: "contains", label: "包含" },
  { value: "not_contains", label: "不包含" },
  { value: "equals", label: "等于" },
  { value: "not_equals", label: "不等于" },
  { value: "gt", label: "大于", numeric: true },
  { value: "gte", label: "大于等于", numeric: true },
  { value: "lt", label: "小于", numeric: true },
  { value: "lte", label: "小于等于", numeric: true },
  { value: "empty", label: "为空", unary: true },
  { value: "not_empty", label: "不为空", unary: true },
];

const IDENT = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;

/** isIdentifier mirrors flowdef's identifier rule for names and node ids.
 * isIdentifier 对应 flowdef 对名称与节点 id 的标识符规则。 */
export function isIdentifier(value: string): boolean {
  return IDENT.test(value) && value !== "inputs";
}

/** newDefinition is a fresh flow: an input node wired to an output node,
 * which is already a valid flow, so a new one can be saved at once.
 *
 * newDefinition 是一个全新的编排：输入节点连到输出节点，这已是一个有效的编排，因此新建后
 * 可以立即保存。 */
export function newDefinition(): FlowDefinition {
  return {
    inputs: [{ name: "topic", label: "主题", type: "text", required: true }],
    nodes: [
      { id: "start", type: "input", position: { x: 0, y: 0 } },
      { id: "end", type: "output", position: { x: 640, y: 0 } },
    ],
    edges: [{ from: "start", to: "end" }],
    outputs: [],
  };
}

export function llmConfig(node: FlowNode): LLMConfig {
  const c = node.config ?? {};
  const out: LLMConfig = { model: typeof c.model === "string" ? c.model : "", prompt: typeof c.prompt === "string" ? c.prompt : "" };
  if (typeof c.system === "string" && c.system !== "") out.system = c.system;
  if (typeof c.temperature === "number") out.temperature = c.temperature;
  if (typeof c.max_tokens === "number") out.max_tokens = c.max_tokens;
  if (Array.isArray(c.json_fields) && c.json_fields.length > 0) out.json_fields = c.json_fields as JSONField[];
  return out;
}

export function templateConfig(node: FlowNode): TemplateConfig {
  const c = node.config ?? {};
  return { template: typeof c.template === "string" ? c.template : "" };
}

export function conditionConfig(node: FlowNode): ConditionConfig {
  const c = node.config ?? {};
  const op = OPERATORS.some((o) => o.value === c.op) ? (c.op as Operator) : "contains";
  const out: ConditionConfig = { left: typeof c.left === "string" ? c.left : "", op };
  if (typeof c.right === "string" && c.right !== "") out.right = c.right;
  return out;
}

export function imageConfig(node: FlowNode): ImageConfig {
  const c = node.config ?? {};
  const out: ImageConfig = { model: typeof c.model === "string" ? c.model : "", prompt: typeof c.prompt === "string" ? c.prompt : "" };
  if (typeof c.size === "string" && c.size !== "") out.size = c.size;
  return out;
}

export function comfyConfig(node: FlowNode): ComfyUIConfig {
  const c = node.config ?? {};
  const inputs: Record<string, string> = {};
  if (typeof c.inputs === "object" && c.inputs !== null && !Array.isArray(c.inputs)) {
    for (const [name, value] of Object.entries(c.inputs)) if (typeof value === "string") inputs[name] = value;
  }
  return { template: typeof c.template === "string" ? c.template : "", inputs };
}

/** defaultConfig is a new node's config; model and imageModel are the
 * defaults for llm and image nodes.
 * defaultConfig 是新节点的配置；model 与 imageModel 分别是 llm 与图像节点的默认模型。 */
export function defaultConfig(type: NodeType, model: string, imageModel = ""): Record<string, unknown> | undefined {
  switch (type) {
    case "llm":
      return { model, prompt: "" };
    case "image_generation":
      return { model: imageModel, prompt: "" };
    case "comfyui_template":
      return { template: "", inputs: {} };
    case "template":
      return { template: "" };
    case "condition":
      return { left: "", op: "contains", right: "" };
    default:
      return undefined;
  }
}

/** nextNodeId picks the first free "<type>_<n>". / nextNodeId 选出第一个空闲的「类型_序号」。 */
export function nextNodeId(def: FlowDefinition, type: NodeType): string {
  const taken = new Set(def.nodes.map((n) => n.id));
  for (let n = 1; ; n++) {
    const id = `${type}_${n}`;
    if (!taken.has(id)) return id;
  }
}

/** addNode adds a node of type at position and returns its id.
 * addNode 在 position 处添加一个 type 类型的节点并返回其 id。 */
export function addNode(def: FlowDefinition, type: NodeType, position: { x: number; y: number }, model = "", imageModel = ""): { def: FlowDefinition; id: string } {
  const id = nextNodeId(def, type);
  const node: FlowNode = { id, type, position: { x: Math.round(position.x), y: Math.round(position.y) } };
  const config = defaultConfig(type, model, imageModel);
  if (config) node.config = config;
  return { def: { ...def, nodes: [...def.nodes, node] }, id };
}

/** removeNodes removes nodes and their edges; the input and output nodes stay.
 * removeNodes 删除节点及其连线；输入与输出节点保留。 */
export function removeNodes(def: FlowDefinition, ids: string[]): FlowDefinition {
  const drop = new Set(def.nodes.filter((n) => ids.includes(n.id) && n.type !== "input" && n.type !== "output").map((n) => n.id));
  if (drop.size === 0) return def;
  return {
    ...def,
    nodes: def.nodes.filter((n) => !drop.has(n.id)),
    edges: def.edges.filter((e) => !drop.has(e.from) && !drop.has(e.to)),
  };
}

export function updateNode(def: FlowDefinition, id: string, change: (node: FlowNode) => FlowNode): FlowDefinition {
  return { ...def, nodes: def.nodes.map((n) => (n.id === id ? change(n) : n)) };
}

export function moveNode(def: FlowDefinition, id: string, position: { x: number; y: number }): FlowDefinition {
  return updateNode(def, id, (n) => ({ ...n, position: { x: Math.round(position.x), y: Math.round(position.y) } }));
}

export function setConfig(def: FlowDefinition, id: string, config: Record<string, unknown>): FlowDefinition {
  return updateNode(def, id, (n) => ({ ...n, config }));
}

export function edgeId(e: FlowEdge): string {
  return `${e.from}->${e.to}${e.branch ? `:${e.branch}` : ""}`;
}

/** toCanvasEdges converts the definition's edges for the canvas. An edge
 * leaving a condition leaves from that branch's handle and is labelled.
 *
 * toCanvasEdges 把定义中的连线转换给画布。从条件节点出发的连线从对应分支的连接点引出并
 * 带标签。 */
export function toCanvasEdges(def: FlowDefinition): CanvasEdge[] {
  return def.edges.map((e) => {
    const out: CanvasEdge = { id: edgeId(e), source: e.from, target: e.to, sourceHandle: e.branch ?? null };
    if (e.branch) out.label = e.branch === "true" ? "是" : "否";
    return out;
  });
}

export function removeEdges(def: FlowDefinition, ids: string[]): FlowDefinition {
  const drop = new Set(ids);
  return { ...def, edges: def.edges.filter((e) => !drop.has(edgeId(e))) };
}

export type ConnectResult = { ok: true; def: FlowDefinition } | { ok: false; reason: string };

/** connect adds the edge a drag on the canvas asks for, or says why not.
 * connect 添加画布上一次拖拽所要求的连线，否则说明原因。 */
export function connect(def: FlowDefinition, source: string, target: string, handle: string | null): ConnectResult {
  const from = def.nodes.find((n) => n.id === source);
  const to = def.nodes.find((n) => n.id === target);
  if (!from || !to) return { ok: false, reason: "节点不存在。" };
  if (source === target) return { ok: false, reason: "节点不能连到自己。" };
  if (to.type === "input") return { ok: false, reason: "输入节点是起点，不能有连入的线。" };
  if (from.type === "output") return { ok: false, reason: "输出节点是终点，不能再连出去。" };
  let branch: Branch | undefined;
  if (from.type === "condition") {
    if (handle !== "true" && handle !== "false") return { ok: false, reason: "请从条件节点的「是」或「否」连出。" };
    branch = handle;
  }
  const edge: FlowEdge = branch ? { from: source, to: target, branch } : { from: source, to: target };
  if (def.edges.some((e) => edgeId(e) === edgeId(edge))) return { ok: false, reason: "这条线已经存在。" };
  if (reachable(def, target).has(source)) return { ok: false, reason: "这样连会形成环。" };
  return { ok: true, def: { ...def, edges: [...def.edges, edge] } };
}

/** reachable is every node downstream of id. / reachable 是 id 下游的全部节点。 */
function reachable(def: FlowDefinition, id: string): Set<string> {
  const seen = new Set<string>();
  const queue = [id];
  while (queue.length > 0) {
    const current = queue.shift()!;
    for (const e of def.edges) {
      if (e.from === current && !seen.has(e.to)) {
        seen.add(e.to);
        queue.push(e.to);
      }
    }
  }
  return seen;
}

/** upstreamOf is every node upstream of id. / upstreamOf 是 id 上游的全部节点。 */
export function upstreamOf(def: FlowDefinition, id: string): Set<string> {
  const seen = new Set<string>();
  const queue = [id];
  while (queue.length > 0) {
    const current = queue.shift()!;
    for (const e of def.edges) {
      if (e.to === current && !seen.has(e.from)) {
        seen.add(e.from);
        queue.push(e.from);
      }
    }
  }
  return seen;
}

/** nodeFields is what {{<node>.<field>}} may name on a node.
 * nodeFields 是 {{<节点>.<字段>}} 可以引用的字段。 */
export function nodeFields(node: FlowNode): string[] {
  switch (node.type) {
    case "llm":
      return ["text", ...(llmConfig(node).json_fields ?? []).map((f) => f.name).filter((name) => name !== "text")];
    case "template":
      return ["text"];
    case "condition":
      return ["result"];
    case "image_generation":
      return ["image"];
    case "comfyui_template":
      return ["image", "artifacts"];
    default:
      return [];
  }
}

export interface Reference {
  ref: string;
  label: string;
}

/** referencesFor lists the variables a template on node id may use: the
 * flow's inputs and the fields of every upstream node. Artifact fields (the
 * image and files an image or ComfyUI node makes) are listed only when
 * artifacts is set, which is for an output's result fields: nowhere else may
 * an artifact be referenced.
 *
 * referencesFor 列出节点 id 上的模板可以使用的变量：编排的输入，以及每个上游节点的字段。
 * 产物字段（图像或 ComfyUI 节点产出的图片与文件）只在设置了 artifacts 时列出，那是给输出
 * 节点的结果字段用的：其他任何地方都不能引用产物。 */
export function referencesFor(def: FlowDefinition, id: string, artifacts = false): Reference[] {
  const out: Reference[] = def.inputs
    .filter((input) => isIdentifier(input.name))
    .map((input) => ({ ref: `{{inputs.${input.name}}}`, label: `输入 · ${input.label || input.name}` }));
  const upstream = upstreamOf(def, id);
  for (const node of def.nodes) {
    if (!upstream.has(node.id)) continue;
    for (const field of nodeFields(node)) {
      if (!artifacts && artifactFieldShape(node.type, field)) continue;
      out.push({ ref: `{{${node.id}.${field}}}`, label: `${node.label || node.id} · ${field}` });
    }
  }
  return out;
}

/** unavailableModels flags llm nodes whose model is not among those the user
 * may call now, as issues located on those nodes.
 *
 * unavailableModels 把模型不在用户当前可用列表中的 llm 节点标出来，作为定位到这些节点
 * 的问题。 */
export function unavailableModels(def: FlowDefinition, models: string[]): FlowIssue[] {
  const offered = new Set(models);
  return def.nodes
    .filter((n) => n.type === "llm" && !offered.has(llmConfig(n).model))
    .map((n) => ({ node: n.id, field: "config.model", code: "model_unavailable" }));
}

/** catalogWarnings flags image_generation and comfyui_template nodes that
 * cannot run against what the catalogs offer now, as issues located on the
 * nodes: an image model that is gone (or no model while the deployment has
 * no default), a workflow template that is not offered, an input the
 * template lacks, a required input left unset, or an input that needs a
 * file, which a flow cannot supply. A catalog that failed to load flags
 * nothing.
 *
 * catalogWarnings 把按目录当前内容无法运行的 image_generation 与 comfyui_template 节点标出
 * 来，作为定位到这些节点的问题：图像模型已不存在（或未指定模型而部署没有默认生图）、
 * 工作流模板不在可用列表中、模板没有的输入、必填却未设置的输入，以及需要文件输入——编排
 * 无法提供文件。加载失败的目录不标记任何东西。 */
export function catalogWarnings(def: FlowDefinition, catalog: FlowCatalog): FlowIssue[] {
  const out: FlowIssue[] = [];
  for (const node of def.nodes) {
    if (node.type === "image_generation" && catalog.imageModels) {
      const { model } = imageConfig(node);
      if (model === "" && !catalog.imageModels.defaultAvailable) out.push({ node: node.id, field: "config.model", code: "image_generation_unavailable" });
      else if (model !== "" && !catalog.imageModels.models.some((m) => m.id === model)) out.push({ node: node.id, field: "config.model", code: "model_unavailable" });
    }
    if (node.type === "comfyui_template" && catalog.templates) {
      const config = comfyConfig(node);
      const template = catalog.templates.find((t) => t.id === config.template);
      if (!template) {
        out.push({ node: node.id, field: "config.template", code: "template_unavailable" });
        continue;
      }
      const declared = new Set(template.inputs.map((input) => input.name));
      for (const name of Object.keys(config.inputs)) {
        if (!declared.has(name)) out.push({ node: node.id, field: `config.inputs.${name}`, code: "invalid_input" });
      }
      for (const input of template.inputs) {
        if (input.type === "file") {
          if (input.required) out.push({ node: node.id, field: `config.inputs.${input.name}`, code: "unsupported_input" });
        } else if (input.required && input.default === undefined && (config.inputs[input.name] ?? "").trim() === "") {
          out.push({ node: node.id, field: `config.inputs.${input.name}`, code: "required" });
        }
      }
    }
  }
  return out;
}

/** artifactReferenceIssues finds artifact fields referenced anywhere but as
 * the whole of an output's ref — in a prompt, a template, a condition, an
 * image prompt, a ComfyUI input, or mixed with other text in a result
 * field — as the issues the server reports on save. Output problems sit on
 * the output node, as the server places them.
 *
 * artifactReferenceIssues 找出在输出 ref 的全部内容之外被引用的产物字段——在提示词、模板、
 * 条件、图像提示词、ComfyUI 输入里，或在结果字段中与其他文本混在一起——给出与服务端保存时
 * 报告的相同的问题。输出的问题与服务端一样位于输出节点上。 */
export function artifactReferenceIssues(def: FlowDefinition): FlowIssue[] {
  const types = new Map(def.nodes.map((n) => [n.id, n.type] as const));
  const names = (text: string) => templateRefs(text).refs.some((ref) => artifactFieldShape(types.get(ref.scope) ?? "", ref.field));
  const out: FlowIssue[] = [];
  const check = (node: string, field: string, text: string) => {
    if (names(text)) out.push({ node, field, code: "artifact_reference" });
  };
  for (const node of def.nodes) {
    const c = node.config ?? {};
    const text = (value: unknown) => (typeof value === "string" ? value : "");
    switch (node.type) {
      case "llm":
        check(node.id, "config.system", text(c.system));
        check(node.id, "config.prompt", text(c.prompt));
        break;
      case "template":
        check(node.id, "config.template", text(c.template));
        break;
      case "condition":
        check(node.id, "config.left", text(c.left));
        check(node.id, "config.right", text(c.right));
        break;
      case "image_generation":
        check(node.id, "config.prompt", text(c.prompt));
        break;
      case "comfyui_template":
        for (const [name, value] of Object.entries(comfyConfig(node).inputs)) check(node.id, `config.inputs.${name}`, value);
        break;
    }
  }
  const outputNode = def.nodes.find((n) => n.type === "output")?.id ?? "";
  for (const output of def.outputs) {
    if (names(output.ref) && !templateRefs(output.ref).sole) out.push({ node: outputNode, field: `outputs.${output.name}.ref`, code: "artifact_reference" });
  }
  return out;
}

/** mergeIssues adds the locally found issues to those the server reported,
 * leaving out one the server already reported for the same node and field
 * (on the output node, any of its fields, since the server may number a
 * result field where the editor names it).
 *
 * mergeIssues 把本地发现的问题并入服务端报告的问题，服务端已对同一节点与字段报告过的则
 * 略去（对输出节点，其任何字段都算，因为服务端可能按序号而编辑器按名称称呼结果字段）。 */
export function mergeIssues(def: FlowDefinition, server: FlowIssue[], local: FlowIssue[]): FlowIssue[] {
  const output = def.nodes.find((n) => n.type === "output")?.id;
  const known = (issue: FlowIssue) =>
    server.some((s) => s.node === issue.node && s.code === issue.code && (s.field === issue.field || (issue.node === output && s.field.startsWith("outputs"))));
  return [...server, ...local.filter((issue) => !known(issue))];
}

/** serialize is the definition as it is sent to save: the nodes' configs
 * without empty optional fields.
 *
 * serialize 是保存时发送的定义：节点配置中去掉为空的可选字段。 */
export function serialize(def: FlowDefinition): FlowDefinition {
  return {
    inputs: def.inputs.map((input) => {
      const out = { ...input };
      if (!out.label) delete out.label;
      if (out.default === undefined || out.default === "") delete out.default;
      return out;
    }),
    nodes: def.nodes.map((node) => {
      const out: FlowNode = { id: node.id, type: node.type, position: node.position };
      if (node.label) out.label = node.label;
      if (node.type === "llm") out.config = { ...llmConfig(node) };
      else if (node.type === "template") out.config = { ...templateConfig(node) };
      else if (node.type === "image_generation") {
        const c = imageConfig(node);
        out.config = { ...(c.model ? { model: c.model } : {}), prompt: c.prompt, ...(c.size ? { size: c.size } : {}) };
      } else if (node.type === "comfyui_template") {
        const c = comfyConfig(node);
        const inputs = Object.fromEntries(Object.entries(c.inputs).filter(([, value]) => value !== ""));
        out.config = { template: c.template, ...(Object.keys(inputs).length > 0 ? { inputs } : {}) };
      } else if (node.type === "condition") {
        const c = conditionConfig(node);
        const unary = OPERATORS.find((o) => o.value === c.op)?.unary;
        out.config = unary ? { left: c.left, op: c.op } : { ...c, right: c.right ?? "" };
      }
      return out;
    }),
    edges: def.edges,
    outputs: def.outputs,
  };
}

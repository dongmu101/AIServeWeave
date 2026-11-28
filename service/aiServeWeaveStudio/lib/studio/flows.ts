import { ContractError } from "./contract.ts";

/**
 * Flows (编排) as Studio reads them from the Gateway's /studio/v1/flows and
 * /studio/v1/runs, and the definition shape of common/flowdef. The parsers
 * throw ContractError on a shape they do not know.
 *
 * Studio 从 Gateway 的 /studio/v1/flows 与 /studio/v1/runs 读到的编排，以及
 * common/flowdef 的定义形状。解析器遇到无法识别的形状时抛出 ContractError。
 */

export type ValueType = "text" | "number" | "boolean";
/** InputType is a run-time input's type: the value types, and image and file,
 * whose value is an uploaded file and which have no default.
 * InputType 是运行时输入的类型：取值类型，加上 image 与 file——其取值是上传的文件，且没有
 * 默认值。 */
export type InputType = ValueType | "image" | "file";
export type NodeType = "input" | "llm" | "template" | "condition" | "image_generation" | "comfyui_template" | "video_generation" | "output";
export type Branch = "true" | "false";
export type Operator =
  | "equals"
  | "not_equals"
  | "contains"
  | "not_contains"
  | "gt"
  | "gte"
  | "lt"
  | "lte"
  | "empty"
  | "not_empty";

export interface FlowInput {
  name: string;
  label?: string;
  type: InputType;
  required: boolean;
  default?: string | number | boolean;
}

/** isFileInput tells whether an input takes a file. / isFileInput 判断输入是否接收文件。 */
export function isFileInput(input: Pick<FlowInput, "type">): boolean {
  return input.type === "image" || input.type === "file";
}

/** FileValue is what a run records for an image or file input: the artifact
 * the Gateway stored the file as, and the name it was uploaded under.
 * FileValue 是运行为 image 或 file 输入记录的取值：Gateway 存放该文件所用的产物 id，以及
 * 上传时的文件名。 */
export interface FileValue {
  artifactId: string;
  filename: string;
}

export interface FlowNode {
  id: string;
  type: NodeType;
  label?: string;
  config?: Record<string, unknown>;
  position: { x: number; y: number };
}

export interface FlowEdge {
  from: string;
  to: string;
  branch?: Branch;
}

export interface FlowOutput {
  name: string;
  ref: string;
}

export interface FlowDefinition {
  inputs: FlowInput[];
  nodes: FlowNode[];
  edges: FlowEdge[];
  outputs: FlowOutput[];
}

export interface JSONField {
  name: string;
  type: ValueType;
  description?: string;
}

export interface LLMConfig {
  model: string;
  system?: string;
  prompt: string;
  temperature?: number;
  max_tokens?: number;
  json_fields?: JSONField[];
}

export interface TemplateConfig {
  template: string;
}

export interface ConditionConfig {
  left: string;
  op: Operator;
  right?: string;
}

/** ImageConfig is an image_generation node's config. An empty model means
 * the deployment's default image generation.
 * ImageConfig 是 image_generation 节点的配置。model 为空表示使用部署默认的图像生成。 */
export interface ImageConfig {
  model: string;
  prompt: string;
  size?: string;
}

/** ComfyUIConfig is a comfyui_template node's config: the workflow template
 * id and the template of each input the node sets; inputs left out take the
 * workflow template's default.
 * ComfyUIConfig 是 comfyui_template 节点的配置：工作流模板 id，以及节点设置的每个输入的
 * 模板；未设置的输入取工作流模板自己的默认值。 */
export interface ComfyUIConfig {
  template: string;
  inputs: Record<string, string>;
}

/** VideoConfig is a video_generation node's config. firstFrame, lastFrame and
 * each reference image are a single reference to one image; the remaining
 * fields are left to the model's default when unset.
 * VideoConfig 是 video_generation 节点的配置。首帧、尾帧与每张参考图都是对单张图片的
 * 单个引用；其余字段未设置时取模型默认值。 */
export interface VideoConfig {
  model: string;
  prompt: string;
  first_frame?: string;
  last_frame?: string;
  reference_images?: string[];
  resolution?: string;
  ratio?: string;
  duration?: number;
  generate_audio?: boolean;
  watermark?: boolean;
}

/** VIDEO_RESOLUTIONS and VIDEO_RATIOS mirror flowdef's closed lists.
 * VIDEO_RESOLUTIONS 与 VIDEO_RATIOS 与 flowdef 的封闭列表一致。 */
export const VIDEO_RESOLUTIONS = ["480p", "720p", "1080p", "4k"] as const;
export const VIDEO_RATIOS = ["16:9", "4:3", "1:1", "3:4", "9:16", "21:9", "adaptive"] as const;
export const MAX_REFERENCE_IMAGES = 10;
export const MAX_VIDEO_DURATION = 3600;

export interface FlowSummary {
  id: string;
  name: string;
  version: number;
  updatedAt: string;
}

export interface Flow extends FlowSummary {
  definition: FlowDefinition;
}

export interface FlowPage {
  flows: FlowSummary[];
  next: string;
}

export type StepStatus = "succeeded" | "failed" | "canceled" | "skipped";
export type RunStatus = "running" | "succeeded" | "failed" | "canceled" | "interrupted";

export interface Usage {
  promptTokens: number;
  completionTokens: number;
}

export interface RunStep {
  nodeId: string;
  nodeType: string;
  status: StepStatus;
  errorCode: string;
  model: string;
  inputs: Record<string, string>;
  outputs: Record<string, string>;
  usage: Usage;
  startedAt: string;
  finishedAt: string;
}

export interface Run {
  id: string;
  flowId: string;
  flowVersion: number;
  status: RunStatus;
  errorCode: string;
  failedNode: string;
  inputs: Record<string, unknown>;
  outputs: Record<string, string>;
  usage: Usage;
  createdAt: string;
  finishedAt: string;
  steps: RunStep[];
}

export interface RunPage {
  runs: Run[];
  next: string;
}

/** FlowIssue is one validation problem, located on its node (empty for the
 * flow as a whole) and a field path inside it.
 *
 * FlowIssue 是一个校验问题，定位到所属节点（整个编排的问题为空）及其中的字段路径。 */
export interface FlowIssue {
  node: string;
  field: string;
  code: string;
}

const NODE_TYPES: NodeType[] = ["input", "llm", "template", "condition", "image_generation", "comfyui_template", "video_generation", "output"];
const INPUT_TYPES: InputType[] = ["text", "number", "boolean", "image", "file"];
const STEP_STATUSES: StepStatus[] = ["succeeded", "failed", "canceled", "skipped"];
const RUN_STATUSES: RunStatus[] = ["running", "succeeded", "failed", "canceled", "interrupted"];
const MAX_ISSUES = 50;

function fail(): never {
  throw new ContractError();
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) fail();
  return value as Record<string, unknown>;
}

function str(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (value === undefined || value === null) return "";
  if (typeof value !== "string") fail();
  return value;
}

function id(source: Record<string, unknown>, key: string): string {
  const value = str(source, key);
  if (value === "") fail();
  return value;
}

function num(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  if (value === undefined || value === null) return 0;
  if (typeof value !== "number" || !Number.isFinite(value)) fail();
  return value;
}

function list(value: unknown): unknown[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) fail();
  return value;
}

function oneOf<T extends string>(value: string, allowed: readonly T[]): T {
  if (!(allowed as readonly string[]).includes(value)) fail();
  return value as T;
}

function textMap(value: unknown): Record<string, string> {
  if (value === undefined || value === null) return {};
  const source = record(value);
  const out: Record<string, string> = {};
  for (const [key, entry] of Object.entries(source)) {
    if (typeof entry !== "string") fail();
    out[key] = entry;
  }
  return out;
}

function usage(value: unknown): Usage {
  if (value === undefined || value === null) return { promptTokens: 0, completionTokens: 0 };
  const source = record(value);
  return { promptTokens: num(source, "prompt_tokens"), completionTokens: num(source, "completion_tokens") };
}

/** parseDefinition reads a flow definition. It checks the shape the editor
 * relies on; whether the flow is valid is the Gateway's and control plane's
 * judgement, reported as issues.
 *
 * parseDefinition 读取编排定义。它只检查编辑器依赖的形状；编排是否有效由 Gateway 与
 * 控制面判断，并以问题列表报告。 */
export function parseDefinition(value: unknown): FlowDefinition {
  const source = record(value);
  const inputs = list(source.inputs).map((item): FlowInput => {
    const input = record(item);
    const out: FlowInput = { name: str(input, "name"), type: oneOf(str(input, "type"), INPUT_TYPES), required: input.required === true };
    if (typeof input.label === "string" && input.label !== "") out.label = input.label;
    const fallback = input.default;
    if (!isFileInput(out) && (typeof fallback === "string" || typeof fallback === "number" || typeof fallback === "boolean")) out.default = fallback;
    return out;
  });
  const nodes = list(source.nodes).map((item): FlowNode => {
    const node = record(item);
    const position = node.position === undefined || node.position === null ? {} : record(node.position);
    const out: FlowNode = {
      id: id(node, "id"),
      type: oneOf(str(node, "type"), NODE_TYPES),
      position: { x: num(position, "x"), y: num(position, "y") },
    };
    if (typeof node.label === "string" && node.label !== "") out.label = node.label;
    if (node.config !== undefined && node.config !== null) out.config = record(node.config);
    return out;
  });
  const edges = list(source.edges).map((item): FlowEdge => {
    const edge = record(item);
    const out: FlowEdge = { from: id(edge, "from"), to: id(edge, "to") };
    const branch = str(edge, "branch");
    if (branch !== "") out.branch = oneOf(branch, ["true", "false"] as const);
    return out;
  });
  const outputs = list(source.outputs).map((item): FlowOutput => {
    const output = record(item);
    return { name: str(output, "name"), ref: str(output, "ref") };
  });
  return { inputs, nodes, edges, outputs };
}

function parseSummary(value: unknown): FlowSummary {
  const source = record(value);
  return { id: id(source, "id"), name: str(source, "name"), version: num(source, "version"), updatedAt: str(source, "updated_at") };
}

/** parseFlow reads GET /studio/v1/flows/{id}. / parseFlow 读取单个编排。 */
export function parseFlow(value: unknown): Flow {
  const source = record(value);
  return { ...parseSummary(source), definition: parseDefinition(source.definition) };
}

/** parseFlowPage reads GET /studio/v1/flows. / parseFlowPage 读取编排列表。 */
export function parseFlowPage(value: unknown): FlowPage {
  const source = record(value);
  return { flows: list(source.data).map(parseSummary), next: str(source, "next") };
}

/** parseFlowVersion reads GET /studio/v1/flows/{id}/versions/{version}.
 * parseFlowVersion 读取编排的某个版本。 */
export function parseFlowVersion(value: unknown): { version: number; definition: FlowDefinition } {
  const source = record(value);
  return { version: num(source, "version"), definition: parseDefinition(source.definition) };
}

/** parseRunStep reads one step, from a run or a node_finished frame.
 * parseRunStep 读取一个步骤，来自运行或 node_finished 帧。 */
export function parseRunStep(value: unknown): RunStep {
  const source = record(value);
  return {
    nodeId: id(source, "node_id"),
    nodeType: str(source, "node_type"),
    status: oneOf(str(source, "status"), STEP_STATUSES),
    errorCode: str(source, "error_code"),
    model: str(source, "model"),
    inputs: textMap(source.inputs),
    outputs: textMap(source.outputs),
    usage: usage(source.usage),
    startedAt: str(source, "started_at"),
    finishedAt: str(source, "finished_at"),
  };
}

/** parseRun reads a run. / parseRun 读取一次运行。 */
export function parseRun(value: unknown): Run {
  const source = record(value);
  const inputs = source.inputs === undefined || source.inputs === null ? {} : record(source.inputs);
  return {
    id: id(source, "id"),
    flowId: str(source, "flow_id"),
    flowVersion: num(source, "flow_version"),
    status: oneOf(str(source, "status"), RUN_STATUSES),
    errorCode: str(source, "error_code"),
    failedNode: str(source, "failed_node"),
    inputs,
    outputs: textMap(source.outputs),
    usage: usage(source.usage),
    createdAt: str(source, "created_at"),
    finishedAt: str(source, "finished_at"),
    steps: list(source.steps).map(parseRunStep),
  };
}

/** parseRunPage reads GET /studio/v1/flows/{id}/runs. / parseRunPage 读取运行记录。 */
export function parseRunPage(value: unknown): RunPage {
  const source = record(value);
  return { runs: list(source.data).map(parseRun), next: str(source, "next") };
}

/** parseIssues reads the issues of a 400 answer. Anything malformed is
 * dropped rather than shown, and the list is bounded.
 *
 * parseIssues 读取 400 应答中的问题列表。格式不对的条目直接丢弃而不展示，列表有上限。 */
export function parseIssues(value: unknown): FlowIssue[] {
  if (typeof value !== "object" || value === null) return [];
  const raw = (value as Record<string, unknown>).issues;
  if (!Array.isArray(raw)) return [];
  const out: FlowIssue[] = [];
  for (const item of raw.slice(0, MAX_ISSUES)) {
    if (typeof item !== "object" || item === null) continue;
    const issue = item as Record<string, unknown>;
    if (typeof issue.code !== "string" || issue.code === "") continue;
    out.push({
      node: typeof issue.node === "string" ? issue.node : "",
      field: typeof issue.field === "string" ? issue.field : "",
      code: issue.code,
    });
  }
  return out;
}

const ISSUE_TEXT: Record<string, string> = {
  too_large: "编排太大，请删减内容。",
  invalid_json: "编排数据格式不正确。",
  too_many: "数量超过了上限。",
  invalid_name: "名称只能由字母、数字和下划线组成，且不能以数字开头。",
  duplicate_name: "名称重复了。",
  invalid_label: "显示名称太长。",
  invalid_type: "类型不受支持。",
  invalid_default: "默认值与类型不符；图片和文件输入不能设默认值。",
  invalid_config: "配置不正确。",
  required: "这一项必填。",
  out_of_range: "数值超出了允许范围。",
  invalid_template: "模板写法有误：不支持嵌套字段，或内容太长。",
  unknown_reference: "引用了不存在的变量。",
  reference_not_upstream: "只能引用上游节点的结果。",
  missing_node: "编排需要恰好一个输入节点和一个输出节点。",
  multiple_nodes: "输入节点和输出节点只能各有一个。",
  invalid_edge: "这条连线无效。",
  cycle: "连线形成了环。",
  unreachable: "这个节点没有从输入节点连过来。",
  unknown_input: "提交了未声明的输入，或多出了没有对应输入的文件。",
  invalid_value: "输入值与类型不符；文件输入不能同时上传新文件又沿用上次的文件。",
  unsupported_file_type: "文件类型不受支持，或文件内容与扩展名不符；图片输入只接受 png、jpg、jpeg、webp、gif、bmp。",
  not_found: "上次运行的这个文件已不存在或已过保留期，请重新选择文件。",
  artifact_storage_unavailable: "部署没有配置文件存储，暂时不能上传文件。",
  model_unavailable: "所选模型当前不可用，请换一个。",
  artifact_reference: "图片或文件只能整个作为结果字段的取值，或整个填入 ComfyUI 模板的文件型输入（ComfyUI 的 artifacts 不行），不能放进提示词、模板、条件，也不能与其他文字混写。",
  template_unavailable: "所选工作流模板当前不可用（未发布或对你不可见），请换一个。",
  image_generation_unavailable: "部署没有默认的图像生成，请选择一个图像模型。",
  invalid_input: "这个输入的值不符合模板的要求：文本输入不能绑定文件、文件输入只能选文件来源，或模板没有这个输入。",
  image_reference_required: "这里只能选一张图片：图片输入，或上游节点生成的图片、尾帧。",
  video_generation_unavailable: "还没有可用的火山方舟 Key：请租户管理员在控制台配置，或联系运维配置平台 Key。",
};

const FIELD_TEXT: Record<string, string> = {
  "config.model": "模型",
  "config.prompt": "提示词",
  "config.system": "系统提示词",
  "config.temperature": "温度",
  "config.max_tokens": "最大输出长度",
  "config.json_fields": "结构化字段",
  "config.template": "模板",
  "config.left": "左侧",
  "config.op": "比较方式",
  "config.right": "右侧",
  "config.size": "尺寸",
  "config.inputs": "模板输入",
  "config.first_frame": "首帧",
  "config.last_frame": "尾帧",
  "config.reference_images": "参考图",
  "config.resolution": "分辨率",
  "config.ratio": "画面比例",
  "config.duration": "时长",
  config: "配置",
  edges: "连线",
  label: "显示名称",
  id: "节点 ID",
  type: "节点类型",
  nodes: "节点",
};

/** issueText is the Chinese wording of an issue, naming the field it is about.
 * issueText 是一个问题的中文表述，并指明所涉及的字段。 */
export function issueText(issue: FlowIssue): string {
  const message = ISSUE_TEXT[issue.code] ?? "这里有问题，请检查。";
  const field = FIELD_TEXT[issue.field] ?? fieldPath(issue.field);
  return field ? `${field}：${message}` : message;
}

function fieldPath(field: string): string {
  const input = /^inputs\.([A-Za-z_][A-Za-z0-9_]*)/.exec(field);
  if (input) return `输入「${input[1]}」`;
  const output = /^outputs\.([A-Za-z_][A-Za-z0-9_]*)/.exec(field);
  if (output) return `输出「${output[1]}」`;
  const templateInput = /^config\.inputs\.(.+)$/.exec(field);
  if (templateInput) return `模板输入「${templateInput[1]}」`;
  const reference = /^config\.reference_images\[(\d+)\]$/.exec(field);
  if (reference) return `参考图 ${Number(reference[1]) + 1}`;
  if (field.startsWith("inputs")) return "输入";
  if (field.startsWith("outputs")) return "输出";
  if (field.startsWith("config.json_fields")) return "结构化字段";
  return "";
}

const RUN_STATUS_TEXT: Record<string, string> = {
  running: "运行中",
  succeeded: "成功",
  failed: "失败",
  canceled: "已取消",
  interrupted: "已中断",
  skipped: "已跳过",
};

/** statusText is the Chinese label of a run or step status.
 * statusText 是运行或步骤状态的中文标签。 */
export function statusText(status: string): string {
  return RUN_STATUS_TEXT[status] ?? status;
}

const RUN_ERROR_TEXT: Record<string, string> = {
  node_timeout: "节点超时。",
  run_timeout: "运行超过了总时限。",
  invalid_json_output: "模型没有按要求返回 JSON。",
  not_a_number: "条件比较的值不是数字。",
  value_too_large: "内容超过了长度上限。",
  model_unavailable: "模型当前不可用。",
  rate_limited: "请求太频繁，请稍后重试。",
  timeout: "模型响应超时。",
  invalid_request: "模型无法处理这次请求。",
  insufficient_balance: "余额不足，请联系管理员充值。",
  account_subscription_expired: "账号包月已到期，请联系管理员续费。",
  upstream_error: "模型返回了错误。",
  no_image_produced: "没有生成图片。",
  image_generation_unavailable: "部署没有默认的图像生成，请在图像生成节点里选择一个图像模型。",
  template_unavailable: "工作流模板当前不可用（未发布或对你不可见）。",
  invalid_input: "填入的值不符合模板输入的要求：类型或范围不对、文本输入绑了文件、文件输入绑了文本、必填的文件没有选，或模板没有这个输入。",
  // Older runs may still carry this code; the Gateway no longer produces it.
  // 历史运行里可能还有这个码；Gateway 已不再产生。
  unsupported_input: "模板需要文件输入，编排暂时无法提供。",
  not_found: "这个节点用到的图片或文件已不存在，可能超过了保留期被清理。请重新运行并选择文件。",
  no_capable_node: "当前没有能运行这个工作流的节点。",
  queue_timeout: "排队等待运行的时间太久，已放弃。",
  generation_failed: "生成失败。",
  out_of_memory: "生成时显存不足，请换更小的尺寸或参数。",
  job_cancelled: "生成任务被取消了。",
  artifact_storage_unavailable: "产物存储暂不可用，请稍后重试。",
  no_video_produced: "没有生成视频。",
  video_generation_unavailable: "没有可用的火山方舟 Key，或 Key 不能使用这个模型。",
  video_generation_failed: "视频生成失败。",
  content_rejected: "提示词、图片或生成的视频没有通过内容审核。",
  video_task_expired: "视频任务过期了，或生成的视频已无法下载。",
  image_missing: "首帧、尾帧或参考图引用的图片不存在（上游节点没有产出，或已过保留期）。",
};

/** runErrorText explains why a run or a node failed.
 * runErrorText 说明运行或节点失败的原因。 */
export function runErrorText(code: string): string {
  return RUN_ERROR_TEXT[code] ?? "出了点问题。";
}

/** flowErrorText is the wording of a refused flow or run request.
 * flowErrorText 是编排或运行请求被拒绝时的文案。 */
export function flowErrorText(code: string, fallback: (code: string) => string): string {
  switch (code) {
    case "not_found":
      return "编排或运行不存在，可能已被删除。";
    case "limit_reached":
      return "编排数或版本数已达上限，请删除不需要的编排。";
    case "flow_changed":
      return "编排已在别处保存，请刷新后再改。";
    case "run_limit":
      return "同时进行的运行太多，请等其中一个结束。";
    case "run_elsewhere":
      return "这次运行在另一台服务器上执行，暂时无法在这里停止。";
    case "run_finished":
      return "这次运行已经结束。";
    case "invalid_flow":
      return "编排有错误，请按提示修改。";
    case "invalid_inputs":
      return "输入值有误，请按提示修改。";
    case "storage_unavailable":
      return "编排或上传文件的存储暂不可用，请稍后重试。";
    case "too_large":
    case "body_too_large":
      return "内容太大，超过了上限：编排请删减内容，运行输入请换小一点的文件（整个请求最大 200 MiB）。";
    case "unavailable":
      return "服务器正在重启，请稍后重试。";
    default:
      return fallback(code);
  }
}

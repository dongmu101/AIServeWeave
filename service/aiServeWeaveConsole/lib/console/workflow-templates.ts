import { ApiError, describe } from "./errors.ts";

/** MAX_GRAPH_BYTES bounds one template's imported ComfyUI graph, mirroring common/workflowtemplate.MaxGraphBytes.
 * MAX_GRAPH_BYTES 限制单个模板导入的 ComfyUI 图，与 common/workflowtemplate.MaxGraphBytes 对齐。 */
export const MAX_GRAPH_BYTES = 4 << 20;
/** MAX_CONTENT_BYTES allows bounded metadata around the graph, mirroring common/workflowtemplate.MaxContentBytes.
 * MAX_CONTENT_BYTES 为图之外的元数据预留有界空间，与 common/workflowtemplate.MaxContentBytes 对齐。 */
export const MAX_CONTENT_BYTES = MAX_GRAPH_BYTES + 256 * 1024;
const MAX_INPUTS = 100;
const MAX_OUTPUTS = 20;
const MAX_CUSTOM_NODE_DEPS = 100;
const MAX_MODEL_DEPS = 100;
const MAX_VISIBLE_TENANTS = 1000;

/** TemplateInput mirrors common/workflowtemplate.Input. / TemplateInput 与 common/workflowtemplate.Input 对齐。 */
export interface TemplateInput {
  name: string;
  node: string;
  field: string;
  type: "string" | "integer" | "number" | "boolean";
  required: boolean;
  default?: unknown;
  max_length?: number;
  min?: number;
  max?: number;
}
/** TemplateOutput mirrors common/workflowtemplate.Output. / TemplateOutput 与 common/workflowtemplate.Output 对齐。 */
export interface TemplateOutput { name: string; node: string; type: string; description?: string }
/** NodeDependency and ModelDependency mirror their Go namesakes. / NodeDependency 与 ModelDependency 与其 Go 同名类型对齐。 */
export interface NodeDependency { name: string; version?: string }
export interface ModelDependency { name: string; version?: string }
/** Dependencies mirrors common/workflowtemplate.Dependencies. / Dependencies 与 common/workflowtemplate.Dependencies 对齐。 */
export interface Dependencies { custom_nodes?: NodeDependency[]; models?: ModelDependency[] }
/** TemplateContent is one editable revision's payload, mirroring common/workflowtemplate.Content.
 * TemplateContent 是一个可编辑版本的载荷，与 common/workflowtemplate.Content 对齐。 */
export interface TemplateContent {
  description?: string;
  inputs: TemplateInput[];
  outputs?: TemplateOutput[];
  dependencies?: Dependencies;
  graph: unknown;
}
/** TemplateRevisionInfo mirrors common/workflowtemplate.RevisionInfo. / TemplateRevisionInfo 与 common/workflowtemplate.RevisionInfo 对齐。 */
export interface TemplateRevisionInfo {
  template_id: string;
  revision: number;
  digest: string;
  created_at: string;
  actor_id: string;
  rollback_of?: number;
}
/** TemplateSnapshot is a full published revision, graph included. / TemplateSnapshot 是一个含图的完整已发布版本。 */
export interface TemplateSnapshot extends TemplateRevisionInfo, TemplateContent { visible_tenant_ids?: string[] }
/** TemplateSummary is one catalogue row, graph-free. / TemplateSummary 是目录中一行，不含图。 */
export interface TemplateSummary extends TemplateRevisionInfo {
  description?: string;
  inputs: TemplateInput[];
  outputs?: TemplateOutput[];
  dependencies?: Dependencies;
  visible_tenant_ids?: string[];
}
/** TemplateHistory is a bounded revision page. / TemplateHistory 是有界版本页。 */
export interface TemplateHistory { items: TemplateRevisionInfo[]; next_before?: number }
/** TemplateReplica records one live observation of the bundle (P03), mirroring workflowtemplate.ReplicaStatus.
 * TemplateReplica 记录整包的一次实时观测（P03），与 workflowtemplate.ReplicaStatus 对齐。 */
export interface TemplateReplica {
  endpoint: string;
  replica_id?: string;
  mode?: string;
  template_count?: number;
  bundle_digest?: string;
  applied_at?: string;
  checked_at?: string;
  generated_at?: string;
  error?: string;
}
/** TemplateStatus compares the desired bundle with live replicas. / TemplateStatus 比较期望整包与实时副本。 */
export interface TemplateStatus {
  desired_template_count: number;
  desired_bundle_digest: string;
  checked_at: string;
  complete: boolean;
  replicas: TemplateReplica[];
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}
function string(value: unknown): string { if (typeof value !== "string") throw new ApiError("contract"); return value; }
function integer(value: unknown, min = 0): number { if (typeof value !== "number" || !Number.isSafeInteger(value) || value < min) throw new ApiError("contract"); return value; }
function known(value: Record<string, unknown>, keys: string[]) { if (Object.keys(value).some((key) => !keys.includes(key))) throw new ApiError("invalid"); }
function byteLength(value: unknown): number { return new TextEncoder().encode(JSON.stringify(value)).length; }

function parseInput(item: unknown): TemplateInput {
  const source = object(item);
  known(source, ["name", "node", "field", "type", "required", "default", "max_length", "min", "max"]);
  const name = string(source.name), node = string(source.node), field = string(source.field), type = string(source.type);
  if (!name.trim() || !node.trim() || !field.trim() || !["string", "integer", "number", "boolean"].includes(type)) throw new ApiError("invalid");
  const result: TemplateInput = { name, node, field, type: type as TemplateInput["type"], required: source.required === true };
  if (source.default !== undefined) result.default = source.default;
  if (source.max_length !== undefined) result.max_length = integer(source.max_length);
  if (source.min !== undefined) { if (typeof source.min !== "number") throw new ApiError("invalid"); result.min = source.min; }
  if (source.max !== undefined) { if (typeof source.max !== "number") throw new ApiError("invalid"); result.max = source.max; }
  return result;
}
function parseOutput(item: unknown): TemplateOutput {
  const source = object(item);
  known(source, ["name", "node", "type", "description"]);
  const name = string(source.name), node = string(source.node), type = string(source.type);
  if (!name.trim() || !node.trim() || !type.trim()) throw new ApiError("invalid");
  const result: TemplateOutput = { name, node, type };
  if (source.description !== undefined) result.description = string(source.description);
  return result;
}
function parseDependencyList<T extends { name: string; version?: string }>(value: unknown, limit: number): T[] {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > limit) throw new ApiError("invalid");
  const seen = new Set<string>();
  return value.map((item) => {
    const source = object(item);
    known(source, ["name", "version"]);
    const name = string(source.name);
    if (!name.trim() || seen.has(name)) throw new ApiError("invalid");
    seen.add(name);
    const result = { name } as T;
    if (source.version !== undefined) (result as { version?: string }).version = string(source.version);
    return result;
  });
}
function parseDependencies(value: unknown): Dependencies {
  if (value === undefined) return {};
  const source = object(value);
  known(source, ["custom_nodes", "models"]);
  const result: Dependencies = {};
  const customNodes = parseDependencyList<NodeDependency>(source.custom_nodes, MAX_CUSTOM_NODE_DEPS);
  const models = parseDependencyList<ModelDependency>(source.models, MAX_MODEL_DEPS);
  if (customNodes.length) result.custom_nodes = customNodes;
  if (models.length) result.models = models;
  return result;
}

/** parseTemplateContent validates and copies an editable revision's payload.
 *
 * The graph itself is treated as opaque here: this module never re-derives
 * ComfyUI's node/field consistency rules, which live once in
 * common/workflowtemplate and are enforced by the control plane's own
 * /validate and /publish endpoints — the same division upstream-routes.ts
 * documents for why forwarding is table-driven rather than proxied.
 *
 * parseTemplateContent 校验并复制一份可编辑版本的载荷。
 *
 * 图本身在这里被当作不透明的东西：本模块从不重新推导 ComfyUI 的节点/字段一致性
 * 规则，那套规则只活在 common/workflowtemplate 一处，由控制面自己的 /validate 与
 * /publish 端点执行——与 upstream-routes.ts 里说明「为什么转发是表驱动而不是代理」
 * 的是同一种分工。
 */
export function parseTemplateContent(value: unknown): TemplateContent {
  const source = object(value);
  known(source, ["description", "inputs", "outputs", "dependencies", "graph"]);
  if (source.graph === undefined || source.graph === null) throw new ApiError("invalid");
  if (!Array.isArray(source.inputs) || source.inputs.length > MAX_INPUTS) throw new ApiError("invalid");
  const outputsRaw = source.outputs === undefined ? [] : source.outputs;
  if (!Array.isArray(outputsRaw) || outputsRaw.length > MAX_OUTPUTS) throw new ApiError("invalid");
  const result: TemplateContent = { inputs: source.inputs.map(parseInput), graph: source.graph };
  const outputs = outputsRaw.map(parseOutput);
  if (outputs.length) result.outputs = outputs;
  const dependencies = parseDependencies(source.dependencies);
  if (dependencies.custom_nodes || dependencies.models) result.dependencies = dependencies;
  if (source.description !== undefined) result.description = string(source.description);
  if (byteLength(result) > MAX_CONTENT_BYTES) throw new ApiError("invalid");
  return result;
}
/** parseVisibleTenantIDs validates a template's tenant allow list. Empty means every tenant may see and run it.
 * parseVisibleTenantIDs 校验一个模板的租户允许列表。空列表意味着所有租户都能看到并运行它。 */
export function parseVisibleTenantIDs(value: unknown): string[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value) || value.length > MAX_VISIBLE_TENANTS) throw new ApiError("invalid");
  return value.map((item) => { const id = string(item); if (!id.trim()) throw new ApiError("invalid"); return id; });
}
function revisionInfo(value: unknown): TemplateRevisionInfo {
  const source = object(value);
  const result: TemplateRevisionInfo = {
    template_id: string(source.template_id),
    revision: integer(source.revision),
    digest: string(source.digest),
    created_at: string(source.created_at),
    actor_id: string(source.actor_id),
  };
  if (source.rollback_of !== undefined) result.rollback_of = integer(source.rollback_of, 1);
  return result;
}
/** parseTemplateSnapshot validates a full publication response, graph included.
 * parseTemplateSnapshot 校验一份含图的完整发布响应。 */
export function parseTemplateSnapshot(value: unknown): TemplateSnapshot {
  const source = object(value);
  // parseTemplateContent rejects any key outside its own five, so it is
  // handed a content-only view rather than the whole envelope — the
  // revision fields (template_id, revision, digest, created_at, actor_id)
  // live alongside content in the wire response but are not part of it.
  //
  // parseTemplateContent 会拒绝除自己那五个键之外的任何键，因此这里递给它的是
  // 一份只含内容的视图，而不是整个信封——版本字段（template_id、revision、
  // digest、created_at、actor_id）在响应里与内容并列，但不属于内容本身。
  const content = parseTemplateContent({ description: source.description, inputs: source.inputs, outputs: source.outputs, dependencies: source.dependencies, graph: source.graph });
  return { ...revisionInfo(source), ...content, visible_tenant_ids: parseVisibleTenantIDs(source.visible_tenant_ids) };
}
/** parseTemplateSummaries validates the graph-free catalogue listing.
 * parseTemplateSummaries 校验不含图的目录列表。 */
export function parseTemplateSummaries(value: unknown): TemplateSummary[] {
  const source = object(value);
  if (!Array.isArray(source.items)) throw new ApiError("contract");
  return source.items.map((item) => {
    const row = object(item);
    known(row, ["template_id", "revision", "digest", "created_at", "actor_id", "rollback_of", "description", "inputs", "outputs", "dependencies", "visible_tenant_ids"]);
    const result: TemplateSummary = { ...revisionInfo(row), inputs: Array.isArray(row.inputs) ? row.inputs.map(parseInput) : [] };
    if (row.description !== undefined) result.description = string(row.description);
    if (Array.isArray(row.outputs) && row.outputs.length) result.outputs = row.outputs.map(parseOutput);
    const dependencies = parseDependencies(row.dependencies);
    if (dependencies.custom_nodes || dependencies.models) result.dependencies = dependencies;
    const visible = parseVisibleTenantIDs(row.visible_tenant_ids);
    if (visible.length) result.visible_tenant_ids = visible;
    return result;
  });
}
/** parseTemplateHistory validates bounded history pages. / parseTemplateHistory 校验有界历史页。 */
export function parseTemplateHistory(value: unknown): TemplateHistory {
  const source = object(value);
  if (!Array.isArray(source.items) || source.items.length > 50) throw new ApiError("contract");
  return { items: source.items.map(revisionInfo), ...(source.next_before === undefined ? {} : { next_before: integer(source.next_before, 1) }) };
}
/** parseTemplateValidation validates the server verdict. / parseTemplateValidation 校验服务端校验结论。 */
export function parseTemplateValidation(value: unknown): string {
  const source = object(value);
  if (source.valid !== true) throw new ApiError("contract");
  return string(source.digest);
}
/** parseTemplateStatus never trusts a complete flag without matching observations, the same discipline parseRouteStatus applies.
 * parseTemplateStatus 不在缺少匹配观测时相信完成标志，与 parseRouteStatus 相同的克制。 */
export function parseTemplateStatus(value: unknown): TemplateStatus {
  const source = object(value);
  if (!Array.isArray(source.replicas) || typeof source.complete !== "boolean") throw new ApiError("contract");
  const desired_template_count = integer(source.desired_template_count), desired_bundle_digest = string(source.desired_bundle_digest);
  const replicas = source.replicas.map((item) => {
    const row = object(item);
    const result: TemplateReplica = { endpoint: string(row.endpoint) };
    for (const key of ["replica_id", "mode", "bundle_digest", "applied_at", "checked_at", "generated_at", "error"] as const) if (row[key] !== undefined) result[key] = string(row[key]);
    if (row.template_count !== undefined) result.template_count = integer(row.template_count);
    return result;
  });
  return {
    desired_template_count,
    desired_bundle_digest,
    checked_at: string(source.checked_at),
    replicas,
    complete: source.complete && replicas.length > 0 && replicas.every((row) => !row.error && row.mode === "controlplane" && row.template_count === desired_template_count && row.bundle_digest === desired_bundle_digest),
  };
}
/** importTemplateManifestFile bounds both declared size and streamed bytes before parsing, the same discipline importRouteFile applies.
 *
 * The accepted shape is a superset of the existing Gateway file-mode
 * manifest ({description, inputs, graph}, see workflow.Template's own JSON
 * tags): an operator migrating a file-sourced template into the control
 * plane can import the exact file already on disk, then add outputs,
 * dependencies and visibility afterward in the form.
 *
 * importTemplateManifestFile 在解析前限制声明大小与流式字节数，与 importRouteFile
 * 相同的克制。
 *
 * 接受的形状是现有 Gateway 文件模式清单（{description, inputs, graph}，见
 * workflow.Template 自身的 JSON 标签）的超集：一个把文件来源的模板迁移进控制面的
 * 运维，可以直接导入磁盘上现成的那份文件，再在表单里补上输出、依赖与可见范围。
 */
export async function importTemplateManifestFile(file: Pick<Blob, "size" | "stream">): Promise<TemplateContent> {
  if (file.size > MAX_CONTENT_BYTES) throw new ApiError("invalid");
  const reader = file.stream().getReader(); const decoder = new TextDecoder(); let size = 0, text = "";
  try {
    for (;;) {
      const { value, done } = await reader.read(); if (done) break;
      size += value.byteLength;
      if (size > MAX_CONTENT_BYTES) { await reader.cancel(); throw new ApiError("invalid"); }
      text += decoder.decode(value, { stream: true });
    }
    const value: unknown = JSON.parse(text + decoder.decode());
    // An existing file-mode manifest carries its own "id", which this parser
    // otherwise rejects as unknown: the template id comes from the operator
    // page's own selection, not from re-reading a field a stale export
    // happens to still carry.
    //
    // 一份现存的文件模式清单自带一个"id"，本解析器原本会把它当作未知字段拒绝：
    // 模板 id 来自运维页面自己的选择，而不是重新读取一次旧导出文件碰巧还带着
    // 的字段。
    if (value && typeof value === "object" && !Array.isArray(value) && "id" in value) {
      const rest = { ...(value as Record<string, unknown>) };
      delete rest.id;
      return parseTemplateContent(rest);
    }
    return parseTemplateContent(value);
  } catch { throw new ApiError("invalid"); } finally { reader.releaseLock(); }
}
/** templateWriteMessage explains conflicts without discarding the draft, the same wording routeWriteMessage uses.
 * templateWriteMessage 解释冲突且不丢弃草稿，与 routeWriteMessage 相同的措辞。 */
export function templateWriteMessage(error: unknown): string {
  return error instanceof ApiError && error.kind === "conflict" ? "当前版本已变化，草稿已保留。请重新读取当前版本，比较后再次确认发布。" : `${describe(error)} 草稿已保留；请刷新当前版本核对结果。`;
}
/** templateBodyLimit raises the bound only for matched bundle-writing paths, the same scoping routeBodyLimit applies.
 * templateBodyLimit 仅对已匹配的整包写入路径提高上限，与 routeBodyLimit 相同的范围收紧。 */
export function templateBodyLimit(path: string): number | undefined {
  return /^\/operator\/v1\/workflow-templates\/[^/]+\/(validate|publish)$/.test(path) ? MAX_CONTENT_BYTES : undefined;
}

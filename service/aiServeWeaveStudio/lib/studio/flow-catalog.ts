import { ContractError, parseModels, type StudioModel } from "./contract.ts";

/**
 * What the flow editor may pick from besides chat models: the image models
 * (GET /studio/v1/image-models) and the published ComfyUI workflow templates
 * visible to the tenant (GET /studio/v1/workflow-templates). The parsers
 * throw ContractError on a shape they do not know.
 *
 * 编排编辑器除对话模型外可以选择的东西：图像模型（GET /studio/v1/image-models）与本租户
 * 可见的已发布 ComfyUI 工作流模板（GET /studio/v1/workflow-templates）。解析器遇到无法
 * 识别的形状时抛出 ContractError。
 */

export interface ImageModelCatalog {
  models: StudioModel[];
  /** an image node with no model can run: the deployment has its own image
   * generation
   * 不指定模型的图像生成节点可以运行：部署有自己的图像生成 */
  defaultAvailable: boolean;
}

/** VideoModelCatalog is GET /studio/v1/video-models: whether the tenant has
 * a Volcengine Ark key (its own or the platform's), and the models to pick
 * from. freeForm means the tenant's own key, with which any model id may be
 * typed.
 * VideoModelCatalog 是 GET /studio/v1/video-models：租户是否有火山方舟 Key（自己的或平台的），
 * 以及可选的模型。freeForm 表示使用租户自己的 Key，此时可以填写任意模型 id。 */
export interface VideoModelCatalog {
  models: StudioModel[];
  available: boolean;
  source: "tenant" | "platform" | "";
  freeForm: boolean;
}

export type TemplateInputType = "string" | "integer" | "number" | "boolean" | "file";

export interface TemplateInput {
  name: string;
  type: TemplateInputType;
  required: boolean;
  default?: string | number | boolean;
  maxLength?: number;
  min?: number;
  max?: number;
}

export interface TemplateOutput {
  name: string;
  type: string;
  description: string;
}

export interface WorkflowTemplate {
  id: string;
  description: string;
  inputs: TemplateInput[];
  outputs: TemplateOutput[];
}

/** FlowCatalog is what the editor knows of the above; a part that failed to
 * load is null, and nothing is flagged unavailable against it.
 *
 * FlowCatalog 是编辑器对上述内容的了解；加载失败的部分为 null，不据此把任何东西标为
 * 不可用。 */
export interface FlowCatalog {
  imageModels: ImageModelCatalog | null;
  templates: WorkflowTemplate[] | null;
  videoModels: VideoModelCatalog | null;
}

const INPUT_TYPES: TemplateInputType[] = ["string", "integer", "number", "boolean", "file"];

function fail(): never {
  throw new ContractError();
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) fail();
  return value as Record<string, unknown>;
}

function text(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (value === undefined || value === null) return "";
  if (typeof value !== "string") fail();
  return value;
}

function optionalNumber(source: Record<string, unknown>, key: string): number | undefined {
  const value = source[key];
  if (value === undefined || value === null) return undefined;
  if (typeof value !== "number" || !Number.isFinite(value)) fail();
  return value;
}

function list(value: unknown): unknown[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) fail();
  return value;
}

/** parseImageModels reads GET /studio/v1/image-models.
 * parseImageModels 读取 GET /studio/v1/image-models。 */
export function parseImageModels(value: unknown): ImageModelCatalog {
  const source = record(value);
  const flag = source.default_available;
  if (flag !== undefined && flag !== null && typeof flag !== "boolean") fail();
  return { models: parseModels(source), defaultAvailable: flag === true };
}

/** parseVideoModels reads GET /studio/v1/video-models.
 * parseVideoModels 读取 GET /studio/v1/video-models。 */
export function parseVideoModels(value: unknown): VideoModelCatalog {
  const source = record(value);
  for (const key of ["available", "free_form"]) {
    const flag = source[key];
    if (flag !== undefined && flag !== null && typeof flag !== "boolean") fail();
  }
  const kind = text(source, "source");
  if (kind !== "" && kind !== "tenant" && kind !== "platform") fail();
  return { models: parseModels(source), available: source.available === true, source: kind, freeForm: source.free_form === true };
}

function parseInput(value: unknown): TemplateInput {
  const source = record(value);
  const name = text(source, "name");
  if (name === "") fail();
  const type = text(source, "type");
  if (!(INPUT_TYPES as string[]).includes(type)) fail();
  const out: TemplateInput = { name, type: type as TemplateInputType, required: source.required === true };
  const fallback = source.default;
  if (typeof fallback === "string" || typeof fallback === "number" || typeof fallback === "boolean") out.default = fallback;
  const maxLength = optionalNumber(source, "max_length");
  if (maxLength !== undefined) out.maxLength = maxLength;
  const min = optionalNumber(source, "min");
  if (min !== undefined) out.min = min;
  const max = optionalNumber(source, "max");
  if (max !== undefined) out.max = max;
  return out;
}

/** parseWorkflowTemplates reads GET /studio/v1/workflow-templates. Duplicate
 * ids keep the first, so a picker never shows one template twice.
 *
 * parseWorkflowTemplates 读取 GET /studio/v1/workflow-templates。重复的 id 只保留第一个，
 * 选择器因此不会把同一个模板显示两次。 */
export function parseWorkflowTemplates(value: unknown): WorkflowTemplate[] {
  const seen = new Set<string>();
  const out: WorkflowTemplate[] = [];
  for (const item of list(record(value).data)) {
    const source = record(item);
    const id = text(source, "id");
    if (id === "") fail();
    const inputs = list(source.inputs).map(parseInput);
    const outputs = list(source.outputs).map((entry): TemplateOutput => {
      const output = record(entry);
      const name = text(output, "name");
      if (name === "") fail();
      return { name, type: text(output, "type"), description: text(output, "description") };
    });
    if (seen.has(id)) continue;
    seen.add(id);
    out.push({ id, description: text(source, "description"), inputs, outputs });
  }
  return out;
}

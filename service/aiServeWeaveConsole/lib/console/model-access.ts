import { ApiError } from "./errors.ts";
import { MAX_ROUTE_DOCUMENT_BYTES } from "./model-routes.ts";

/** PLATFORM_SCOPE names the deployment-wide list, mirroring model.PlatformScope. / PLATFORM_SCOPE 表示全部署列表，镜像 model.PlatformScope。 */
export const PLATFORM_SCOPE = "platform";
/** MAX_MODELS mirrors common/modelaccess.MaxModels. / MAX_MODELS 镜像 common/modelaccess.MaxModels。 */
export const MAX_MODELS = 1000;
/** MAX_MODEL_BYTES mirrors common/modelaccess.MaxModelBytes. / MAX_MODEL_BYTES 镜像 common/modelaccess.MaxModelBytes。 */
export const MAX_MODEL_BYTES = 256;
/** MAX_MODEL_ACCESS_DOCUMENT_BYTES mirrors common/modelaccess.MaxDocumentBytes. / MAX_MODEL_ACCESS_DOCUMENT_BYTES 镜像 common/modelaccess.MaxDocumentBytes。 */
export const MAX_MODEL_ACCESS_DOCUMENT_BYTES = MAX_ROUTE_DOCUMENT_BYTES;

/** ModelAccess mirrors types.ModelAccessResponse; empty models means unrestricted. / ModelAccess 镜像 types.ModelAccessResponse；models 为空表示不限制。 */
export interface ModelAccess { scope: string; models: string[]; updatedBy: string | null; updatedAt: string | null }

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}

/** parseModelAccess validates one stored list. / parseModelAccess 校验一份已存储的列表。 */
export function parseModelAccess(value: unknown): ModelAccess {
  const source = object(value);
  if (typeof source.scope !== "string" || source.scope === "" || !Array.isArray(source.models) || source.models.length > MAX_MODELS) throw new ApiError("contract");
  const models = source.models.map((model) => { if (typeof model !== "string" || model === "") throw new ApiError("contract"); return model; });
  const optional = (key: string) => {
    const field = source[key];
    if (field === undefined || field === null || field === "") return null;
    if (typeof field !== "string") throw new ApiError("contract");
    return field;
  };
  return { scope: source.scope, models, updatedBy: optional("updated_by"), updatedAt: optional("updated_at") };
}

/** normalizeModels mirrors common/modelaccess.Normalize so the page can reject a list before sending it. / normalizeModels 镜像 common/modelaccess.Normalize，使页面在发送前即可拒绝非法列表。 */
export function normalizeModels(models: readonly string[]): string[] {
  const out = new Set<string>();
  for (const raw of models) {
    const model = raw.trim();
    if (model === "" || new TextEncoder().encode(model).length > MAX_MODEL_BYTES) throw new ApiError("invalid");
    out.add(model);
  }
  if (out.size > MAX_MODELS) throw new ApiError("invalid");
  return [...out].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
}

/** parseModelLines splits a textarea into one model name per line, ignoring blank lines. / parseModelLines 把文本框按行拆成模型名，忽略空行。 */
export function parseModelLines(text: string): string[] {
  return normalizeModels(text.split(/\r?\n/).filter((line) => line.trim() !== ""));
}

/** modelAccessBodyLimit raises the bound only for the list-writing path. / modelAccessBodyLimit 仅对列表写入路径提高上限。 */
export function modelAccessBodyLimit(path: string): number | undefined {
  return /^\/operator\/v1\/model-access\/[^/]+$/.test(path) ? MAX_MODEL_ACCESS_DOCUMENT_BYTES : undefined;
}

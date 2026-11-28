import type { RequestSpec } from "./api-client.ts";
import { ApiError } from "./errors.ts";
import type { MessageKey } from "../i18n/messages.ts";

/**
 * This module mirrors the Volcengine Ark endpoints of the control plane
 * (internal/types/ark.go): GET/PUT/DELETE /{admin,operator}/v1/ark and
 * GET /{admin,operator}/v1/ark/tasks, DELETE …/ark/tasks/:task_id. A tenant
 * owner or admin saves the tenant's own Ark API key for Studio video nodes; a
 * platform operator saves the platform's, which every tenant without its own
 * uses, limited to the models listed with it and billed at their prices. The
 * key is write-only: no response carries it, only its last four characters.
 * The control plane checks the key with Ark before it saves, so a PUT can take
 * up to 20 seconds. The task list is read from Ark with the scope's own key.
 *
 * 本模块镜像控制面的火山方舟端点（internal/types/ark.go）：GET/PUT/DELETE
 * /{admin,operator}/v1/ark，以及 GET /{admin,operator}/v1/ark/tasks、DELETE
 * …/ark/tasks/:task_id。租户 owner 或 admin 为 Studio 视频节点保存租户自己的方舟 API Key；
 * 平台运维保存平台的 Key，没有自己 Key 的租户都使用它，但只能用随它列出的模型，并按其单价
 * 计费。Key 只写不读：任何响应都不携带它，只给末四位。控制面保存前会先向方舟检查 Key，因此
 * PUT 最多可能要 20 秒。任务列表用该 scope 自己的 Key 从方舟读取。
 */

/** ArkSurface picks the tenant's or the platform's endpoints. / ArkSurface 选择租户或平台的端点。 */
export type ArkSurface = "admin" | "operator";

/**
 * SAVE_TIMEOUT_MS bounds one save in the browser: the control plane's key
 * check alone may take 20 seconds (ArkProbeTimeout).
 *
 * SAVE_TIMEOUT_MS 限定浏览器里一次保存的时长：仅控制面检查 Key 就可能要 20 秒
 * （ArkProbeTimeout）。
 */
export const SAVE_TIMEOUT_MS = 35_000;
/**
 * CALL_TIMEOUT_MS bounds a task list or cancel call, which the control plane
 * allows 20 seconds against Ark (ArkCallTimeout).
 *
 * CALL_TIMEOUT_MS 限定一次任务列表或取消调用，控制面对方舟允许 20 秒（ArkCallTimeout）。
 */
export const CALL_TIMEOUT_MS = 30_000;

/** Limits mirroring logic/arkcredentials.go. / 镜像 logic/arkcredentials.go 的上限。 */
export const MAX_ARK_KEY_BYTES = 512;
export const MAX_ARK_BASE_URL_BYTES = 2048;
export const MAX_ARK_MODELS = 100;
export const MAX_ARK_MODEL_BYTES = 256;
export const MAX_ARK_TASK_PAGE = 100;
export const ARK_TASK_PAGE_SIZE = 20;

/** ARK_TASK_STATUSES are Ark's task states; a filter may name one of them. / ARK_TASK_STATUSES 是方舟的任务状态；筛选只能取其中之一。 */
export const ARK_TASK_STATUSES = ["queued", "running", "succeeded", "failed", "cancelled", "expired"] as const;
/** ArkTaskStatus is one task state, or "unknown" for one this build does not know. / ArkTaskStatus 是一种任务状态；本构建不认识的为 "unknown"。 */
export type ArkTaskStatus = (typeof ARK_TASK_STATUSES)[number] | "unknown";

/** ArkCredential mirrors types.ArkCredential. / ArkCredential 镜像 types.ArkCredential。 */
export interface ArkCredential {
  keyLast4: string;
  baseUrl: string;
  models: string[];
  updatedBy: string;
  updatedAt: string;
}

/** ArkState mirrors types.ArkResponse. / ArkState 镜像 types.ArkResponse。 */
export interface ArkState {
  enabled: boolean;
  credential: ArkCredential | null;
  platformAvailable: boolean;
}

/** ArkTask mirrors types.ArkTask. createdAt is "" when Ark did not report it. / ArkTask 镜像 types.ArkTask。方舟未报告时 createdAt 为空串。 */
export interface ArkTask {
  id: string;
  model: string;
  status: ArkTaskStatus;
  errorCode: string;
  resolution: string;
  ratio: string;
  duration: number;
  completionTokens: number;
  createdAt: string;
  updatedAt: string;
}

/** ArkTaskPage mirrors types.ArkTaskList. / ArkTaskPage 镜像 types.ArkTaskList。 */
export interface ArkTaskPage {
  total: number;
  pageNum: number;
  pageSize: number;
  items: ArkTask[];
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}

function text(value: unknown): string {
  if (typeof value !== "string") throw new ApiError("contract");
  return value;
}

function optionalText(value: unknown): string {
  return value === undefined || value === null ? "" : text(value);
}

function time(value: unknown): string {
  const raw = text(value);
  if (!Number.isFinite(Date.parse(raw))) throw new ApiError("contract");
  return raw;
}

function optionalTime(value: unknown): string {
  return value === undefined || value === null ? "" : time(value);
}

function count(value: unknown, optional = false): number {
  if (optional && (value === undefined || value === null)) return 0;
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) throw new ApiError("contract");
  return value;
}

/** parseArkCredential validates a saved key view (the GET `credential` field and the PUT answer). / parseArkCredential 校验已保存 Key 的视图（GET 的 credential 字段与 PUT 的响应）。 */
export function parseArkCredential(value: unknown): ArkCredential {
  const data = object(value);
  const last4 = text(data.key_last4);
  if (last4.length > 8) throw new ApiError("contract");
  const models = data.models === undefined || data.models === null ? [] : data.models;
  if (!Array.isArray(models) || models.length > MAX_ARK_MODELS) throw new ApiError("contract");
  return {
    keyLast4: last4,
    baseUrl: optionalText(data.base_url),
    models: models.map(text),
    updatedBy: optionalText(data.updated_by),
    updatedAt: time(data.updated_at),
  };
}

/** parseArkState validates GET …/v1/ark. / parseArkState 校验 GET …/v1/ark。 */
export function parseArkState(value: unknown): ArkState {
  const data = object(value);
  if (typeof data.enabled !== "boolean") throw new ApiError("contract");
  if (data.platform_available !== undefined && typeof data.platform_available !== "boolean") throw new ApiError("contract");
  if (!data.enabled) return { enabled: false, credential: null, platformAvailable: false };
  return {
    enabled: true,
    credential: data.credential === undefined || data.credential === null ? null : parseArkCredential(data.credential),
    platformAvailable: data.platform_available === true,
  };
}

function taskStatus(value: unknown): ArkTaskStatus {
  const raw = text(value);
  return (ARK_TASK_STATUSES as readonly string[]).includes(raw) ? (raw as ArkTaskStatus) : "unknown";
}

/** parseArkTask validates one task. / parseArkTask 校验一个任务。 */
export function parseArkTask(value: unknown): ArkTask {
  const data = object(value);
  const id = text(data.id);
  if (id === "") throw new ApiError("contract");
  return {
    id,
    model: optionalText(data.model),
    status: taskStatus(data.status),
    errorCode: optionalText(data.error_code),
    resolution: optionalText(data.resolution),
    ratio: optionalText(data.ratio),
    duration: count(data.duration, true),
    completionTokens: count(data.completion_tokens, true),
    createdAt: optionalTime(data.created_at),
    updatedAt: optionalTime(data.updated_at),
  };
}

/** parseArkTaskPage validates GET …/v1/ark/tasks. / parseArkTaskPage 校验 GET …/v1/ark/tasks。 */
export function parseArkTaskPage(value: unknown): ArkTaskPage {
  const data = object(value);
  if (!Array.isArray(data.items)) throw new ApiError("contract");
  const pageNum = count(data.page_num);
  const pageSize = count(data.page_size);
  if (pageNum < 1 || pageSize < 1 || pageSize > MAX_ARK_TASK_PAGE) throw new ApiError("contract");
  return { total: count(data.total), pageNum, pageSize, items: data.items.map(parseArkTask) };
}

/** taskAction reports what may be done to a task: cancel a queued one, delete the record of a finished one, nothing to a running one. / taskAction 报告能对任务做什么：排队中的可以取消，已结束的可以删除记录，执行中的什么都不能做。 */
export function taskAction(status: ArkTaskStatus): "cancel" | "delete" | null {
  if (status === "queued") return "cancel";
  if (status === "succeeded" || status === "failed" || status === "cancelled" || status === "expired") return "delete";
  return null;
}

/** pageCount is how many pages total tasks fill, at least one. / pageCount 是 total 个任务占的页数，至少一页。 */
export function pageCount(total: number, pageSize: number): number {
  return Math.max(1, Math.ceil(total / Math.max(1, pageSize)));
}

/** ArkDraft is the edit form's content. An empty key keeps the saved one. / ArkDraft 是编辑表单的内容。Key 为空表示保留已保存的值。 */
export interface ArkDraft {
  apiKey: string;
  baseUrl: string;
  /** models is the textarea, one model per line. / models 是多行文本框，每行一个模型。 */
  models: string;
}

/** draftFromCredential fills the form from a saved key; the key is never filled. / draftFromCredential 用已保存的 Key 填充表单；Key 永远不填充。 */
export function draftFromCredential(saved: ArkCredential | null): ArkDraft {
  return { apiKey: "", baseUrl: saved?.baseUrl ?? "", models: saved ? saved.models.join("\n") : "" };
}

/** splitModels turns the textarea into model ids: one per line or comma, trimmed, de-duplicated. / splitModels 把多行文本转成模型 id：按行或逗号拆分、去空白、去重。 */
export function splitModels(raw: string): string[] {
  const out: string[] = [];
  for (const part of raw.split(/[\n,]/)) {
    const model = part.trim();
    if (model !== "" && !out.includes(model)) out.push(model);
  }
  return out;
}

/**
 * keyKept reports whether a save may leave the key blank and keep the saved
 * one: there is a saved key and the base URL stays the same. It mirrors the
 * control plane's rule, which refuses to send a saved key to a new host.
 *
 * keyKept 报告保存时能否把 Key 留空、保留已保存的值：已有保存的 Key，且 base URL 不变。规则
 * 镜像控制面：控制面拒绝把已保存的 Key 发往新的主机。
 */
export function keyKept(draft: ArkDraft, saved: ArkCredential | null): boolean {
  return saved !== null && normalizeBaseUrl(draft.baseUrl) === saved.baseUrl;
}

/** DEFAULT_ARK_BASE_URL mirrors arkvideo.DefaultBaseURL, which saves as "". / DEFAULT_ARK_BASE_URL 镜像 arkvideo.DefaultBaseURL，保存为空串。 */
export const DEFAULT_ARK_BASE_URL = "https://ark.cn-beijing.volces.com/api/v3";

/** normalizeBaseUrl restates the control plane's normalisation: trimmed, no trailing slash, the default endpoint as "". / normalizeBaseUrl 复述控制面的规范化：去空白、去末尾斜杠，默认地址记为空串。 */
export function normalizeBaseUrl(raw: string): string {
  const trimmed = raw.trim().replace(/\/+$/, "");
  return trimmed === DEFAULT_ARK_BASE_URL ? "" : trimmed;
}

/** ArkProblem names why a draft cannot be sent. / ArkProblem 说明草稿为何不能发送。 */
export type ArkProblem = "key" | "base_url" | "models";

/**
 * checkArkDraft returns the first problem with draft, or null. The control
 * plane checks again; only shape is checked here. A tenant never sends a base
 * URL, so surface "admin" ignores it.
 *
 * checkArkDraft 返回草稿的第一个问题，没有时返回 null。控制面会再检查一次；这里只检查形状。
 * 租户从不发送 base URL，因此 surface 为 "admin" 时忽略它。
 */
export function checkArkDraft(surface: ArkSurface, draft: ArkDraft, saved: ArkCredential | null): ArkProblem | null {
  const key = draft.apiKey.trim();
  if (surface === "operator") {
    const base = normalizeBaseUrl(draft.baseUrl);
    if (base !== "") {
      let url: URL | null = null;
      try { url = new URL(base); } catch { url = null; }
      if (!url || (url.protocol !== "https:" && url.protocol !== "http:") || url.hostname === "" || url.username !== "" || url.password !== "" ||
        url.search !== "" || url.hash !== "" || base.length > MAX_ARK_BASE_URL_BYTES) return "base_url";
    }
  }
  const effective: ArkDraft = surface === "operator" ? draft : { ...draft, baseUrl: saved?.baseUrl ?? "" };
  if (key === "" ? !keyKept(effective, saved) : key.length > MAX_ARK_KEY_BYTES || !/^[\x21-\x7e]+$/.test(key)) return "key";
  const models = splitModels(draft.models);
  if (models.length > MAX_ARK_MODELS) return "models";
  for (const model of models) {
    if (new TextEncoder().encode(model).length > MAX_ARK_MODEL_BYTES || model.includes("{{") || /[\s\p{Cc}]/u.test(model)) return "models";
  }
  return null;
}

/** buildArkPutBody builds the PUT body. An empty key is left out so the control plane keeps the saved one; a tenant's body has no base URL. / buildArkPutBody 构造 PUT 请求体。空 Key 被省略，让控制面保留已保存的值；租户的请求体不带 base URL。 */
export function buildArkPutBody(surface: ArkSurface, draft: ArkDraft): Record<string, unknown> {
  const body: Record<string, unknown> = { models: splitModels(draft.models) };
  const key = draft.apiKey.trim();
  if (key !== "") body.api_key = key;
  if (surface === "operator") body.base_url = normalizeBaseUrl(draft.baseUrl);
  return body;
}

function base(surface: ArkSurface): string {
  return `/${surface}/v1/ark`;
}

/** arkRequest reads the scope's key state. / arkRequest 读取 scope 的 Key 状态。 */
export function arkRequest(surface: ArkSurface): RequestSpec<ArkState> {
  return { surface, method: "GET", path: base(surface), parse: parseArkState };
}

/** putArkRequest saves a draft. The write is sent once and never retried. / putArkRequest 保存草稿。写操作只发送一次、从不重试。 */
export function putArkRequest(surface: ArkSurface, draft: ArkDraft): RequestSpec<ArkCredential> {
  return { surface, method: "PUT", path: base(surface), body: buildArkPutBody(surface, draft), parse: parseArkCredential, timeoutMs: SAVE_TIMEOUT_MS };
}

/** deleteArkRequest removes the scope's key. / deleteArkRequest 删除 scope 的 Key。 */
export function deleteArkRequest(surface: ArkSurface): RequestSpec<null> {
  return { surface, method: "DELETE", path: base(surface), parse: () => null };
}

/** ArkTaskQuery is one page of the task list. / ArkTaskQuery 是任务列表的一页。 */
export interface ArkTaskQuery {
  pageNum: number;
  status: (typeof ARK_TASK_STATUSES)[number] | "";
}

/** arkTasksRequest reads one page of the scope's tasks. / arkTasksRequest 读取 scope 任务的一页。 */
export function arkTasksRequest(surface: ArkSurface, q: ArkTaskQuery): RequestSpec<ArkTaskPage> {
  const query: Record<string, string> = { page_num: String(Math.max(1, Math.floor(q.pageNum))), page_size: String(ARK_TASK_PAGE_SIZE) };
  if (q.status !== "") query.status = q.status;
  return { surface, method: "GET", path: `${base(surface)}/tasks`, query, parse: parseArkTaskPage, timeoutMs: CALL_TIMEOUT_MS };
}

/** deleteArkTaskRequest cancels a queued task or deletes a finished one's record; sent once. / deleteArkTaskRequest 取消排队中的任务或删除已结束任务的记录；只发送一次。 */
export function deleteArkTaskRequest(surface: ArkSurface, taskId: string): RequestSpec<null> {
  return { surface, method: "DELETE", path: `${base(surface)}/tasks/${encodeURIComponent(taskId)}`, parse: () => null, timeoutMs: CALL_TIMEOUT_MS };
}

const CODES = ["invalid_ark", "key_required", "ark_unreachable", "task_not_cancelable"] as const;
const REASONS = ["network", "auth", "rate_limited", "other"] as const;

/**
 * sanitizeArkError extracts the machine-readable part of a 400 or 503 answer
 * from the control plane: the code and, for ark_unreachable, the reason.
 * Anything outside the documented values is dropped, and the upstream `error`
 * text is never passed on.
 *
 * sanitizeArkError 从控制面 400 或 503 的响应里取出机器可读的部分：code，以及
 * ark_unreachable 时的 reason。文档之外的取值一律丢弃，上游的 `error` 文本从不转交。
 */
export function sanitizeArkError(body: string): { code: string; reason?: string } | null {
  let value: unknown;
  try { value = JSON.parse(body); } catch { return null; }
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const { code, reason } = value as Record<string, unknown>;
  if (!(CODES as readonly unknown[]).includes(code)) return null;
  const known = (REASONS as readonly unknown[]).includes(reason);
  return code === "ark_unreachable" && known ? { code: code as string, reason: reason as string } : { code: code as string };
}

/** isArkPath reports whether a forwarded path is one of the Ark endpoints whose code is passed on. / isArkPath 报告一个转发路径是否属于会转交 code 的方舟端点。 */
export function isArkPath(surface: ArkSurface, path: string): boolean {
  const root = base(surface);
  return path === root || path === `${root}/tasks` || path.startsWith(`${root}/tasks/`);
}

const REASON_MESSAGE: Record<string, MessageKey> = {
  network: "tenant.arkReasonNetwork",
  auth: "tenant.arkReasonAuth",
  rate_limited: "tenant.arkReasonRateLimited",
  other: "tenant.arkReasonOther",
};

/**
 * arkErrorMessage maps a failed Ark call to fixed copy, or null when the
 * generic copy for the request's failure kind should be used instead.
 *
 * arkErrorMessage 把一次失败的方舟调用映射到固定文案；需要改用该请求失败类型的通用文案时
 * 返回 null。
 */
export function arkErrorMessage(error: unknown): MessageKey | null {
  if (!(error instanceof ApiError) || (error.status !== 400 && error.status !== 503)) return null;
  switch (error.code) {
    case "invalid_ark": return "tenant.arkErrInvalid";
    case "key_required": return "tenant.arkErrKeyRequired";
    case "task_not_cancelable": return "tenant.arkErrNotCancelable";
    case "ark_unreachable": return REASON_MESSAGE[error.reason] ?? "tenant.arkReasonOther";
    default: return null;
  }
}

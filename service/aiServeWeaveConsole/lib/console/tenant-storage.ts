import type { RequestSpec } from "./api-client.ts";
import { ApiError } from "./errors.ts";
import type { MessageKey } from "../i18n/messages.ts";

/**
 * This module mirrors the tenant object storage endpoint of the control plane:
 * GET/PUT/DELETE /admin/v1/storage (.scratch/tenant-object-storage/spec.md,
 * 「接口 → Console」). A tenant owner or admin points the tenant's workflow
 * artifacts and Studio files at their own S3 or WebDAV. The secrets are
 * write-only: no response carries them, S3 shows the last four characters of
 * the access key ID and WebDAV shows the user name. The control plane probes
 * the storage before it saves, so a PUT can take several seconds.
 *
 * 本模块镜像控制面的租户对象存储端点：GET/PUT/DELETE /admin/v1/storage
 * （.scratch/tenant-object-storage/spec.md「接口 → Console」）。租户 owner 或 admin 把本租户的
 * 工作流产物与 Studio 文件指向自己的 S3 或 WebDAV。密钥只写不读：任何响应都不携带它们，
 * S3 只给 Access Key ID 的末四位，WebDAV 给出用户名。控制面保存前会探测该存储，因此 PUT
 * 可能要几秒。
 */

/** MAX_RETENTION_DAYS mirrors the control plane's upper bound. / MAX_RETENTION_DAYS 镜像控制面的上限。 */
export const MAX_RETENTION_DAYS = 36500;
/**
 * SAVE_TIMEOUT_MS bounds one save in the browser. The control plane probes the
 * storage (write, read back, delete) before it answers, which outlasts the
 * default request timeout when the endpoint is slow or unreachable.
 *
 * SAVE_TIMEOUT_MS 限定浏览器里一次保存的时长。控制面在回答之前要探测存储（写入、读回、
 * 删除），端点很慢或连不上时会超过默认的请求超时。
 */
export const SAVE_TIMEOUT_MS = 35_000;

/** StorageKind is the kind of a saved configuration. / StorageKind 是已保存配置的类型。 */
export type StorageKind = "s3" | "webdav";

/** S3Location mirrors the s3 object of the view. / S3Location 镜像视图中的 s3 对象。 */
export interface S3Location {
  endpoint: string;
  region: string;
  bucket: string;
  prefix: string;
  pathStyle: boolean;
  accessKeyIdLast4: string;
}

/** WebDavLocation mirrors the webdav object of the view. / WebDavLocation 镜像视图中的 webdav 对象。 */
export interface WebDavLocation {
  url: string;
  dir: string;
  username: string;
}

/** TenantStorage is a saved configuration. / TenantStorage 是一份已保存的配置。 */
export interface TenantStorage {
  kind: StorageKind;
  s3: S3Location | null;
  webdav: WebDavLocation | null;
  retentionDays: number;
  updatedBy: string;
  updatedAt: string;
}

/** StorageState is GET /admin/v1/storage. / StorageState 是 GET /admin/v1/storage 的响应。 */
export interface StorageState {
  enabled: boolean;
  allowPrivateEndpoints: boolean;
  storage: TenantStorage | null;
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}

function text(value: unknown): string {
  if (typeof value !== "string") throw new ApiError("contract");
  return value;
}

function optionalObject(value: unknown): Record<string, unknown> | null {
  return value === undefined || value === null ? null : object(value);
}

function parseS3(raw: Record<string, unknown>): S3Location {
  const last4 = text(raw.access_key_id_last4);
  if (last4.length > 8 || typeof raw.path_style !== "boolean") throw new ApiError("contract");
  return {
    endpoint: text(raw.endpoint), region: text(raw.region), bucket: text(raw.bucket), prefix: text(raw.prefix),
    pathStyle: raw.path_style, accessKeyIdLast4: last4,
  };
}

function parseWebDav(raw: Record<string, unknown>): WebDavLocation {
  return { url: text(raw.url), dir: text(raw.dir), username: text(raw.username) };
}

/** parseTenantStorage validates a storage view (the GET `storage` field and the PUT answer). / parseTenantStorage 校验存储视图（GET 的 storage 字段与 PUT 的响应）。 */
export function parseTenantStorage(value: unknown): TenantStorage {
  const data = object(value);
  const s3 = optionalObject(data.s3);
  const webdav = optionalObject(data.webdav);
  const kind = data.kind;
  // The object that matches the kind must be there, and the other must not:
  // a view that names s3 but carries a webdav block cannot be drawn truthfully.
  // 与 kind 对应的对象必须存在，另一个必须缺席：声称 s3 却携带 webdav 块的视图无法如实绘制。
  if (!((kind === "s3" && s3 && !webdav) || (kind === "webdav" && webdav && !s3))) throw new ApiError("contract");
  const days = data.retention_days;
  if (typeof days !== "number" || !Number.isSafeInteger(days) || days < 0 || days > MAX_RETENTION_DAYS) throw new ApiError("contract");
  const updatedAt = text(data.updated_at);
  if (!Number.isFinite(Date.parse(updatedAt))) throw new ApiError("contract");
  return {
    kind, s3: s3 ? parseS3(s3) : null, webdav: webdav ? parseWebDav(webdav) : null,
    retentionDays: days, updatedBy: text(data.updated_by), updatedAt,
  };
}

/** parseStorageState validates GET /admin/v1/storage. / parseStorageState 校验 GET /admin/v1/storage。 */
export function parseStorageState(value: unknown): StorageState {
  const data = object(value);
  if (typeof data.enabled !== "boolean") throw new ApiError("contract");
  if (!data.enabled) return { enabled: false, allowPrivateEndpoints: false, storage: null };
  if (typeof data.allow_private_endpoints !== "boolean") throw new ApiError("contract");
  return {
    enabled: true,
    allowPrivateEndpoints: data.allow_private_endpoints,
    storage: data.storage === undefined || data.storage === null ? null : parseTenantStorage(data.storage),
  };
}

/** DraftKind adds "none" (no own storage) to the saved kinds. / DraftKind 在已保存类型之外加上「不使用」。 */
export type DraftKind = "none" | StorageKind;

/** StorageDraft is the edit form's content. Empty secrets keep the saved ones. / StorageDraft 是编辑表单的内容。密钥为空表示保留已保存的值。 */
export interface StorageDraft {
  kind: DraftKind;
  endpoint: string;
  region: string;
  bucket: string;
  prefix: string;
  pathStyle: boolean;
  accessKeyId: string;
  secretAccessKey: string;
  webdavUrl: string;
  webdavDir: string;
  webdavUsername: string;
  webdavPassword: string;
  /** retentionDays stays text so a half-typed number is not rewritten under the cursor. / retentionDays 保持为文本，免得半途输入的数字在光标下被改写。 */
  retentionDays: string;
}

/** emptyDraft is the form for a tenant without a configuration. / emptyDraft 是尚无配置的租户所见的表单。 */
export function emptyDraft(): StorageDraft {
  return {
    kind: "none", endpoint: "", region: "", bucket: "", prefix: "", pathStyle: false, accessKeyId: "", secretAccessKey: "",
    webdavUrl: "", webdavDir: "", webdavUsername: "", webdavPassword: "", retentionDays: "0",
  };
}

/** draftFromStorage fills the form from a saved configuration; secrets are never filled. / draftFromStorage 用已保存的配置填充表单；密钥永远不填充。 */
export function draftFromStorage(saved: TenantStorage | null): StorageDraft {
  const draft = emptyDraft();
  if (!saved) return draft;
  draft.kind = saved.kind;
  draft.retentionDays = String(saved.retentionDays);
  if (saved.s3) {
    Object.assign(draft, { endpoint: saved.s3.endpoint, region: saved.s3.region, bucket: saved.s3.bucket, prefix: saved.s3.prefix, pathStyle: saved.s3.pathStyle });
  }
  if (saved.webdav) {
    Object.assign(draft, { webdavUrl: saved.webdav.url, webdavDir: saved.webdav.dir, webdavUsername: saved.webdav.username });
  }
  return draft;
}

/**
 * locationChanged reports whether saving draft would move the storage away
 * from the saved location: another kind, another S3 endpoint, region or
 * bucket, or another WebDAV URL. It is the control plane's rule for when the
 * saved secrets may be kept, restated so the form can ask for them before the
 * request, and it is also when files already written stay behind. Prefix, path
 * style, directory, user name and retention do not count.
 *
 * locationChanged 报告保存草稿是否会让存储离开已保存的位置：换了类型，或换了 S3 的
 * endpoint、region、bucket，或换了 WebDAV 的 URL。这是控制面「何时可以保留已保存密钥」的
 * 规则，在此复述好让表单在请求之前就要求重填；同时也是已写入的文件会被留在原处的时机。
 * 前缀、path-style、目录、用户名与保留天数不算。
 */
export function locationChanged(draft: StorageDraft, saved: TenantStorage | null): boolean {
  if (draft.kind === "none" || saved === null) return false;
  if (draft.kind !== saved.kind) return true;
  if (draft.kind === "s3" && saved.s3) {
    return draft.endpoint.trim() !== saved.s3.endpoint || draft.region.trim() !== saved.s3.region || draft.bucket.trim() !== saved.s3.bucket;
  }
  if (draft.kind === "webdav" && saved.webdav) return draft.webdavUrl.trim() !== saved.webdav.url;
  return true;
}

/**
 * secretsKept reports whether saving draft may leave its secrets blank and
 * keep the saved ones: the same kind at the same location, and for WebDAV the
 * same user name too, since a password belongs to its user. It mirrors the
 * control plane's rule.
 *
 * secretsKept 报告保存草稿时能否把密钥留空、保留已保存的值：类型与位置不变；WebDAV 还要求
 * 用户名不变，因为密码属于它的用户。规则镜像控制面。
 */
export function secretsKept(draft: StorageDraft, saved: TenantStorage | null): boolean {
  if (saved === null || draft.kind === "none" || draft.kind !== saved.kind || locationChanged(draft, saved)) return false;
  if (draft.kind === "webdav") return saved.webdav !== null && draft.webdavUsername.trim() === saved.webdav.username;
  return true;
}

/** StorageProblem names why a draft cannot be sent. / StorageProblem 说明草稿为何不能发送。 */
export type StorageProblem = "bucket" | "url" | "https" | "credentials" | "retention";

function parsedUrl(value: string): URL | null {
  try {
    const url = new URL(value);
    return (url.protocol === "https:" || url.protocol === "http:") && url.hostname !== "" ? url : null;
  } catch {
    return null;
  }
}

/** parseRetention returns the retention days, or null when text is not an integer in range. / parseRetention 返回保留天数，文本不是范围内的整数时返回 null。 */
export function parseRetention(value: string): number | null {
  const trimmed = value.trim();
  if (!/^\d{1,5}$/.test(trimmed)) return null;
  const days = Number(trimmed);
  return days <= MAX_RETENTION_DAYS ? days : null;
}

/**
 * checkDraft returns the first problem with draft, or null. Hosts and private
 * ranges are the control plane's to judge; only the shape and the "no https,
 * no private endpoints" rule are checked here. Secrets are required for a new
 * configuration and whenever the location changes, and a partly typed pair is
 * accepted when the location is unchanged because every secret field is kept
 * on its own.
 *
 * checkDraft 返回草稿的第一个问题，没有时返回 null。主机与内网段由控制面裁决；这里只
 * 检查形状以及「未开放内网时只接受 https」。新配置、位置变化时必须填写密钥；位置不变时
 * 允许只填其中一个，因为每个密钥字段各自保留。
 */
export function checkDraft(draft: StorageDraft, saved: TenantStorage | null, allowPrivateEndpoints: boolean): StorageProblem | null {
  if (draft.kind === "none") return null;
  const location = draft.kind === "s3" ? draft.endpoint.trim() : draft.webdavUrl.trim();
  if (draft.kind === "s3" && draft.bucket.trim() === "") return "bucket";
  if (location === "") {
    if (draft.kind === "webdav") return "url";
  } else {
    const url = parsedUrl(location);
    if (!url) return "url";
    if (!allowPrivateEndpoints && url.protocol !== "https:") return "https";
  }
  if (parseRetention(draft.retentionDays) === null) return "retention";
  if (!secretsKept(draft, saved)) {
    if (draft.kind === "s3" ? draft.accessKeyId.trim() === "" || draft.secretAccessKey.trim() === "" : draft.webdavPassword === "") return "credentials";
  }
  return null;
}

/**
 * buildPutBody builds the PUT body for a draft whose kind is not "none". An
 * empty secret is left out so the control plane keeps the saved one; the
 * other kind's block is never sent.
 *
 * buildPutBody 为类型不是「不使用」的草稿构造 PUT 请求体。空密钥被省略，让控制面保留
 * 已保存的值；另一种类型的块从不发送。
 */
export function buildPutBody(draft: StorageDraft): Record<string, unknown> {
  const retention = parseRetention(draft.retentionDays) ?? 0;
  if (draft.kind === "webdav") {
    const webdav: Record<string, unknown> = { url: draft.webdavUrl.trim(), dir: draft.webdavDir.trim(), username: draft.webdavUsername.trim() };
    if (draft.webdavPassword !== "") webdav.password = draft.webdavPassword;
    return { kind: "webdav", webdav, retention_days: retention };
  }
  const s3: Record<string, unknown> = {
    endpoint: draft.endpoint.trim(), region: draft.region.trim(), bucket: draft.bucket.trim(), prefix: draft.prefix.trim(), path_style: draft.pathStyle,
  };
  if (draft.accessKeyId.trim() !== "") s3.access_key_id = draft.accessKeyId.trim();
  if (draft.secretAccessKey.trim() !== "") s3.secret_access_key = draft.secretAccessKey.trim();
  return { kind: "s3", s3, retention_days: retention };
}

/** storageRequest reads the tenant's storage configuration. / storageRequest 读取租户的存储配置。 */
export function storageRequest(): RequestSpec<StorageState> {
  return { method: "GET", path: "/admin/v1/storage", parse: parseStorageState };
}

/** putStorageRequest saves a draft whose kind is not "none". The write is sent once and never retried. / putStorageRequest 保存类型不是「不使用」的草稿。写操作只发送一次、从不重试。 */
export function putStorageRequest(draft: StorageDraft): RequestSpec<TenantStorage> {
  return { method: "PUT", path: "/admin/v1/storage", body: buildPutBody(draft), parse: parseTenantStorage, timeoutMs: SAVE_TIMEOUT_MS };
}

/** deleteStorageRequest removes the configuration. / deleteStorageRequest 删除配置。 */
export function deleteStorageRequest(): RequestSpec<null> {
  return { method: "DELETE", path: "/admin/v1/storage", parse: () => null };
}

const CODES = ["invalid_storage", "credentials_required", "endpoint_not_allowed", "storage_unreachable"] as const;
const REASONS = ["network", "auth", "not_found", "permission", "endpoint_not_allowed", "other"] as const;

/**
 * sanitizeStorageError extracts the machine-readable part of a 400 answer from
 * the control plane: the code and, for an unreachable storage, the reason.
 * Anything outside the documented values is dropped, and the upstream `error`
 * text is never passed on, so the browser can only learn a fixed vocabulary.
 *
 * sanitizeStorageError 从控制面 400 的响应里取出机器可读的部分：code，以及存储连不通时的
 * reason。文档之外的取值一律丢弃，上游的 `error` 文本从不转交，因此浏览器只能得到一套
 * 固定的词汇。
 */
export function sanitizeStorageError(body: string): { code: string; reason?: string } | null {
  let value: unknown;
  try { value = JSON.parse(body); } catch { return null; }
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const { code, reason } = value as Record<string, unknown>;
  if (!(CODES as readonly unknown[]).includes(code)) return null;
  const known = (REASONS as readonly unknown[]).includes(reason);
  return code === "storage_unreachable" && known ? { code: code as string, reason: reason as string } : { code: code as string };
}

const REASON_MESSAGE: Record<string, MessageKey> = {
  network: "tenant.storageReasonNetwork",
  auth: "tenant.storageReasonAuth",
  not_found: "tenant.storageReasonNotFound",
  permission: "tenant.storageReasonPermission",
  endpoint_not_allowed: "tenant.storageErrEndpoint",
  other: "tenant.storageReasonOther",
};

/**
 * storageErrorMessage maps a failed save to fixed copy, or null when the
 * generic copy for the request's failure kind should be used instead (an
 * unknown code, a 403, a timeout). An unknown reason of an unreachable storage
 * reads as "other".
 *
 * storageErrorMessage 把一次失败的保存映射到固定文案；需要改用该请求失败类型的通用文案
 * 时（未知 code、403、超时）返回 null。存储连不通但 reason 未知时按「其他」处理。
 */
export function storageErrorMessage(error: unknown): MessageKey | null {
  if (!(error instanceof ApiError) || error.status !== 400) return null;
  switch (error.code) {
    case "invalid_storage": return "tenant.storageErrInvalid";
    case "credentials_required": return "tenant.storageErrCredentials";
    case "endpoint_not_allowed": return "tenant.storageErrEndpoint";
    case "storage_unreachable": return REASON_MESSAGE[error.reason] ?? "tenant.storageReasonOther";
    default: return null;
  }
}

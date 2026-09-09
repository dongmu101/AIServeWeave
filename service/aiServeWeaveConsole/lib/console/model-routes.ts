import { ApiError, describe } from "./errors.ts";

/** MAX_ROUTES_BYTES bounds imported bundles. / MAX_ROUTES_BYTES 限制导入集合大小。 */
export const MAX_ROUTES_BYTES = 1 << 20;
/** MAX_ROUTE_DOCUMENT_BYTES allows bounded snapshot metadata. / MAX_ROUTE_DOCUMENT_BYTES 为快照元数据预留有界空间。 */
export const MAX_ROUTE_DOCUMENT_BYTES = MAX_ROUTES_BYTES + 64 * 1024;
/** ModelTarget preserves Gateway target semantics. / ModelTarget 保留 Gateway 目标语义。 */
export interface ModelTarget { runtime_model: string; node_selector?: Record<string, string>; priority?: number; weight?: number }
/** ModelRoute maps an alias to targets. / ModelRoute 把别名映射到目标。 */
export interface ModelRoute { model: string; targets: ModelTarget[] }
/** RouteRevision describes an immutable publication. / RouteRevision 描述不可变发布。 */
export interface RouteRevision { revision: number; digest: string; created_at: string; actor_id: string; rollback_of?: number }
/** RouteSnapshot is a versioned bundle. / RouteSnapshot 是带版本的集合。 */
export interface RouteSnapshot extends RouteRevision { routes: ModelRoute[] }
/** RouteHistory is a bounded revision page. / RouteHistory 是有界版本页。 */
export interface RouteHistory { items: RouteRevision[]; next_before?: number }
/** RouteReplica records one live observation. / RouteReplica 记录一次实时观测。 */
export interface RouteReplica { endpoint: string; replica_id?: string; mode?: string; revision?: number; digest?: string; applied_at?: string; checked_at?: string; generated_at?: string; error?: string }
/** RouteStatus compares desired and live replicas. / RouteStatus 比较期望与实时副本。 */
export interface RouteStatus { desired_revision: number; desired_digest: string; checked_at: string; complete: boolean; replicas: RouteReplica[] }

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}
function string(value: unknown): string { if (typeof value !== "string") throw new ApiError("contract"); return value; }
function integer(value: unknown, min = 0): number { if (typeof value !== "number" || !Number.isSafeInteger(value) || value < min) throw new ApiError("contract"); return value; }
function known(value: Record<string, unknown>, keys: string[]) { if (Object.keys(value).some((key) => !keys.includes(key))) throw new ApiError("invalid"); }

/** parseRoutes validates and copies an editable bundle. / parseRoutes 校验并复制可编辑集合。 */
export function parseRoutes(value: unknown): ModelRoute[] {
  if (!Array.isArray(value) || value.length > 1000 || new TextEncoder().encode(JSON.stringify(value)).length > MAX_ROUTES_BYTES) throw new ApiError("invalid");
  const aliases = new Set<string>();
  return value.map((item) => {
    const route = object(item); known(route, ["model", "targets"]);
    const model = string(route.model);
    if (!model.trim() || aliases.has(model) || !Array.isArray(route.targets) || route.targets.length < 1 || route.targets.length > 100) throw new ApiError("invalid");
    aliases.add(model);
    return { model, targets: route.targets.map((item) => {
      const target = object(item); known(target, ["runtime_model", "node_selector", "priority", "weight"]);
      const result: ModelTarget = { runtime_model: string(target.runtime_model) };
      if (!result.runtime_model.trim()) throw new ApiError("invalid");
      if (target.priority !== undefined) result.priority = integer(target.priority, Number.MIN_SAFE_INTEGER);
      if (target.weight !== undefined) result.weight = integer(target.weight);
      if (target.node_selector !== undefined && target.node_selector !== null) result.node_selector = Object.fromEntries(Object.entries(object(target.node_selector)).map(([key, value]) => { if (!key.trim()) throw new ApiError("invalid"); return [key, string(value)]; }));
      return result;
    }) };
  });
}
function revision(value: unknown): RouteRevision {
  const source = object(value);
  const result: RouteRevision = { revision: integer(source.revision), digest: string(source.digest), created_at: string(source.created_at), actor_id: string(source.actor_id) };
  if (source.rollback_of !== undefined) result.rollback_of = integer(source.rollback_of, 1);
  return result;
}
/** parseRouteSnapshot validates publication responses. / parseRouteSnapshot 校验发布响应。 */
export function parseRouteSnapshot(value: unknown): RouteSnapshot { return { ...revision(value), routes: parseRoutes(object(value).routes) }; }
/** parseRouteHistory validates bounded history pages. / parseRouteHistory 校验有界历史页。 */
export function parseRouteHistory(value: unknown): RouteHistory {
  const source = object(value);
  if (!Array.isArray(source.items) || source.items.length > 50) throw new ApiError("contract");
  return { items: source.items.map(revision), ...(source.next_before === undefined ? {} : { next_before: integer(source.next_before, 1) }) };
}
/** parseRouteValidation validates the server verdict. / parseRouteValidation 校验服务端校验结论。 */
export function parseRouteValidation(value: unknown): string { const source = object(value); if (source.valid !== true) throw new ApiError("contract"); return string(source.digest); }
/** parseRouteStatus never trusts a complete flag without matching observations. / parseRouteStatus 不在缺少匹配观测时相信完成标志。 */
export function parseRouteStatus(value: unknown): RouteStatus {
  const source = object(value);
  if (!Array.isArray(source.replicas) || typeof source.complete !== "boolean") throw new ApiError("contract");
  const desired_revision = integer(source.desired_revision), desired_digest = string(source.desired_digest);
  const replicas = source.replicas.map((item) => {
    const row = object(item); const result: RouteReplica = { endpoint: string(row.endpoint) };
    for (const key of ["replica_id", "mode", "digest", "applied_at", "checked_at", "generated_at", "error"] as const) if (row[key] !== undefined) result[key] = string(row[key]);
    if (row.revision !== undefined) result.revision = integer(row.revision);
    return result;
  });
  return { desired_revision, desired_digest, checked_at: string(source.checked_at), replicas, complete: source.complete && desired_revision > 0 && replicas.length > 0 && replicas.every((row) => !row.error && row.mode === "controlplane" && row.revision === desired_revision && row.digest === desired_digest) };
}
/** importRouteFile bounds both declared size and streamed bytes before parsing. / importRouteFile 在解析前限制声明大小与流式字节数。 */
export async function importRouteFile(file: Pick<Blob, "size" | "stream">): Promise<ModelRoute[]> {
  if (file.size > MAX_ROUTES_BYTES) throw new ApiError("invalid");
  const reader = file.stream().getReader(); const decoder = new TextDecoder(); let size = 0, text = "";
  try {
    for (;;) {
      const { value, done } = await reader.read(); if (done) break;
      size += value.byteLength;
      if (size > MAX_ROUTES_BYTES) { await reader.cancel(); throw new ApiError("invalid"); }
      text += decoder.decode(value, { stream: true });
    }
    const value: unknown = JSON.parse(text + decoder.decode());
    return parseRoutes(Array.isArray(value) ? value : [value]);
  } catch { throw new ApiError("invalid"); } finally { reader.releaseLock(); }
}
/** routeWriteMessage explains conflicts without discarding the draft. / routeWriteMessage 解释冲突且不丢弃草稿。 */
export function routeWriteMessage(error: unknown): string {
  return error instanceof ApiError && error.kind === "conflict" ? "当前版本已变化，草稿已保留。请重新读取当前版本，比较后再次确认发布。" : `${describe(error)} 草稿已保留；请刷新当前版本核对结果。`;
}

/** routeBodyLimit raises the bound only for matched bundle-writing paths. / routeBodyLimit 仅对已匹配的集合写入路径提高上限。 */
export function routeBodyLimit(path: string): number | undefined {
  return path === "/operator/v1/routes/validate" || path === "/operator/v1/routes/publish" ? MAX_ROUTE_DOCUMENT_BYTES : undefined;
}

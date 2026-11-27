/**
 * The Studio API calls the browser may make through this server, by method
 * and path. Anything else is a 404 here and never reaches the Gateway. Path
 * segments are matched exactly; ":id" matches one non-empty segment of safe
 * characters only, so "..", encoded slashes and query smuggling cannot form
 * a different upstream path.
 *
 * 浏览器可经本服务发起的 Studio API 调用，按方法与路径列出。其余一律在这里 404，永远
 * 到不了 Gateway。路径段精确匹配；":id" 只匹配一个由安全字符组成的非空段，因此 ".."、
 * 编码过的斜杠与查询串夹带都拼不出另一条上游路径。
 */

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

interface Route {
  method: Method;
  pattern: string[];
  query: string[];
  /** maxBody raises the request body bound for this route; others keep the
   * default. Saving a flow carries its whole definition. Starting a run is
   * not here at all: it carries files, so it has its own streaming route
   * (app/api/studio/flows/[id]/runs/start).
   * maxBody 为该路由放宽请求体上限；其他路由保持默认值。保存编排要带上完整定义。启动运行
   * 根本不在这里：它带文件，所以有自己的流式路由
   * （app/api/studio/flows/[id]/runs/start）。 */
  maxBody?: number;
}

/** FLOW_BODY_BYTES fits a definition of common/flowdef's 1 MiB bound plus the
 * name.
 * FLOW_BODY_BYTES 容得下 common/flowdef 上限 1 MiB 的定义加名称。 */
export const FLOW_BODY_BYTES = (1 << 20) + 64 * 1024;

const ROUTES: Route[] = [
  { method: "GET", pattern: ["me"], query: [] },
  { method: "GET", pattern: ["models"], query: [] },
  { method: "GET", pattern: ["conversations"], query: ["q", "cursor", "limit"] },
  { method: "POST", pattern: ["conversations"], query: [] },
  { method: "GET", pattern: ["conversations", ":id"], query: [] },
  { method: "PATCH", pattern: ["conversations", ":id"], query: [] },
  { method: "DELETE", pattern: ["conversations", ":id"], query: [] },
  { method: "GET", pattern: ["conversations", ":id", "messages"], query: ["after_seq", "limit"] },
  { method: "GET", pattern: ["flows"], query: ["cursor", "limit"] },
  { method: "POST", pattern: ["flows"], query: [], maxBody: FLOW_BODY_BYTES },
  { method: "GET", pattern: ["flows", ":id"], query: [] },
  { method: "PATCH", pattern: ["flows", ":id"], query: [], maxBody: FLOW_BODY_BYTES },
  { method: "DELETE", pattern: ["flows", ":id"], query: [] },
  { method: "GET", pattern: ["flows", ":id", "versions", ":id"], query: [] },
  { method: "GET", pattern: ["flows", ":id", "runs"], query: ["cursor", "limit"] },
  { method: "GET", pattern: ["image-models"], query: [] },
  { method: "GET", pattern: ["workflow-templates"], query: [] },
  { method: "GET", pattern: ["runs", ":id"], query: [] },
  { method: "POST", pattern: ["runs", ":id", "cancel"], query: [] },
];

const SEGMENT = /^[A-Za-z0-9_-]{1,128}$/;

export interface ResolvedRoute {
  method: Method;
  path: string;
  search: string;
  maxBody?: number;
}

/** resolveStudioRoute maps a browser request onto an allowed Gateway call, or
 * returns null. segments are the path parts after /api/studio/.
 *
 * resolveStudioRoute 把一次浏览器请求映射为一个允许的 Gateway 调用，否则返回 null。
 * segments 是 /api/studio/ 之后的路径段。 */
export function resolveStudioRoute(method: string, segments: string[], params: URLSearchParams): ResolvedRoute | null {
  if (!segments.every((segment) => SEGMENT.test(segment))) return null;
  const route = ROUTES.find(
    (candidate) =>
      candidate.method === method &&
      candidate.pattern.length === segments.length &&
      candidate.pattern.every((part, i) => part === ":id" || part === segments[i])
  );
  if (!route) return null;
  const kept = new URLSearchParams();
  for (const name of route.query) {
    const value = params.get(name);
    if (value !== null) kept.set(name, value);
  }
  const search = kept.toString();
  const resolved: ResolvedRoute = { method: route.method, path: `/studio/v1/${segments.join("/")}`, search: search ? `?${search}` : "" };
  if (route.maxBody !== undefined) resolved.maxBody = route.maxBody;
  return resolved;
}

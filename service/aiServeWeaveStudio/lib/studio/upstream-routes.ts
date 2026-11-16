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
}

const ROUTES: Route[] = [
  { method: "GET", pattern: ["me"], query: [] },
  { method: "GET", pattern: ["models"], query: [] },
];

const SEGMENT = /^[A-Za-z0-9_-]{1,128}$/;

export interface ResolvedRoute {
  method: Method;
  path: string;
  search: string;
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
  return { method: route.method, path: `/studio/v1/${segments.join("/")}`, search: search ? `?${search}` : "" };
}

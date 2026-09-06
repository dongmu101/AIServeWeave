/**
 * This module is the Console server's allowlist of control plane calls.
 *
 * The forwarding entry point exists so the browser never holds a control plane
 * token; that only helps if the browser also cannot choose what the token is
 * spent on. So the entry point is not a proxy: it resolves the incoming
 * method and path against the table below, and forwards nothing that does not
 * match. There is no upstream host in a request, no arbitrary path, and no
 * query parameter this file does not name.
 *
 * `POST /admin/v1/auth/login` is deliberately absent: sign-in is the one call
 * that produces a token, and it is handled by the session route so the token
 * has exactly one way into a cookie. `/internal/v1/apikeys/verify` is absent
 * because it belongs to the Gateway and needs a secret the Console does not
 * have; `POST /admin/v1/tenants` is absent because tenant bootstrap is an
 * operator task guarded by a bootstrap token.
 *
 * 本模块是 Console 服务端对控制面调用的白名单。
 *
 * 转发入口的存在，是为了让浏览器永远不持有控制面令牌；而这只有在浏览器同样无法决定
 * 这个令牌被花在什么地方时才有意义。所以这个入口不是代理：它把进来的方法与路径拿去
 * 与下面这张表求解，匹配不上的一律不转发。请求里没有上游主机，没有任意路径，也没有
 * 本文件未点名的查询参数。
 *
 * `POST /admin/v1/auth/login` 刻意不在其中：登录是唯一产出令牌的调用，它由 session 路由
 * 处理，好让令牌进入 cookie 的路径只有一条。`/internal/v1/apikeys/verify` 不在其中，
 * 因为它属于 Gateway，且需要 Console 并不持有的密钥；`POST /admin/v1/tenants` 不在其中，
 * 因为租户引导是由 bootstrap token 守卫的运维动作。
 */

/** Segment is one path segment: a literal, or a placeholder that accepts one
 * non-empty segment.
 *
 * Segment 是路径中的一节：字面量，或接受一节非空内容的占位符。 */
type Segment = { literal: string } | { param: true };

/** UpstreamRoute is one allowed call.
 *
 * UpstreamRoute 是一条被允许的调用。 */
interface UpstreamRoute {
  method: string;
  segments: Segment[];
  /** query lists the query parameters that may be forwarded. Anything else is
   * dropped rather than passed on.
   *
   * query 列出可以被转发的查询参数。其余的会被丢弃而不是继续传递。 */
  query: readonly string[];
}

/** literal and param build the table below readably.
 *
 * literal 与 param 让下面这张表读起来清楚。 */
const literal = (value: string): Segment => ({ literal: value });
const param = (): Segment => ({ param: true });

/**
 * ROUTES is the whole forwarding surface, in one screen on purpose — which
 * calls the Console may make should be readable without following the code
 * into the route handler.
 *
 * ROUTES 是全部的转发面，刻意放在一屏之内 —— Console 可以发起哪些调用，应当无需追进
 * route handler 就能读出来。
 */
const ROUTES: readonly UpstreamRoute[] = [
  {
    method: "GET",
    segments: [literal("admin"), literal("v1"), literal("users")],
    query: ["limit", "cursor", "role", "q"],
  },
  {
    method: "POST",
    segments: [literal("admin"), literal("v1"), literal("users")],
    query: [],
  },
  {
    method: "GET",
    segments: [literal("admin"), literal("v1"), literal("apikeys")],
    query: ["limit", "cursor", "status", "q"],
  },
  {
    method: "POST",
    segments: [literal("admin"), literal("v1"), literal("apikeys")],
    query: [],
  },
  {
    method: "DELETE",
    segments: [literal("admin"), literal("v1"), literal("apikeys"), param()],
    query: [],
  },
  {
    method: "GET",
    segments: [
      literal("admin"),
      literal("v1"),
      literal("tenants"),
      literal("current"),
    ],
    query: [],
  },
  {
    method: "PUT",
    segments: [
      literal("admin"),
      literal("v1"),
      literal("tenants"),
      literal("limits"),
    ],
    query: [],
  },
  {
    method: "GET",
    segments: [literal("admin"), literal("v1"), literal("audit")],
    query: ["limit", "cursor", "action", "actor_id", "since", "until"],
  },
  {
    method: "GET",
    segments: [literal("admin"), literal("v1"), literal("workflows")],
    query: [],
  },
  {
    // The tenant is never a parameter here: it comes from the session, the
    // same rule the quota endpoints follow. A `tenant_id` in the query would
    // be dropped by this table, but it has no business being sent at all.
    //
    // 这里租户绝不是参数：它来自会话，与配额端点遵循同一条规则。查询串里的
    // `tenant_id` 会被本表丢弃，但它压根就不该被发送。
    method: "GET",
    segments: [literal("admin"), literal("v1"), literal("jobs")],
    query: [],
  },
];

/**
 * OPERATOR_ROUTES is the second, separate surface: the fleet inventory.
 *
 * It is its own table, and it is resolved by its own function, because the two
 * surfaces are forwarded with different credentials and are open to different
 * people. One table with a flag on each row would put "which secret pays for
 * this call" one boolean away from being wrong.
 *
 * OPERATOR_ROUTES 是第二个、独立的面：机群清单。
 *
 * 它有自己的表，也由自己的函数求解，因为这两个面用不同的凭据转发，面向不同的人开放。
 * 用一张表加一个行标志，会让「这次调用由哪个密钥支付」距离出错只有一个布尔值之遥。
 */
const OPERATOR_ROUTES: readonly UpstreamRoute[] = [
  {
    method: "GET",
    segments: [literal("operator"), literal("v1"), literal("nodes")],
    query: [],
  },
  {
    method: "GET",
    segments: [literal("operator"), literal("v1"), literal("models")],
    query: [],
  },
  {
    method: "GET",
    segments: [literal("operator"), literal("v1"), literal("workflows")],
    query: [],
  },
];

/**
 * resolveUpstream returns the upstream path and query for a request, or null
 * when the request matches no allowed route.
 *
 * The returned path is rebuilt from the matched segments rather than taken
 * from the request, so a segment that decoded to something else — a slash, a
 * "..", a second query string — cannot survive into the upstream URL.
 *
 * resolveUpstream 返回一次请求对应的上游路径与查询串，请求匹配不上任何被允许的路由时
 * 返回 null。
 *
 * 返回的路径是由匹配到的各节重建的，而不是取自请求本身，因此某一节解码后变成别的东西
 * ——一个斜杠、一个 ".."、第二个查询串——都无法存活到上游 URL 里。
 */
export function resolveUpstream(
  method: string,
  segments: readonly string[],
  search: URLSearchParams
): { path: string; search: string } | null {
  return resolve(ROUTES, method, segments, search);
}

/**
 * resolveOperatorUpstream is the same resolution against the operator table.
 *
 * resolveOperatorUpstream 是针对运维表的同一套求解。
 */
export function resolveOperatorUpstream(
  method: string,
  segments: readonly string[],
  search: URLSearchParams
): { path: string; search: string } | null {
  return resolve(OPERATOR_ROUTES, method, segments, search);
}

/** resolve matches a request against one table.
 *
 * resolve 把一次请求与一张表做匹配。 */
function resolve(
  routes: readonly UpstreamRoute[],
  method: string,
  segments: readonly string[],
  search: URLSearchParams
): { path: string; search: string } | null {
  const route = routes.find(
    (candidate) =>
      candidate.method === method.toUpperCase() &&
      matches(candidate.segments, segments)
  );
  if (!route) {
    return null;
  }

  const forwarded = new URLSearchParams();
  for (const name of route.query) {
    const value = search.get(name);
    if (value !== null) {
      forwarded.set(name, value);
    }
  }
  const query = forwarded.toString();
  return {
    path: `/${segments.map(encodeURIComponent).join("/")}`,
    search: query === "" ? "" : `?${query}`,
  };
}

/** matches reports whether a request's segments fit a route's pattern.
 *
 * matches 报告一次请求的各节是否契合某条路由的模式。 */
function matches(pattern: readonly Segment[], segments: readonly string[]) {
  if (pattern.length !== segments.length) {
    return false;
  }
  return pattern.every((slot, index) => {
    const segment = segments[index];
    if (segment === undefined || segment === "") {
      return false;
    }
    return "param" in slot ? isSafeParam(segment) : slot.literal === segment;
  });
}

/**
 * isSafeParam bounds what a placeholder accepts. A control plane id is an
 * opaque token of ordinary characters; refusing everything else here means a
 * traversal attempt is rejected as an unmatched route rather than relying on
 * the encoder further down to render it harmless.
 *
 * isSafeParam 限定占位符能接受什么。控制面的 id 是由普通字符构成的不透明串；在这里
 * 拒绝其余一切，意味着路径穿越的尝试会以「没有匹配的路由」被拒，而不是依赖下游的
 * 编码去把它变得无害。
 */
function isSafeParam(segment: string): boolean {
  if (segment === "." || segment === "..") {
    return false;
  }
  return segment.length <= 128 && /^[A-Za-z0-9._~-]+$/.test(segment);
}

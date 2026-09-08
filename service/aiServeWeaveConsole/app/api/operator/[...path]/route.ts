import { isTrustedWrite } from "@/lib/console/request-origin";
import { resolveOperatorUpstream } from "@/lib/console/upstream-routes";
import { callControlPlane } from "@/lib/server/control-plane";
import {
  errorResponse,
  jsonResponse,
  rawJsonResponse,
  readBoundedText,
} from "@/lib/server/responses";
import { clearOperatorSession, readOperatorSession } from "@/lib/server/operator-session";

/** Platform requests use only the independently sealed platform session and allowlist.
 * 平台请求仅使用独立密封的平台会话与白名单。 */

/** RouteContext is the catch-all segment Next.js hands the handler.
 *
 * RouteContext 是 Next.js 交给 handler 的通配路径段。 */
interface RouteContext {
  params: Promise<{ path: string[] }>;
}

/** GET forwards a read.
 *
 * GET 转发一次读取。 */
export async function GET(request: Request, context: RouteContext) {
  return forward(request, context);
}

/** POST forwards a creation.
 *
 * POST 转发一次创建。 */
export async function POST(request: Request, context: RouteContext) {
  return forward(request, context);
}

/** DELETE forwards a removal.
 *
 * DELETE 转发一次删除。 */
export async function DELETE(request: Request, context: RouteContext) {
  return forward(request, context);
}

/**
 * forward runs the four checks every call passes: the caller has a session,
 * the call is one the allowlist names, a write came from this origin, and the
 * body is bounded. Only then does anything leave for the control plane.
 *
 * forward 执行每次调用都要通过的四项检查：调用方持有会话、该调用在白名单之列、写操作
 * 来自本源、请求体在上限之内。只有全部通过，才会有东西发往控制面。
 */
async function forward(
  request: Request,
  context: RouteContext
): Promise<Response> {
  const session = await readOperatorSession();
  if (!session) {
    return errorResponse(401, "unauthorized");
  }

  const url = new URL(request.url);
  const { path } = await context.params;
  const upstream = resolveOperatorUpstream(request.method, path, url.searchParams);
  if (!upstream) {
    // An unlisted call is answered as not found rather than forbidden: the
    // Console has no such endpoint, and saying "forbidden" would suggest the
    // right session could reach it.
    //
    // 未在白名单中的调用以「不存在」作答而不是「被禁止」：Console 根本没有这个端点，
    // 说「被禁止」会暗示换一个会话就能到达它。
    return errorResponse(404, "not_found");
  }

  if (
    !isTrustedWrite({
      method: request.method,
      origin: request.headers.get("origin"),
      host: request.headers.get("host"),
      secFetchSite: request.headers.get("sec-fetch-site"),
    })
  ) {
    return errorResponse(403, "forbidden_origin");
  }

  let body: string | undefined;
  if (request.method !== "GET") {
    const raw = await readBoundedText(request);
    if (raw === null) {
      return errorResponse(413, "body_too_large");
    }
    body = raw.trim() === "" ? "{}" : raw;
  }

  const result = await callControlPlane({
    method: request.method,
    path: upstream.path,
    search: upstream.search,
    token: session.token,
    body,
  });
  if (result.kind !== "response") {
    return errorResponse(result.kind === "timeout" ? 504 : 502, "upstream");
  }

  if (result.status === 401) {
    // The control plane rejected the token this session holds, so the session
    // is over: clearing it here means the next page load goes to sign-in
    // rather than rendering a shell whose every request will fail.
    //
    // 控制面拒绝了本会话持有的令牌，因此会话已经结束：在这里清掉它，意味着下一次页面
    // 加载会走向登录，而不是渲染一个每个请求都会失败的外壳。
    await clearOperatorSession();
    return errorResponse(401, "unauthorized");
  }
  if (result.status === 204) {
    return jsonResponse(204, null);
  }
  if (result.status >= 400) {
    return errorResponse(result.status, "upstream_error");
  }
  return rawJsonResponse(result.status, result.text);
}

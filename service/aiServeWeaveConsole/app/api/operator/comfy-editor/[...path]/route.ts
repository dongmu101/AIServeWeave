import {
  EDITOR_FORWARDED_REQUEST_HEADERS,
  EDITOR_FORWARDED_RESPONSE_HEADERS,
  MAX_EDITOR_WRITE_BYTES,
  resolveEditorUpstream,
} from "@/lib/console/comfy-editor";
import { isTrustedWrite } from "@/lib/console/request-origin";
import { streamControlPlane } from "@/lib/server/control-plane";
import { errorResponse, readBoundedText } from "@/lib/server/responses";
import { clearOperatorSession, readOperatorSession } from "@/lib/server/operator-session";

/**
 * This route serves the embedded ComfyUI workflow editor (the official
 * ComfyUI_frontend release plus the control plane's stub backend) to the
 * operator Console, same-origin, so wujie can mount it. It is the one
 * Console route that is a prefix forward rather than an allowlist row: the
 * release has hundreds of hashed file names. What keeps it narrow instead is
 * that the upstream is pinned to the control plane's editor prefix, only the
 * platform session is attached, and the stub there refuses anything that
 * would execute or upload. Write bodies are bounded and then dropped here —
 * the stub discards them anyway, so nothing an operator does inside the
 * editor leaves the browser except through the ordinary publish endpoints.
 *
 * 本路由把嵌入式 ComfyUI 工作流编辑器（官方 ComfyUI_frontend 发布包加上控制面的桩
 * 后端）以同源方式提供给运维 Console，使 wujie 能挂载它。它是 Console 里唯一一条按
 * 前缀转发、而不是白名单逐行列举的路由：发布包有几百个带哈希的文件名。让它保持窄小的
 * 是另外几件事：上游被钉死在控制面的编辑器前缀上，只附加平台会话，而那边的桩后端拒绝
 * 一切会执行或上传的请求。写请求体在这里有界读取后直接丢弃——桩后端本来也会丢弃——
 * 因此运维在编辑器里做的任何事，除了经由普通发布端点之外，都不会离开浏览器。
 */

interface RouteContext {
  params: Promise<{ path: string[] }>;
}

/** GET serves a file or a stub read. / GET 提供一个文件或一次桩读取。 */
export async function GET(request: Request, context: RouteContext) {
  return forward(request, context);
}

/** HEAD mirrors GET without a body. / HEAD 与 GET 相同但不带响应体。 */
export async function HEAD(request: Request, context: RouteContext) {
  return forward(request, context);
}

/** POST is a stub write (accepted and discarded) or a refused execution. / POST 是一次桩写入（接受后丢弃）或一次被拒绝的执行。 */
export async function POST(request: Request, context: RouteContext) {
  return forward(request, context);
}

/** DELETE is a stub write, accepted and discarded. / DELETE 是一次桩写入，接受后丢弃。 */
export async function DELETE(request: Request, context: RouteContext) {
  return forward(request, context);
}

async function forward(request: Request, context: RouteContext): Promise<Response> {
  const session = await readOperatorSession();
  if (!session) return errorResponse(401, "unauthorized");

  const { path } = await context.params;
  const upstream = resolveEditorUpstream(request.method, path, new URL(request.url).search);
  if (!upstream) return errorResponse(404, "not_found");

  if (!isTrustedWrite({
    method: request.method,
    origin: request.headers.get("origin"),
    host: request.headers.get("host"),
    secFetchSite: request.headers.get("sec-fetch-site"),
  })) {
    return errorResponse(403, "forbidden_origin");
  }
  if (request.method === "POST" || request.method === "DELETE") {
    if ((await readBoundedText(request, MAX_EDITOR_WRITE_BYTES)) === null) return errorResponse(413, "body_too_large");
  }

  const requestHeaders = new Headers();
  for (const name of EDITOR_FORWARDED_REQUEST_HEADERS) {
    const value = request.headers.get(name);
    if (value !== null) requestHeaders.set(name, value);
  }
  const result = await streamControlPlane({
    method: request.method,
    path: upstream.path,
    search: upstream.search,
    token: session.token,
    requestHeaders,
    responseHeaders: EDITOR_FORWARDED_RESPONSE_HEADERS,
  });
  if (result.kind !== "response") return errorResponse(result.kind === "timeout" ? 504 : 502, "upstream");
  if (result.status === 401) {
    await result.body?.cancel();
    await clearOperatorSession();
    return errorResponse(401, "unauthorized");
  }
  if (result.status >= 300 && result.status < 400 && result.status !== 304) {
    // The stub never redirects; one arriving here is not followed or passed on.
    // 桩后端从不重定向；到达这里的重定向既不跟随也不转交。
    await result.body?.cancel();
    return errorResponse(502, "upstream");
  }
  result.headers.set("X-Content-Type-Options", "nosniff");
  if (!result.headers.has("cache-control")) result.headers.set("Cache-Control", "no-store");
  const empty = request.method === "HEAD" || result.status === 204 || result.status === 304;
  if (empty) await result.body?.cancel();
  return new Response(empty ? null : result.body, { status: result.status, headers: result.headers });
}

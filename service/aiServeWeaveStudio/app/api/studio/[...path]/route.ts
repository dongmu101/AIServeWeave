import { studioErrorCode } from "@/lib/studio/contract";
import { parseIssues } from "@/lib/studio/flows";
import { isTrustedWrite } from "@/lib/studio/request-origin";
import { resolveStudioRoute } from "@/lib/studio/upstream-routes";
import { errorResponse, jsonResponse, rawJsonResponse, readBoundedText } from "@/lib/server/responses";
import { readSession } from "@/lib/server/session";
import { callGateway } from "@/lib/server/upstream";

/**
 * The browser's only way to the Gateway: an allow-listed forward that adds
 * the session token from the sealed cookie. Error bodies are reduced to the
 * stable code, never the upstream's own message.
 *
 * 浏览器通往 Gateway 的唯一途径：一个按白名单转发、并从密封 Cookie 中补上会话令牌的
 * 入口。错误体只保留稳定错误码，绝不透传上游自己的 message；被拒绝的编排或运行还保留其
 * 问题列表，其中只有节点、字段与错误码。
 */

type Context = { params: Promise<{ path: string[] }> };

async function forward(request: Request, context: Context): Promise<Response> {
  if (!isTrustedWrite({
    method: request.method,
    origin: request.headers.get("origin"),
    host: request.headers.get("host"),
    secFetchSite: request.headers.get("sec-fetch-site"),
  })) {
    return errorResponse(403, "forbidden_origin");
  }
  const route = resolveStudioRoute(request.method, (await context.params).path, new URL(request.url).searchParams);
  if (!route) return errorResponse(404, "not_found");
  const session = await readSession();
  if (!session) return errorResponse(401, "unauthorized");

  let body: string | undefined;
  if (route.method !== "GET" && route.method !== "DELETE") {
    const raw = await readBoundedText(request, route.maxBody);
    if (raw === null) return errorResponse(413, "body_too_large");
    body = raw;
  }
  const result = await callGateway({ method: route.method, path: route.path, search: route.search, token: session.token, body });
  if (result.kind !== "response") return errorResponse(result.kind === "timeout" ? 504 : 502, "upstream");
  if (result.status >= 200 && result.status < 300) return rawJsonResponse(result.status, result.text);
  const code = studioErrorCode(result.text) || "upstream";
  if (result.status === 400) {
    let issues: ReturnType<typeof parseIssues> = [];
    try {
      issues = parseIssues(JSON.parse(result.text));
    } catch {
      issues = [];
    }
    if (issues.length > 0) return jsonResponse(400, { error: code, issues });
  }
  return errorResponse(result.status, code);
}

export const GET = forward;
export const POST = forward;
export const PUT = forward;
export const PATCH = forward;
export const DELETE = forward;

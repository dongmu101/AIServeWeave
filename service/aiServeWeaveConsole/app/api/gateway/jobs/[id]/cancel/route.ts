import { isTrustedWrite } from "@/lib/console/request-origin";
import { callGateway } from "@/lib/server/gateway";
import { errorResponse, rawJsonResponse } from "@/lib/server/responses";
import { readSession } from "@/lib/server/session";

/**
 * This is the browser's entry point to Gateway `POST /v1/jobs/{id}/cancel` —
 * the declared exception to "Console only talks to the control plane"
 * documented on SessionPayload.gatewayApiKey. Unlike
 * `app/api/admin/[...path]/route.ts`, there is no allowlist table here: this
 * file names the one Gateway path it may reach, and that is the allowlist.
 *
 * 这是浏览器进入 Gateway `POST /v1/jobs/{id}/cancel` 的入口——SessionPayload.gatewayApiKey
 * 上记录过的那个「Console 只与控制面对话」的声明例外。与
 * `app/api/admin/[...path]/route.ts` 不同，这里没有白名单表：本文件点名了它能够到达的
 * 唯一一个 Gateway 路径，那本身就是白名单。
 */

interface RouteContext {
  params: Promise<{ id: string }>;
}

export async function POST(request: Request, context: RouteContext) {
  const session = await readSession();
  if (!session) {
    return errorResponse(401, "unauthorized");
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

  const { id } = await context.params;
  const result = await callGateway({
    method: "POST",
    path: `/v1/jobs/${encodeURIComponent(id)}/cancel`,
    apiKey: session.gatewayApiKey,
  });

  switch (result.kind) {
    case "no_key":
      // A settings problem, not a job problem: distinct from every other
      // failure this route can report, and the one case the jobs page
      // should have already kept this button from being reachable for.
      //
      // 是设置问题，不是 job 问题：与本路由能报告的其他任何失败都不同，也是
      // job 页面本该早已让这个按钮够不到的那一种情况。
      return errorResponse(409, "gateway_key_missing");
    case "unreachable":
    case "timeout":
      return errorResponse(result.kind === "timeout" ? 504 : 502, "upstream");
    case "response":
      break;
  }

  const { response } = result;
  const text = await response.text();
  if (response.status === 401 || response.status === 403) {
    // The Gateway rejected this session's stored key — expired, revoked, or
    // never valid. This is reported distinctly from a plain "forbidden" so
    // the settings page can be pointed to directly, not just "try again".
    //
    // Gateway 拒绝了本会话存放的那把 key——过期、被吊销，或者从来就无效。这里
    // 用一个与普通「forbidden」不同的代号报告，好让界面能直接指向设置页，而
    // 不只是说「请重试」。
    return errorResponse(response.status, "gateway_key_rejected");
  }
  if (response.status >= 400) {
    return errorResponse(response.status, "upstream_error");
  }
  return rawJsonResponse(response.status, text);
}

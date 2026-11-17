import { parseLoginResult } from "@/lib/console/contract";
import { isTrustedWrite } from "@/lib/console/request-origin";
import { sealTicket, TICKET_TTL_MS } from "@/lib/console/studio-handoff";
import { callControlPlane } from "@/lib/server/control-plane";
import { studioHandoff } from "@/lib/server/config";
import { errorResponse, jsonResponse } from "@/lib/server/responses";
import { readSession } from "@/lib/server/session";

/**
 * POST signs the current Console user into Studio. It asks the control plane
 * for a second, independent session for the same account, seals it into a
 * short-lived ticket only Studio can open, and returns the ticket with the
 * Studio address the browser posts it to. The browser sees ciphertext, never
 * the token; logging out of either side leaves the other signed in.
 *
 * POST 让当前 Console 用户登录 Studio。它请控制面为同一账户另签一个独立会话，密封成只有
 * Studio 打得开的短时票据，连同浏览器要提交到的 Studio 地址一起返回。浏览器只见到密文，
 * 见不到令牌；退出任一边都不影响另一边。
 */
export async function POST(request: Request): Promise<Response> {
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
  const handoff = studioHandoff();
  if (!handoff) return errorResponse(404, "not_found");
  const session = await readSession();
  if (!session) return errorResponse(401, "unauthorized");

  const result = await callControlPlane({ method: "POST", path: "/admin/v1/auth/session/handoff", token: session.token });
  if (result.kind !== "response") return errorResponse(result.kind === "timeout" ? 504 : 502, "upstream");
  if (result.status !== 200) {
    return errorResponse(result.status === 401 || result.status === 403 ? 401 : 502, result.status === 401 || result.status === 403 ? "unauthorized" : "upstream");
  }
  let login;
  try {
    login = parseLoginResult(JSON.parse(result.text));
  } catch {
    return errorResponse(502, "upstream_contract");
  }
  const ticket = sealTicket(
    {
      token: login.token,
      expiresAt: login.expiresAt,
      notAfter: new Date(Date.now() + TICKET_TTL_MS).toISOString(),
      user: {
        id: login.user.id,
        tenantId: login.user.tenantId,
        email: login.user.email,
        name: login.user.name,
        role: login.user.role,
      },
    },
    handoff.key
  );
  return jsonResponse(200, { action: handoff.action, ticket });
}

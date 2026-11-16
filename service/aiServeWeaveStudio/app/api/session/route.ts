import { parseLogin } from "@/lib/studio/contract";
import { isTrustedWrite } from "@/lib/studio/request-origin";
import { logoutDisposition } from "@/lib/studio/session-revocation";
import { errorResponse, jsonResponse, readBoundedText } from "@/lib/server/responses";
import { clearSession, readSession, writeSession } from "@/lib/server/session";
import { callControlPlane } from "@/lib/server/upstream";

/**
 * This route owns the Studio session. The control plane token is obtained
 * here and sealed into the HttpOnly cookie; no response ever carries it back
 * to the browser.
 *
 * 本路由掌管 Studio 会话。控制面令牌在这里取得并密封进 HttpOnly Cookie；任何响应都不会
 * 把它带回浏览器。
 */

/** POST exchanges an email and password for a session cookie.
 * POST 用邮箱与密码换取会话 Cookie。 */
export async function POST(request: Request): Promise<Response> {
  if (!trusted(request)) return errorResponse(403, "forbidden_origin");
  const raw = await readBoundedText(request);
  if (raw === null) return errorResponse(413, "body_too_large");
  const credentials = credentialsFrom(raw);
  if (!credentials) return errorResponse(400, "invalid_request");

  const result = await callControlPlane({ method: "POST", path: "/admin/v1/auth/login", body: JSON.stringify(credentials) });
  if (result.kind !== "response") return errorResponse(result.kind === "timeout" ? 504 : 502, "upstream");
  if (result.status !== 200) {
    return errorResponse(result.status === 401 ? 401 : 502, result.status === 401 ? "invalid_credentials" : "upstream");
  }
  let login;
  try {
    login = parseLogin(JSON.parse(result.text));
  } catch {
    return errorResponse(502, "upstream_contract");
  }
  await writeSession({ token: login.token, expiresAt: login.expiresAt, user: login.user });
  return jsonResponse(200, { user: login.user, expiresAt: login.expiresAt });
}

/** DELETE revokes the control plane session, then clears the cookie. A
 * revocation that could not be confirmed keeps the cookie so the user can
 * retry instead of believing they signed out.
 *
 * DELETE 先吊销控制面会话，再清除 Cookie。吊销未能确认时保留 Cookie，让用户可以重试，
 * 而不是误以为已经登出。 */
export async function DELETE(request: Request): Promise<Response> {
  if (!trusted(request)) return errorResponse(403, "forbidden_origin");
  const session = await readSession();
  const result = session ? await callControlPlane({ method: "DELETE", path: "/admin/v1/auth/session", token: session.token }) : null;
  const disposition = logoutDisposition(result);
  if (!disposition.clear) return errorResponse(disposition.status, disposition.error!);
  await clearSession();
  return jsonResponse(204, null);
}

function trusted(request: Request): boolean {
  return isTrustedWrite({
    method: request.method,
    origin: request.headers.get("origin"),
    host: request.headers.get("host"),
    secFetchSite: request.headers.get("sec-fetch-site"),
  });
}

function credentialsFrom(raw: string): { email: string; password: string } | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null) return null;
  const { email, password } = parsed as Record<string, unknown>;
  if (typeof email !== "string" || typeof password !== "string" || email.trim() === "") return null;
  return { email: email.trim(), password };
}

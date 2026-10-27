import { isTrustedWrite } from "@/lib/console/request-origin";
import { logoutDisposition } from "@/lib/console/session-revocation";
import { callControlPlane } from "@/lib/server/control-plane";
import {
  errorResponse,
  jsonResponse,
  readBoundedText,
} from "@/lib/server/responses";
import { clearSession, readSession } from "@/lib/server/session";
import { exchangeCredentials } from "@/lib/server/sign-in";

/**
 * This route owns the Console session. Sign-in is deliberately not part of
 * the forwarding allowlist: the token is obtained only in
 * lib/server/sign-in.ts, which this route and the sign-up route share, so it
 * has exactly one path into a cookie and no path back out to the browser. The
 * response says who signed in; it does not carry the token, and neither does
 * any later response.
 *
 * 本路由掌管 Console 会话。登录刻意不属于转发白名单：令牌只在本路由与注册路由共用的
 * lib/server/sign-in.ts 里取得，因此它进入 cookie 只有一条路径，而回到浏览器则没有
 * 任何路径。响应说明谁登录了；它不携带令牌，之后的任何响应也不携带。
 */

/**
 * POST exchanges an email and password for a session cookie.
 *
 * POST 用邮箱与密码换取一个会话 cookie。
 */
export async function POST(request: Request): Promise<Response> {
  if (!trusted(request)) {
    return errorResponse(403, "forbidden_origin");
  }

  const raw = await readBoundedText(request);
  if (raw === null) {
    return errorResponse(413, "body_too_large");
  }
  const credentials = credentialsFrom(raw);
  if (!credentials) {
    return errorResponse(400, "invalid_request");
  }
  return exchangeCredentials(credentials);
}

/**
 * DELETE ends the Console session.
 *
 * DELETE 结束 Console 会话。
 */
export async function DELETE(request: Request): Promise<Response> {
  if (!trusted(request)) {
    return errorResponse(403, "forbidden_origin");
  }
  const session = await readSession();
  const result = session
    ? await callControlPlane({ method: "DELETE", path: "/admin/v1/auth/session", token: session.token })
    : null;
  const disposition = logoutDisposition(result);
  if (!disposition.clear) {
    return errorResponse(disposition.status, disposition.error!);
  }
  await clearSession();
  return jsonResponse(204, null);
}

/** trusted applies the same-origin check to a state-changing request.
 *
 * trusted 对一次改变状态的请求施加同源检查。 */
function trusted(request: Request): boolean {
  return isTrustedWrite({
    method: request.method,
    origin: request.headers.get("origin"),
    host: request.headers.get("host"),
    secFetchSite: request.headers.get("sec-fetch-site"),
  });
}

/**
 * credentialsFrom validates the sign-in body. It checks shape only: whether
 * the credentials are correct is the control plane's answer to give, and a
 * Console that pre-judged it would be describing a rule it does not own.
 *
 * credentialsFrom 校验登录请求体。它只检查形状：凭据是否正确由控制面作答，Console 若
 * 抢先判断，就是在描述一条并不归它所有的规则。
 */
function credentialsFrom(
  raw: string
): { email: string; password: string } | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null) {
    return null;
  }
  const { email, password } = parsed as Record<string, unknown>;
  if (typeof email !== "string" || typeof password !== "string") {
    return null;
  }
  if (email === "") {
    return null;
  }
  return { email, password };
}

import { parseOperatorLogin } from "@/lib/console/operator-session";
import { isTrustedWrite } from "@/lib/console/request-origin";
import { callControlPlane } from "@/lib/server/control-plane";
import {
  errorResponse,
  jsonResponse,
  readBoundedText,
} from "@/lib/server/responses";
import { clearOperatorSession, writeOperatorSession } from "@/lib/server/operator-session";

/** This route obtains platform JWTs and seals them only into the independent operator cookie.
 * 本路由取得平台 JWT，仅将其密封到独立运维 Cookie；响应不携带令牌。 */

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

  // The password is forwarded in this one body and is held nowhere else: not
  // in a variable that outlives this call, not in a log line, not in the
  // session that follows.
  //
  // 密码只在这一个请求体中被转发，别处一概不留：不留在活得比本次调用更久的变量里，
  // 不留在日志行里，也不留在随后建立的会话里。
  const result = await callControlPlane({
    method: "POST",
    path: "/admin/v1/platform/auth/login",
    body: JSON.stringify(credentials),
  });
  if (result.kind !== "response") {
    return errorResponse(result.kind === "timeout" ? 504 : 502, "upstream");
  }
  if (result.status !== 200) {
    // 401 is passed through as itself: a wrong password must not look like a
    // Console failure the user should retry differently.
    //
    // 401 原样透传：密码错误不该看起来像是一个需要用户换个方式重试的 Console 故障。
    return errorResponse(
      result.status === 401 ? 401 : 502,
      result.status === 401 ? "invalid_credentials" : "upstream"
    );
  }

  let login;
  try {
    login = parseOperatorLogin(JSON.parse(result.text));
  } catch {
    return errorResponse(502, "upstream_contract");
  }

  await writeOperatorSession(login);
  return jsonResponse(200, { operator: login.operator, expiresAt: login.expiresAt });
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
  await clearOperatorSession();
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

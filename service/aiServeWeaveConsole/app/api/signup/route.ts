import { isTrustedWrite } from "@/lib/console/request-origin";
import {
  parseSignupBody,
  upstreamSignupDisposition,
} from "@/lib/console/signup";
import { serverConfig } from "@/lib/server/config";
import { callControlPlane } from "@/lib/server/control-plane";
import { errorResponse, readBoundedText } from "@/lib/server/responses";
import { exchangeCredentials } from "@/lib/server/sign-in";

/**
 * POST creates a tenant and its owner through the control plane's public
 * sign-up endpoint, then signs the new owner in with the same credentials so
 * the person lands in the console rather than on a sign-in form. Like the
 * sign-in route, it is not part of the forwarding allowlist: it holds a
 * password and ends in a session cookie, and neither belongs in a generic
 * forwarder.
 *
 * When the Console does not offer sign-up, the route answers 404 without
 * calling upstream, matching the page itself.
 *
 * If the tenant is created but the follow-up sign-in fails, the error from
 * that sign-in is returned. The account exists, and signing in by hand works
 * once whatever failed recovers; retrying the sign-up would only report the
 * email as taken.
 *
 * POST 经控制面的公开注册端点创建一个租户及其 owner，然后用同一组凭据为新 owner 登录，
 * 让人直接进入控制台而不是停在登录表单上。与登录路由一样，它不属于转发白名单：它经手
 * 密码、以会话 cookie 收尾，两者都不该出现在通用转发器里。
 *
 * Console 不提供注册时，本路由不调用上游直接回答 404，与页面本身一致。
 *
 * 如果租户已创建而随后的登录失败，返回的是那次登录的错误。账户已经存在，待失败的那一环
 * 恢复后手动登录即可；重试注册只会得到「邮箱已注册」。
 */
export async function POST(request: Request): Promise<Response> {
  if (!serverConfig().signupEnabled) {
    return errorResponse(404, "signup_disabled");
  }
  const trusted = isTrustedWrite({
    method: request.method,
    origin: request.headers.get("origin"),
    host: request.headers.get("host"),
    secFetchSite: request.headers.get("sec-fetch-site"),
  });
  if (!trusted) {
    return errorResponse(403, "forbidden_origin");
  }

  const raw = await readBoundedText(request);
  if (raw === null) {
    return errorResponse(413, "body_too_large");
  }
  const credentials = parseSignupBody(raw);
  if (!credentials) {
    return errorResponse(400, "invalid_request");
  }

  const result = await callControlPlane({
    method: "POST",
    path: "/admin/v1/auth/signup",
    body: JSON.stringify({
      tenant_name: credentials.tenantName,
      email: credentials.email,
      password: credentials.password,
    }),
  });
  if (result.kind !== "response") {
    return errorResponse(result.kind === "timeout" ? 504 : 502, "upstream");
  }
  const disposition = upstreamSignupDisposition(result.status);
  if (!disposition.proceed) {
    return errorResponse(disposition.status, disposition.code);
  }

  return exchangeCredentials({
    email: credentials.email,
    password: credentials.password,
  });
}

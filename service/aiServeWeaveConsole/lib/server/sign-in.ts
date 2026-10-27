import { parseLoginResult } from "@/lib/console/contract";
import { callControlPlane } from "@/lib/server/control-plane";
import { errorResponse, jsonResponse } from "@/lib/server/responses";
import { writeSession } from "@/lib/server/session";

/**
 * exchangeCredentials signs in against the control plane, seals the resulting
 * token into the session cookie, and returns the response the browser gets.
 * It is the one place a tenant control plane token is obtained: the sign-in
 * route calls it directly, and the sign-up route calls it once the tenant
 * exists. The response says who signed in; it never carries the token.
 *
 * exchangeCredentials 向控制面登录，把得到的令牌密封进会话 cookie，并返回浏览器拿到的
 * 响应。它是取得租户控制面令牌的唯一位置：登录路由直接调用它，注册路由在租户建好之后
 * 调用它。响应说明谁登录了，绝不携带令牌。
 */
export async function exchangeCredentials(credentials: {
  email: string;
  password: string;
}): Promise<Response> {
  // The password is forwarded in this one body and is held nowhere else: not
  // in a variable that outlives this call, not in a log line, not in the
  // session that follows.
  //
  // 密码只在这一个请求体中被转发，别处一概不留：不留在活得比本次调用更久的变量里，
  // 不留在日志行里，也不留在随后建立的会话里。
  const result = await callControlPlane({
    method: "POST",
    path: "/admin/v1/auth/login",
    body: JSON.stringify({
      email: credentials.email,
      password: credentials.password,
    }),
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
    login = parseLoginResult(JSON.parse(result.text));
  } catch {
    return errorResponse(502, "upstream_contract");
  }

  const user = {
    id: login.user.id,
    tenantId: login.user.tenantId,
    email: login.user.email,
    name: login.user.name,
    role: login.user.role,
  };
  await writeSession({ token: login.token, expiresAt: login.expiresAt, user });
  return jsonResponse(200, { user, expiresAt: login.expiresAt });
}

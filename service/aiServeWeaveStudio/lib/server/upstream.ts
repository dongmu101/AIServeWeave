import { serverConfig } from "@/lib/server/config";

/**
 * Calls to Studio's two upstreams. The base address always comes from
 * configuration; the browser never names a host. Nothing about a call — path,
 * body or token — is logged: a sign-in body carries a password and every
 * Studio call carries a session token.
 *
 * 对 Studio 两个上游的调用。基地址一律来自配置，浏览器从不指定主机。调用的任何部分——
 * 路径、请求体或令牌——都不写日志：登录请求体里有密码，每个 Studio 调用都带着会话令牌。
 */

export type UpstreamResult =
  | { kind: "response"; status: number; text: string }
  | { kind: "unreachable" }
  | { kind: "timeout" };

export interface UpstreamCall {
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  path: string;
  search?: string;
  token?: string;
  body?: string;
}

/** callControlPlane is for sign-in and sign-out only.
 * callControlPlane 只用于登录与登出。 */
export function callControlPlane(call: UpstreamCall): Promise<UpstreamResult> {
  return call_(serverConfig().controlPlaneUrl, call);
}

/** callGateway calls the Gateway's Studio API with the session token.
 * callGateway 携带会话令牌调用 Gateway 的 Studio API。 */
export function callGateway(call: UpstreamCall & { token: string }): Promise<UpstreamResult> {
  return call_(serverConfig().gatewayUrl, call);
}

async function call_(base: string, call: UpstreamCall): Promise<UpstreamResult> {
  const config = serverConfig();
  const headers: Record<string, string> = { Accept: "application/json" };
  if (call.token) headers.Authorization = `Bearer ${call.token}`;
  if (call.body !== undefined) headers["Content-Type"] = "application/json";
  try {
    const response = await fetch(`${base}${call.path}${call.search ?? ""}`, {
      method: call.method,
      headers,
      body: call.body,
      cache: "no-store",
      redirect: "manual",
      signal: AbortSignal.timeout(config.timeoutMs),
    });
    return { kind: "response", status: response.status, text: await response.text() };
  } catch (error) {
    return error instanceof DOMException && error.name === "TimeoutError" ? { kind: "timeout" } : { kind: "unreachable" };
  }
}

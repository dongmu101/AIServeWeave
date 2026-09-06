import { serverConfig } from "@/lib/server/config";

/**
 * This module is the only place in the Console that opens a connection to the
 * control plane. The upstream address comes from configuration, never from a
 * request, and the session token is attached here — so no caller can aim a
 * call somewhere else or send the token to a host of its choosing.
 *
 * 本模块是 Console 中唯一会向控制面发起连接的地方。上游地址来自配置而绝不来自请求，
 * 会话令牌也在这里附加 —— 因此没有任何调用方能把一次调用指向别处，或把令牌发给它
 * 自选的主机。
 */

/**
 * UpstreamResult is what one call to the control plane produced. A transport
 * failure is a distinct case rather than a synthesized status, so a caller
 * cannot confuse "the control plane said 503" with "there was nothing there".
 *
 * UpstreamResult 是一次控制面调用的产出。传输失败是一个独立的情形，而不是一个被合成
 * 出来的状态码，因此调用方不会把「控制面回了 503」与「那边根本没东西」搞混。
 */
export type UpstreamResult =
  | { kind: "response"; status: number; text: string }
  | { kind: "unreachable" }
  | { kind: "timeout" };

/**
 * UpstreamCall is one Admin API call, already resolved against the allowlist.
 *
 * UpstreamCall 是一次已对照白名单求解过的 Admin API 调用。
 */
export interface UpstreamCall {
  method: string;
  /** path is the upstream path, rebuilt by resolveUpstream.
   *
   * path 是上游路径，由 resolveUpstream 重建。 */
  path: string;
  search?: string;
  /** token is the control plane session token, absent for sign-in.
   *
   * token 是控制面会话令牌，登录时不带。 */
  token?: string;
  /** body is a JSON document, already serialized.
   *
   * body 是一个已序列化的 JSON 文档。 */
  body?: string;
}

/**
 * callControlPlane performs one call and reads its body as text.
 *
 * The body is read as text rather than parsed here because this layer does not
 * interpret it: a forwarded response is handed on as it arrived, and a parsed
 * one is only re-serialized. Nothing about the request — not the path, not the
 * body, not the token — is logged: a sign-in body holds a password and a
 * created key's response holds a plaintext credential.
 *
 * callControlPlane 执行一次调用，并把响应体按文本读取。
 *
 * 这里按文本读取而不解析，是因为本层不解释它：被转发的响应原样交出去，解析过的也只是
 * 再序列化一次。请求的任何部分——路径、请求体、令牌——都不写日志：一次登录的请求体里有
 * 密码，一次创建 key 的响应里有明文凭据。
 */
export async function callControlPlane(
  call: UpstreamCall
): Promise<UpstreamResult> {
  const config = serverConfig();
  const url = `${config.controlPlaneUrl}${call.path}${call.search ?? ""}`;
  const headers: Record<string, string> = { Accept: "application/json" };
  if (call.token) {
    headers.Authorization = `Bearer ${call.token}`;
  }
  if (call.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }

  try {
    const response = await fetch(url, {
      method: call.method,
      headers,
      body: call.body,
      cache: "no-store",
      signal: AbortSignal.timeout(config.timeoutMs),
    });
    return { kind: "response", status: response.status, text: await response.text() };
  } catch (error) {
    return isTimeout(error) ? { kind: "timeout" } : { kind: "unreachable" };
  }
}

/** isTimeout reports whether a fetch rejection was this call's own deadline.
 *
 * isTimeout 报告一次 fetch 的失败是否源于本次调用自己的超时。 */
function isTimeout(error: unknown): boolean {
  return error instanceof DOMException && error.name === "TimeoutError";
}

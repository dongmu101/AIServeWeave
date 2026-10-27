import { readBoundedText } from "./responses";
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
  /** maxResponseBytes bounds route publication responses. / maxResponseBytes 限制路由发布响应。 */
  maxResponseBytes?: number;
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
    const text = call.maxResponseBytes === undefined ? await response.text() : await readBoundedText(response, call.maxResponseBytes);
    if (text === null) return { kind: "unreachable" };
    return { kind: "response", status: response.status, text };
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

/**
 * StreamedResult is one streamed call's outcome: status, the allow-listed
 * response headers and the body as it arrives. / StreamedResult 是一次流式调用的产出：
 * 状态码、允许列表内的响应头，以及按到达顺序的响应体。
 */
export type StreamedResult =
  | { kind: "response"; status: number; headers: Headers; body: ReadableStream<Uint8Array> | null }
  | { kind: "unreachable" }
  | { kind: "timeout" };

/**
 * StreamedCall is one body-less call whose response is passed on without
 * buffering. / StreamedCall 是一次不带请求体、响应不经缓冲直接转交的调用。
 */
export interface StreamedCall {
  method: string;
  path: string;
  search?: string;
  token: string;
  /** requestHeaders are copied upstream; the caller already restricted them. / requestHeaders 原样带往上游，调用方已限制其范围。 */
  requestHeaders?: Headers;
  /** responseHeaders names the response headers passed back. / responseHeaders 指明回传哪些响应头。 */
  responseHeaders: readonly string[];
}

/**
 * streamControlPlane performs one call and hands its body on as a stream,
 * for the embedded workflow editor's static release, whose bundles are
 * megabytes each: buffering them as text would hold every one in memory and
 * mangle the binary ones. The same rule as callControlPlane applies — the
 * address comes from configuration and nothing about the call is logged.
 *
 * streamControlPlane 执行一次调用，并把响应体作为流转交出去，服务于嵌入式工作流
 * 编辑器的静态发布包——其中每个打包文件都有若干兆：把它们当文本缓冲，会把每一个都
 * 留在内存里，并损坏其中的二进制文件。与 callControlPlane 相同的规则适用：地址来自
 * 配置，调用的任何部分都不写日志。
 */
export async function streamControlPlane(call: StreamedCall): Promise<StreamedResult> {
  const config = serverConfig();
  const url = `${config.controlPlaneUrl}${call.path}${call.search ?? ""}`;
  const headers = new Headers(call.requestHeaders);
  headers.set("Authorization", `Bearer ${call.token}`);
  // The deadline covers reaching the control plane and receiving its headers,
  // not the body: a multi-megabyte bundle to a slow browser legitimately takes
  // longer than any request timeout, and AbortSignal.timeout would cut it off
  // mid-stream. A client that goes away cancels the body instead.
  // 截止时间只覆盖连上控制面并收到响应头，不覆盖响应体：几兆的打包文件送往慢速浏览器
  // 本来就会超过任何请求超时，AbortSignal.timeout 会把它从中间截断。客户端离开时
  // 取消的是响应体本身。
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new DOMException("upstream headers timed out", "TimeoutError")), config.timeoutMs);
  try {
    const response = await fetch(url, {
      method: call.method,
      headers,
      cache: "no-store",
      redirect: "manual",
      signal: controller.signal,
    });
    const passed = new Headers();
    for (const name of call.responseHeaders) {
      const value = response.headers.get(name);
      if (value !== null) passed.set(name, value);
    }
    return { kind: "response", status: response.status, headers: passed, body: response.body };
  } catch (error) {
    return isTimeout(error) ? { kind: "timeout" } : { kind: "unreachable" };
  } finally {
    clearTimeout(timer);
  }
}

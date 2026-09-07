import { serverConfig } from "@/lib/server/config";

/**
 * This module is the only place in the Console that opens a connection to the
 * Gateway's data plane. It exists only for the one declared exception to
 * "Console only talks to the control plane" — see
 * SessionPayload.gatewayApiKey for why it is not a violation of that rule but
 * a narrow, named carve-out from it.
 *
 * Unlike callControlPlane, this returns the raw fetch Response rather than
 * buffered text. A cancel call's body is a few bytes of JSON either way works
 * for, but an artifact can be an image or a video of unbounded size, and this
 * repository's own rule against unbounded buffering applies here exactly as
 * it does in the Go services: the caller streams the body straight into the
 * outgoing Response instead of holding it in this process.
 *
 * 本模块是 Console 中唯一会向 Gateway 数据面发起连接的地方。它的存在只为了那一个
 * 声明过的例外——「Console 只与控制面对话」这条规则唯一的例外——为什么这不是违反
 * 那条规则、而是从中划出的一处窄小的、有名字的例外，见 SessionPayload.gatewayApiKey。
 *
 * 与 callControlPlane 不同，这里返回的是原始的 fetch Response，而不是缓冲好的文本。
 * 一次取消调用的响应体不管哪种方式都只有几个字节的 JSON，但一个产物可能是大小不受
 * 限制的图片或视频，本仓库那条反对无界缓冲的规则在这里与它在 Go 服务里的成立方式
 * 完全相同：调用方把响应体直接串流进发出去的 Response，而不是先在本进程里持有它。
 */

/** GatewayResult is what one call to the Gateway produced. See
 * UpstreamResult in control-plane.ts for why a transport failure is its own
 * case rather than a synthesized status.
 *
 * GatewayResult 是一次 Gateway 调用的产出。为什么传输失败是独立的一种情形而不是
 * 合成出来的状态码，见 control-plane.ts 的 UpstreamResult。 */
export type GatewayResult =
  | { kind: "response"; response: Response }
  | { kind: "unreachable" }
  | { kind: "timeout" }
  /** noKey means this session has not configured a gatewayApiKey — the
   * caller never even attempted a request.
   *
   * noKey 表示本会话尚未配置 gatewayApiKey——调用方压根没有尝试发起请求。 */
  | { kind: "no_key" };

/** GatewayCall is one call to the Gateway data plane.
 *
 * GatewayCall 是一次对 Gateway 数据面的调用。 */
export interface GatewayCall {
  method: string;
  /** path is a Gateway data-plane path such as "/v1/jobs/job_123/cancel".
   *
   * path 是形如 "/v1/jobs/job_123/cancel" 的 Gateway 数据面路径。 */
  path: string;
  /** apiKey is the tenant's own Gateway API Key, read from the session. A
   * missing key is GatewayResult's "no_key", not an empty Authorization
   * header sent and rejected by the Gateway — the two would look the same to
   * a person but the first is this Console's own state, and conflating them
   * would blame the Gateway for something this service never configured.
   *
   * apiKey 是租户自己的 Gateway API Key，从会话中读取。key 缺失对应
   * GatewayResult 的 "no_key"，而不是发出一个空的 Authorization 头再被
   * Gateway 拒绝——这两者在人看来很像，但前者是本服务自己的状态，把两者混为
   * 一谈，会把一件本服务从未配置好的事怪到 Gateway 头上。 */
  apiKey: string | undefined;
}

/**
 * callGateway performs one call and hands back the raw response for the
 * caller to read as it sees fit. Nothing about the request — the path, the
 * key — is logged, for the same reason callControlPlane never logs a sign-in
 * body: this key authenticates real inference spend and every action on a
 * tenant's runs, and it is exactly the sort of thing the root AGENTS.md's
 * 安全红线 keeps out of logs.
 *
 * callGateway 执行一次调用，并把原始响应交还给调用方按需读取。请求的任何部分——
 * 路径、key——都不记日志，理由与 callControlPlane 从不记录登录请求体相同：这把 key
 * 认证的是真实的推理开销与对一个租户全部运行的每一个操作，正是根 AGENTS.md「安全
 * 红线」要求不得进日志的那一类东西。
 */
export async function callGateway(call: GatewayCall): Promise<GatewayResult> {
  if (!call.apiKey) {
    return { kind: "no_key" };
  }
  const config = serverConfig();
  const url = `${config.gatewayUrl}${call.path}`;
  try {
    const response = await fetch(url, {
      method: call.method,
      headers: { Authorization: `Bearer ${call.apiKey}` },
      cache: "no-store",
      signal: AbortSignal.timeout(config.timeoutMs),
    });
    return { kind: "response", response };
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

import { callGateway } from "@/lib/server/gateway";
import { errorResponse } from "@/lib/server/responses";
import { readSession } from "@/lib/server/session";

/**
 * This is the browser's entry point to Gateway `GET /v1/artifacts/{id}` —
 * the read half of the declared exception documented on
 * SessionPayload.gatewayApiKey. It is meant to be used as a plain
 * `<a href>` navigation (or an `<img src>`/`<video src>`), not through the
 * JSON request layer in lib/console/api-client.ts: an artifact can be a
 * multi-megabyte image or video, and that layer's contract is "parse this as
 * JSON", which a binary body is not.
 *
 * The response body is piped straight through from the Gateway's own
 * fetch Response to this route's Response, unread and unbuffered in
 * between — the same "边读边送" rule the Gateway's own artifacts.go follows
 * on the hop before this one, continued rather than broken on this hop.
 *
 * 这是浏览器进入 Gateway `GET /v1/artifacts/{id}` 的入口——
 * SessionPayload.gatewayApiKey 上记录过的那个声明例外里负责读取的那一半。它
 * 应当以一次普通的 `<a href>` 导航（或 `<img src>`/`<video src>`）来使用，而不是
 * 经由 lib/console/api-client.ts 里的 JSON 请求层：一个产物可能是数兆字节的图片
 * 或视频，那一层的契约是「把这个解析成 JSON」，而一段二进制响应体不是。
 *
 * 响应体直接从 Gateway 自己那次 fetch 得到的 Response，串流进本路由的 Response，
 * 中间不读取、不缓冲——这与 Gateway 自己的 artifacts.go 在上一跳所遵循的「边读边送」
 * 是同一条规则，在这一跳延续，而不是被打破。
 */

interface RouteContext {
  params: Promise<{ id: string }>;
}

export async function GET(_request: Request, context: RouteContext) {
  const session = await readSession();
  if (!session) {
    return errorResponse(401, "unauthorized");
  }

  const { id } = await context.params;
  const result = await callGateway({
    method: "GET",
    path: `/v1/artifacts/${encodeURIComponent(id)}`,
    apiKey: session.gatewayApiKey,
  });

  switch (result.kind) {
    case "no_key":
      return errorResponse(409, "gateway_key_missing");
    case "unreachable":
    case "timeout":
      return errorResponse(result.kind === "timeout" ? 504 : 502, "upstream");
    case "response":
      break;
  }

  const { response } = result;
  if (response.status === 401 || response.status === 403) {
    return errorResponse(response.status, "gateway_key_rejected");
  }
  if (response.status >= 400) {
    return errorResponse(response.status, "upstream_error");
  }

  // Only the three headers artifacts.go actually sets are forwarded — an
  // allowlist here for the same reason the request side has one: passing
  // every upstream header through unexamined would let the Gateway (or
  // whatever is between this Console and it) set something this response
  // was never meant to carry, such as a cache directive that lets a shared
  // cache hold one tenant's generated image.
  //
  // 这里只转发 artifacts.go 真正会设置的那三个头——设一份白名单，理由与请求
  // 那一侧设白名单相同：不加甄别地透传每一个上游响应头，会让 Gateway（或本
  // Console 与它之间的任何东西）设置一些这个响应本不该携带的东西，比如一条
  // 会让共享缓存留下某个租户生成图像的缓存指令。
  const headers = new Headers({ "Cache-Control": "no-store" });
  for (const name of ["content-type", "content-length", "content-disposition"]) {
    const value = response.headers.get(name);
    if (value) {
      headers.set(name, value);
    }
  }

  return new Response(response.body, { status: response.status, headers });
}

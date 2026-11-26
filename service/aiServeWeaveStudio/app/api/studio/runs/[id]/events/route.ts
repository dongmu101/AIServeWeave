import { studioErrorCode } from "@/lib/studio/contract";
import { serverConfig } from "@/lib/server/config";
import { errorResponse } from "@/lib/server/responses";
import { readSession } from "@/lib/server/session";

/**
 * A flow run's event stream: authenticates the watcher and relays the
 * Gateway's SSE bytes untouched. The browser parses the frames
 * (lib/studio/flow-run.ts). Closing the page only stops watching; the run
 * goes on on the Gateway.
 *
 * 编排运行的事件流：鉴权观察者，并原样转发 Gateway 的 SSE 字节。帧由浏览器解析
 * （lib/studio/flow-run.ts）。关闭页面只是停止观察，运行仍在 Gateway 上继续。
 */

const RUN_ID = /^frn_[A-Za-z0-9_-]{1,36}$/;
// A run lasts at most 30 minutes; the watch ends a little after.
// 一次运行最长 30 分钟；观察稍晚于此结束。
const MAX_WATCH_MS = 35 * 60_000;

type Context = { params: Promise<{ id: string }> };

export async function GET(request: Request, context: Context): Promise<Response> {
  const { id } = await context.params;
  if (!RUN_ID.test(id)) return errorResponse(404, "not_found");
  const session = await readSession();
  if (!session) return errorResponse(401, "unauthorized");

  let upstream: Response;
  try {
    upstream = await fetch(`${serverConfig().gatewayUrl}/studio/v1/runs/${id}/events`, {
      headers: { Authorization: `Bearer ${session.token}`, Accept: "text/event-stream" },
      cache: "no-store",
      redirect: "manual",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(MAX_WATCH_MS)]),
    });
  } catch {
    return errorResponse(502, "upstream");
  }
  if (!upstream.ok || !upstream.body) {
    const code = studioErrorCode(await upstream.text().catch(() => "")) || (upstream.status === 401 ? "unauthorized" : "upstream");
    return errorResponse(upstream.status === 200 ? 502 : upstream.status, code);
  }
  // Only the frames reach the browser, none of the Gateway's headers.
  // 到达浏览器的只有帧，不含 Gateway 的任何响应头。
  return new Response(upstream.body, {
    headers: { "Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-store", "X-Accel-Buffering": "no" },
  });
}

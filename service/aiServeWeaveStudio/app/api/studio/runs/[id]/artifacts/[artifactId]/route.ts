import { studioErrorCode } from "@/lib/studio/contract";
import { ARTIFACT_ID_PATTERN, RUN_ID_PATTERN, artifactResponseHeaders } from "@/lib/studio/artifacts";
import { isTrustedWrite } from "@/lib/studio/request-origin";
import { serverConfig } from "@/lib/server/config";
import { errorResponse } from "@/lib/server/responses";
import { readSession } from "@/lib/server/session";

/**
 * A run's artifact (a generated image or file): authenticates the downloader
 * and streams the Gateway's bytes through without buffering them, so an
 * artifact of many megabytes costs no memory here and has no size bound of
 * its own. Only the type, length and disposition the Gateway gave are passed
 * on, with headers that keep the bytes inert in the browser.
 *
 * 一次运行的产物（生成的图片或文件）：鉴权下载者，并把 Gateway 的字节不经缓冲地流式转发，
 * 因此几十兆的产物在这里不占内存，也没有自己的大小上限。只传递 Gateway 给出的类型、长度与
 * disposition，并附上让这些字节在浏览器中保持惰性的头。
 */

// A download may be slow on a poor link, but not endless.
// 弱网下载可能很慢，但不能没有尽头。
const MAX_DOWNLOAD_MS = 15 * 60_000;

type Context = { params: Promise<{ id: string; artifactId: string }> };

export async function GET(request: Request, context: Context): Promise<Response> {
  if (!isTrustedWrite({
    method: request.method,
    origin: request.headers.get("origin"),
    host: request.headers.get("host"),
    secFetchSite: request.headers.get("sec-fetch-site"),
  })) {
    return errorResponse(403, "forbidden_origin");
  }
  const { id, artifactId } = await context.params;
  if (!RUN_ID_PATTERN.test(id) || !ARTIFACT_ID_PATTERN.test(artifactId)) return errorResponse(404, "not_found");
  const session = await readSession();
  if (!session) return errorResponse(401, "unauthorized");

  let upstream: Response;
  try {
    upstream = await fetch(`${serverConfig().gatewayUrl}/studio/v1/runs/${id}/artifacts/${artifactId}`, {
      // Identity: the length passed on must be the length of the bytes passed on.
      // 不压缩：传递的长度必须是所传字节的长度。
      headers: { Authorization: `Bearer ${session.token}`, "Accept-Encoding": "identity" },
      cache: "no-store",
      redirect: "manual",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(MAX_DOWNLOAD_MS)]),
    });
  } catch {
    return errorResponse(502, "upstream");
  }
  if (!upstream.ok || !upstream.body) {
    const code = studioErrorCode(await upstream.text().catch(() => "")) || (upstream.status === 401 ? "unauthorized" : "upstream");
    return errorResponse(upstream.status >= 400 && upstream.status < 600 ? upstream.status : 502, code);
  }
  return new Response(upstream.body, { headers: artifactResponseHeaders(upstream.headers) });
}

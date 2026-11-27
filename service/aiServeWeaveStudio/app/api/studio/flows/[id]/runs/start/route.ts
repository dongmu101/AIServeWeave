import { isTrustedWrite } from "@/lib/studio/request-origin";
import { serverConfig } from "@/lib/server/config";
import { gatewayAnswer } from "@/lib/server/gateway-answer";
import { errorResponse } from "@/lib/server/responses";
import { readSession } from "@/lib/server/session";

/**
 * Starting a run: authenticates the starter and streams the request body to
 * the Gateway without buffering it. A run with image or file inputs is a
 * multipart/form-data upload of up to the Gateway's own bound (200 MiB), which
 * must not sit in this process's memory; so the body goes through as a
 * stream, with its content type (the multipart boundary) and length, and this
 * route sets no size bound of its own — the Gateway answers 413 when it is
 * too large, and 400 with issues when an input or file is refused. A run
 * without files is the same route with a JSON body.
 *
 * 启动运行：鉴权启动者，并把请求体不经缓冲地流式转发给 Gateway。带 image 或 file 输入的运行是
 * multipart/form-data 上传，大小最大到 Gateway 自己的上限（200 MiB），不能放在本进程内存里；
 * 因此请求体以流的方式转发，连同其内容类型（multipart 分隔符）与长度，本路由不设自己的大小
 * 上限——过大由 Gateway 回答 413，输入或文件被拒绝时回答 400 并附问题列表。不带文件的运行是
 * 同一条路由，请求体为 JSON。
 */

const FLOW_ID = /^flw_[A-Za-z0-9_-]{1,36}$/;
// An upload may be slow on a poor link, but not endless.
// 弱网下上传可能很慢，但不能没有尽头。
const MAX_UPLOAD_MS = 15 * 60_000;

type Context = { params: Promise<{ id: string }> };

export async function POST(request: Request, context: Context): Promise<Response> {
  if (!isTrustedWrite({
    method: request.method,
    origin: request.headers.get("origin"),
    host: request.headers.get("host"),
    secFetchSite: request.headers.get("sec-fetch-site"),
  })) {
    return errorResponse(403, "forbidden_origin");
  }
  const { id } = await context.params;
  if (!FLOW_ID.test(id)) return errorResponse(404, "not_found");
  const session = await readSession();
  if (!session) return errorResponse(401, "unauthorized");

  const contentType = request.headers.get("content-type") ?? "";
  const media = contentType.split(";")[0].trim().toLowerCase();
  if ((media !== "application/json" && media !== "multipart/form-data") || !request.body) return errorResponse(400, "invalid_request");

  const headers: Record<string, string> = { Authorization: `Bearer ${session.token}`, "Content-Type": contentType, Accept: "application/json" };
  const length = request.headers.get("content-length");
  if (length !== null && /^\d{1,15}$/.test(length)) headers["Content-Length"] = length;

  let upstream: Response;
  try {
    upstream = await fetch(`${serverConfig().gatewayUrl}/studio/v1/flows/${id}/runs`, {
      method: "POST",
      headers,
      body: request.body,
      // Streaming a request body needs half duplex.
      // 流式发送请求体需要半双工。
      duplex: "half",
      cache: "no-store",
      redirect: "manual",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(MAX_UPLOAD_MS)]),
    } as RequestInit & { duplex: "half" });
  } catch {
    return errorResponse(502, "upstream");
  }
  // The answer is a small JSON document (the run, or the refusal).
  // 应答是一个很小的 JSON 文档（运行本身或拒绝）。
  return gatewayAnswer(upstream.status, await upstream.text().catch(() => ""));
}

import { errorMessage, studioErrorCode } from "@/lib/studio/contract";
import { isTrustedWrite } from "@/lib/studio/request-origin";
import { parseSendBody } from "@/lib/studio/send-request";
import { serverConfig } from "@/lib/server/config";
import { readBoundedText } from "@/lib/server/responses";
import { readSession } from "@/lib/server/session";

/**
 * The chat stream: authenticates one send, forwards it to the Gateway's
 * Studio SSE and relays the bytes untouched. The translation into AI SDK UI
 * message chunks runs in the browser (lib/studio/chat-transport.ts), so a
 * streamed answer costs this server no per-token parsing. Nothing is
 * persisted here; the Gateway writes both sides of the turn. When the browser
 * stops, its request aborts, which aborts the upstream request, which the
 * Gateway records as an incomplete reply.
 *
 * A refusal answers in plain text with Studio's fixed wording, which is what
 * the AI SDK transport shows as the error; the stable code rides in a header.
 *
 * 对话流：鉴权一次发送，转发到 Gateway 的 Studio SSE，并原样转发字节。转换为 AI SDK UI
 * message chunk 的工作在浏览器里进行（lib/studio/chat-transport.ts），流式回答的每个
 * token 都不再让本服务端解析。这里不做任何持久化，一轮的两侧都由 Gateway 写入。浏览器
 * 停止时其请求中止，进而中止上游请求，Gateway 据此记为未完成的回复。
 *
 * 拒绝时以纯文本返回 Studio 的固定文案（AI SDK transport 会把它作为错误展示），稳定
 * 错误码放在响应头里。
 */

const CONVERSATION_ID = /^cnv_[A-Za-z0-9_-]{1,36}$/;
const MAX_SEND_BYTES = 160 * 1024;
const MAX_STREAM_MS = 15 * 60_000;

type Context = { params: Promise<{ id: string }> };

function refuse(status: number, code: string): Response {
  return new Response(errorMessage(code), {
    status,
    headers: { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store", "X-Studio-Error": code },
  });
}

export async function POST(request: Request, context: Context): Promise<Response> {
  if (!isTrustedWrite({
    method: request.method,
    origin: request.headers.get("origin"),
    host: request.headers.get("host"),
    secFetchSite: request.headers.get("sec-fetch-site"),
  })) {
    return refuse(403, "forbidden_origin");
  }
  const { id } = await context.params;
  if (!CONVERSATION_ID.test(id)) return refuse(404, "not_found");
  const session = await readSession();
  if (!session) return refuse(401, "unauthorized");

  const raw = await readBoundedText(request, MAX_SEND_BYTES);
  if (raw === null) return refuse(413, "body_too_large");
  let body: ReturnType<typeof parseSendBody> = null;
  try {
    body = parseSendBody(JSON.parse(raw));
  } catch {
    body = null;
  }
  if (!body) return refuse(400, "invalid_request");

  let upstream: Response;
  try {
    upstream = await fetch(`${serverConfig().gatewayUrl}/studio/v1/conversations/${id}/messages`, {
      method: "POST",
      headers: { Authorization: `Bearer ${session.token}`, "Content-Type": "application/json", Accept: "text/event-stream" },
      body: JSON.stringify(body),
      cache: "no-store",
      redirect: "manual",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(MAX_STREAM_MS)]),
    });
  } catch {
    return refuse(502, "upstream");
  }
  if (!upstream.ok || !upstream.body) {
    const code = studioErrorCode(await upstream.text().catch(() => "")) || (upstream.status === 401 ? "unauthorized" : "upstream");
    return refuse(upstream.status === 200 ? 502 : upstream.status, code);
  }

  // The Gateway's bytes go out untouched; only its frames' content reaches
  // the browser, none of its headers. If the upstream is cut, this response
  // is cut too and the browser reports the interruption.
  // Gateway 的字节原样发出；到达浏览器的只有其帧内容，不含它的响应头。上游被截断时本响应
  // 随之截断，由浏览器报告中断。
  return new Response(upstream.body, {
    headers: { "Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-store", "X-Accel-Buffering": "no" },
  });
}

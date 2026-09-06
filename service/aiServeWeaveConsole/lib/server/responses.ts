/**
 * MAX_BODY_BYTES bounds a request body the Console server reads. These are
 * small JSON documents; the control plane applies the same bound for the same
 * reason, and applying it here too means an oversized body is refused before
 * it is forwarded anywhere.
 *
 * MAX_BODY_BYTES 限制 Console 服务端读取的请求体大小。这些都是很小的 JSON 文档；
 * 控制面出于同样的理由施加了同样的上限，在这里也施加一次，意味着超大的请求体在被转发
 * 到任何地方之前就被拒绝。
 */
export const MAX_BODY_BYTES = 64 * 1024;

/**
 * jsonResponse writes a JSON response that no cache may keep.
 *
 * Every response from these routes is derived from one session's data, so
 * `no-store` is not a per-endpoint decision: a shared cache holding any of it
 * could serve one tenant's data to another.
 *
 * jsonResponse 写出一个任何缓存都不得保留的 JSON 响应。
 *
 * 这些路由的每一个响应都源自某一个会话的数据，因此 `no-store` 不是逐端点的决定：
 * 共享缓存只要留下其中任何一份，就可能把一个租户的数据发给另一个租户。
 */
export function jsonResponse(status: number, body: unknown): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: headers(),
  });
}

/**
 * rawJsonResponse forwards an upstream JSON document unchanged.
 *
 * rawJsonResponse 原样转发一个上游的 JSON 文档。
 */
export function rawJsonResponse(status: number, text: string): Response {
  return new Response(text, { status, headers: headers() });
}

/**
 * errorResponse writes the failure shape.
 *
 * The text is a fixed code, not the upstream's message: the browser decides
 * what to show from the status alone, and forwarding an operator-facing
 * message would put control plane internals one `fetch` away from anyone with
 * a session.
 *
 * errorResponse 写出失败时的形状。
 *
 * 其中的文本是一个固定的代号，而不是上游的报错信息：浏览器只根据状态码决定展示什么，
 * 而转发一条面向运维的信息，会让控制面的内部细节距离任何持有会话的人只剩一次 `fetch`。
 */
export function errorResponse(status: number, code: string): Response {
  return jsonResponse(status, { error: code });
}

/**
 * readBoundedText reads a request body, refusing one over the bound.
 *
 * The bound is enforced while reading, not after. `request.text()` buffers the
 * whole body first and only then measures it, which makes the limit a report
 * rather than a limit: a chunked request declaring no Content-Length gets to
 * put as much into this process's memory as it likes before being told 413.
 * So the body is read chunk by chunk, counted as it arrives, and the stream is
 * cancelled the moment the count goes over.
 *
 * The declared Content-Length is still checked first. It is a cheaper refusal
 * for the honest oversized request, and it costs nothing for the dishonest
 * one — a caller who lies about the length, or omits it, is caught by the
 * counting below.
 *
 * readBoundedText 读取请求体，超过上限则拒绝。
 *
 * 上限在读取过程中生效，而不是读完之后。`request.text()` 会先把整个请求体缓冲下来、
 * 之后才去度量它，那让这个上限成了一份报告而不是一道限制：一个不声明 Content-Length 的
 * 分块请求，可以在被告知 413 之前往本进程内存里塞进任意多的内容。因此这里逐块读取、
 * 边到达边计数，一旦超出立刻取消这个流。
 *
 * 声明的 Content-Length 仍然先检查一次。对一个诚实的超大请求，那是一次更省事的拒绝；
 * 对不诚实的那个则毫无代价——谎报长度或干脆不报的调用方，会被下面的计数抓住。
 */
export async function readBoundedText(request: Request): Promise<string | null> {
  const declared = Number(request.headers.get("content-length") ?? "0");
  if (Number.isFinite(declared) && declared > MAX_BODY_BYTES) {
    return null;
  }
  if (!request.body) {
    return "";
  }

  const reader = request.body.getReader();
  // The decoder is streaming: a multi-byte character split across two chunks
  // must not become two replacement characters, which is what decoding each
  // chunk on its own would do.
  //
  // 解码器是流式的：一个被拆到两个分块里的多字节字符，不能变成两个替换字符——那正是
  // 各自独立解码每个分块会造成的结果。
  const decoder = new TextDecoder();
  let received = 0;
  let text = "";

  for (;;) {
    const { done, value } = await reader.read();
    if (done) {
      return text + decoder.decode();
    }
    received += value.byteLength;
    if (received > MAX_BODY_BYTES) {
      // Cancelling tells the source to stop producing. Without it the rest of
      // the body keeps arriving into a buffer nobody is going to read.
      //
      // 取消会告知源头停止生产。没有它，请求体的其余部分仍会继续到达一个没人会去读的
      // 缓冲区。
      await reader.cancel();
      return null;
    }
    text += decoder.decode(value, { stream: true });
  }
}

/** headers is the header set every response from these routes carries.
 *
 * headers 是这些路由的每个响应都携带的头集合。 */
function headers(): HeadersInit {
  return {
    "Content-Type": "application/json; charset=utf-8",
    "Cache-Control": "no-store, no-cache, must-revalidate",
    Pragma: "no-cache",
  };
}

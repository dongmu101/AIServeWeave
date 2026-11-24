import type { UIMessageChunk } from "ai";

import type { StudioMetadata } from "./conversations.ts";
import { SSEParser } from "./sse.ts";
import { createTranslator } from "./stream-translate.ts";

/**
 * translateGatewayStream turns the Gateway's Studio SSE bytes into AI SDK UI
 * message chunks. It runs in the browser: the Studio server relays the
 * Gateway's bytes untouched, so a streamed answer costs the server no parsing
 * or re-encoding per token.
 *
 * A stream cut before message_stop ends with Studio's interrupted error, as
 * the translator decides. An abort is not a cut: the user stopped the reply,
 * so the AbortError is passed on for the AI SDK to treat as a stop.
 *
 * translateGatewayStream 把 Gateway 的 Studio SSE 字节转成 AI SDK 的 UI message chunk。
 * 它在浏览器里运行：Studio 服务端原样转发 Gateway 的字节，流式回答的每个 token 都不再
 * 让服务端解析和重新编码。
 *
 * 在 message_stop 之前被截断的流，按转换器的规则以 Studio 的「已中断」错误结束。中止不是
 * 截断：那是用户停止了回答，因此 AbortError 原样抛出，由 AI SDK 当作停止处理。
 */
export function translateGatewayStream(source: ReadableStream<Uint8Array>): ReadableStream<UIMessageChunk<StudioMetadata>> {
  const reader = source.getReader();
  // stream: true keeps a multibyte character split across pieces intact.
  // stream: true 让跨分片的多字节字符保持完整。
  const decoder = new TextDecoder();
  const parser = new SSEParser();
  const translator = createTranslator();
  return new ReadableStream<UIMessageChunk<StudioMetadata>>({
    async pull(controller) {
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          const chunks = parser.push(decoder.decode(value, { stream: true })).flatMap(frame => translator.frame(frame));
          if (chunks.length > 0) {
            for (const chunk of chunks) controller.enqueue(chunk);
            return;
          }
        }
      } catch (error) {
        if (isAbort(error)) {
          controller.error(error);
          return;
        }
        // Cut mid-stream: end() reports it unless the reply already finished.
        // 中途截断：除非回答已结束，否则由 end() 报告。
      }
      for (const chunk of translator.end()) controller.enqueue(chunk);
      controller.close();
    },
    cancel(reason) {
      return reader.cancel(reason);
    },
  });
}

function isAbort(error: unknown): boolean {
  return typeof error === "object" && error !== null && (error as { name?: unknown }).name === "AbortError";
}

import { HttpChatTransport, type UIMessage, type UIMessageChunk } from "ai";

import { translateGatewayStream } from "./gateway-stream.ts";

/**
 * StudioChatTransport is the AI SDK's HTTP transport with one difference:
 * the stream route relays the Gateway's own Studio SSE, and the translation
 * into UI message chunks happens here in the browser
 * (translateGatewayStream). Requests, refusals and aborts behave exactly as
 * with DefaultChatTransport.
 *
 * StudioChatTransport 是 AI SDK 的 HTTP transport，只有一处不同：流式路由原样转发 Gateway
 * 自己的 Studio SSE，转换为 UI message chunk 的工作在浏览器里完成（translateGatewayStream）。
 * 请求、拒绝与中止的行为与 DefaultChatTransport 完全相同。
 */
export class StudioChatTransport<UI_MESSAGE extends UIMessage> extends HttpChatTransport<UI_MESSAGE> {
  protected processResponseStream(stream: ReadableStream<Uint8Array>): ReadableStream<UIMessageChunk> {
    return translateGatewayStream(stream) as ReadableStream<UIMessageChunk>;
  }
}

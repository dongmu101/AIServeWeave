import { messageText } from "./conversations.ts";

/**
 * The body of a send, built in the browser from what useChat hands the
 * transport, and checked again on the Studio server before it is forwarded.
 * Only the newest message travels: the server holds the history.
 *
 * 发送请求体：在浏览器中依据 useChat 交给 transport 的内容构造，在 Studio 服务端转发前
 * 再校验一次。只发送最新一条消息，历史由服务端保存。
 */

// At four UTF-8 bytes a character this stays under the control plane's 128 KiB.
// 按每字符四个 UTF-8 字节计，也不超过控制面的 128 KiB 上限。
export const MAX_MESSAGE_CHARS = 32_000;

export type SendBody =
  | { content: string; model: string }
  | { regenerate: true; model: string };

/** buildSendBody turns a useChat request into a send body.
 * buildSendBody 把 useChat 的请求转换为发送请求体。 */
export function buildSendBody(input: {
  trigger: "submit-message" | "regenerate-message";
  messages: Array<{ role: string; parts: Array<{ type: string; text?: string }> }>;
  model: string;
}): SendBody {
  if (input.trigger === "regenerate-message") return { regenerate: true, model: input.model };
  const last = input.messages.at(-1);
  return { content: last && last.role === "user" ? messageText(last) : "", model: input.model };
}

/** parseSendBody validates a send body, returning null when it is not one.
 * parseSendBody 校验发送请求体，不合法时返回 null。 */
export function parseSendBody(value: unknown): SendBody | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const source = value as Record<string, unknown>;
  const model = source.model;
  if (typeof model !== "string" || model === "" || model.length > 128) return null;
  const keys = Object.keys(source).sort().join(",");
  if (source.regenerate === true && keys === "model,regenerate") return { regenerate: true, model };
  const content = source.content;
  if (keys === "content,model" && typeof content === "string" && content.trim() !== "" && content.length <= MAX_MESSAGE_CHARS) {
    return { content, model };
  }
  return null;
}

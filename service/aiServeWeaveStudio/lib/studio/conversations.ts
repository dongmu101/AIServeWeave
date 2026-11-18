import type { UIMessage } from "ai";

import { ContractError } from "./contract.ts";

/**
 * Parsers for the Gateway's conversation JSON, and the conversion of stored
 * messages into the AI SDK's UIMessage. Like contract.ts, a shape that is not
 * recognised throws ContractError instead of rendering as empty.
 *
 * Gateway 对话 JSON 的解析器，以及把已存储消息转换为 AI SDK 的 UIMessage。与
 * contract.ts 一样，无法识别的形状抛出 ContractError，而不是渲染成空数据。
 */

export interface Conversation {
  id: string;
  title: string;
  model: string;
  systemPrompt: string;
  updatedAt: string;
}

export interface ConversationPage {
  conversations: Conversation[];
  next: string;
}

export type MessageStatus = "complete" | "incomplete" | "error";

export interface StoredMessage {
  id: string;
  role: "user" | "assistant";
  content: string;
  reasoning: string;
  status: MessageStatus;
  model: string;
  usage: { promptTokens: number; completionTokens: number } | null;
  errorCode: string;
}

/** StudioMetadata rides on each UIMessage: what the stored message, or the
 * stream's finish, says about it.
 *
 * StudioMetadata 附在每条 UIMessage 上：已存储的消息或流的结束帧对它的描述。 */
export interface StudioMetadata {
  status?: MessageStatus;
  model?: string;
  usage?: { promptTokens: number; completionTokens: number };
  errorCode?: string;
  title?: string;
}

export type StudioUIMessage = UIMessage<StudioMetadata>;

function fail(): never {
  throw new ContractError();
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) fail();
  return value as Record<string, unknown>;
}

function str(source: Record<string, unknown>, key: string, required = false): string {
  const value = source[key];
  if (value === undefined && !required) return "";
  if (typeof value !== "string" || (required && value === "")) fail();
  return value;
}

function count(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : 0;
}

/** parseConversation reads one conversation object.
 * parseConversation 读取一个对话对象。 */
export function parseConversation(value: unknown): Conversation {
  const source = record(value);
  return {
    id: str(source, "id", true),
    title: str(source, "title"),
    model: str(source, "model"),
    systemPrompt: str(source, "system_prompt"),
    updatedAt: str(source, "updated_at"),
  };
}

/** parseConversationPage reads GET /studio/v1/conversations.
 * parseConversationPage 读取 GET /studio/v1/conversations。 */
export function parseConversationPage(value: unknown): ConversationPage {
  const source = record(value);
  const data = source.data ?? [];
  if (!Array.isArray(data)) fail();
  return { conversations: data.map(parseConversation), next: str(source, "next") };
}

/** parseMessage reads one stored message.
 * parseMessage 读取一条已存储的消息。 */
export function parseMessage(value: unknown): StoredMessage {
  const source = record(value);
  const role = str(source, "role", true);
  if (role !== "user" && role !== "assistant") fail();
  const status = str(source, "status", true);
  if (status !== "complete" && status !== "incomplete" && status !== "error") fail();
  const usage = source.usage === undefined || source.usage === null ? null : record(source.usage);
  return {
    id: str(source, "id", true),
    role,
    content: str(source, "content"),
    reasoning: str(source, "reasoning"),
    status,
    model: str(source, "model"),
    usage: usage ? { promptTokens: count(usage.prompt_tokens), completionTokens: count(usage.completion_tokens) } : null,
    errorCode: str(source, "error_code"),
  };
}

/** parseMessages reads GET /studio/v1/conversations/{id}/messages.
 * parseMessages 读取 GET /studio/v1/conversations/{id}/messages。 */
export function parseMessages(value: unknown): StoredMessage[] {
  const data = record(value).data ?? [];
  if (!Array.isArray(data)) fail();
  return data.map(parseMessage);
}

/** toUIMessages converts stored messages into the messages useChat starts
 * from. Reasoning comes before the answer, as it streamed.
 *
 * toUIMessages 把已存储的消息转换为 useChat 的初始消息。思考过程位于回答之前，与流式时
 * 的顺序一致。 */
export function toUIMessages(messages: StoredMessage[]): StudioUIMessage[] {
  return messages.map((message) => {
    const parts: StudioUIMessage["parts"] = [];
    if (message.reasoning) parts.push({ type: "reasoning", text: message.reasoning, state: "done" });
    if (message.content) parts.push({ type: "text", text: message.content, state: "done" });
    const metadata: StudioMetadata = { status: message.status };
    if (message.model) metadata.model = message.model;
    if (message.usage) metadata.usage = message.usage;
    if (message.errorCode) metadata.errorCode = message.errorCode;
    return { id: message.id, role: message.role, parts, metadata };
  });
}

/** messageText is the plain text of a UIMessage's text parts.
 * messageText 是 UIMessage 中文本部分的纯文本。 */
export function messageText(message: { parts: Array<{ type: string; text?: string }> }): string {
  return message.parts
    .filter((part) => part.type === "text")
    .map((part) => part.text ?? "")
    .join("");
}

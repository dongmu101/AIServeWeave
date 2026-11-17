"use client";

/**
 * The home page creates a conversation, then navigates to it; the first
 * message waits here, in memory, for the conversation page to send it. It is
 * never written to storage or the URL, and a reload simply loses it.
 *
 * 首页先创建对话再跳转过去；第一条消息在内存中等待对话页发送。它从不写入存储或 URL，
 * 刷新页面只会丢失它。
 */

const waiting = new Map<string, { text: string; model: string }>();

export function holdFirstMessage(conversationId: string, text: string, model: string) {
  waiting.set(conversationId, { text, model });
}

/** takeFirstMessage returns the waiting message once, then forgets it.
 * takeFirstMessage 只返回一次等待中的消息，随后即忘记。 */
export function takeFirstMessage(conversationId: string): { text: string; model: string } | undefined {
  const message = waiting.get(conversationId);
  waiting.delete(conversationId);
  return message;
}

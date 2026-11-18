import assert from "node:assert/strict";
import { test } from "node:test";

import { ContractError } from "./contract.ts";
import { messageText, parseConversationPage, parseMessages, toUIMessages } from "./conversations.ts";

test("parseConversationPage reads conversations and the next cursor", () => {
  const page = parseConversationPage({
    data: [{ id: "cnv_1", title: "Go", model: "qwen3:8b", system_prompt: "", created_at: "2026-11-16T08:00:00Z", updated_at: "2026-11-16T09:00:00Z" }],
    next: "abc",
  });
  assert.deepEqual(page, { conversations: [{ id: "cnv_1", title: "Go", model: "qwen3:8b", systemPrompt: "", updatedAt: "2026-11-16T09:00:00Z" }], next: "abc" });
  assert.deepEqual(parseConversationPage({ data: [] }), { conversations: [], next: "" });
});

test("parsers refuse shapes they do not know", () => {
  assert.throws(() => parseConversationPage({ data: [{ title: "no id" }] }), ContractError);
  assert.throws(() => parseMessages({ data: [{ id: "m", role: "system", status: "complete" }] }), ContractError);
  assert.throws(() => parseMessages({ data: [{ id: "m", role: "user", status: "pending" }] }), ContractError);
});

test("stored messages become UIMessages with reasoning before the answer", () => {
  const messages = parseMessages({
    data: [
      { id: "msg_1", seq: 1, role: "user", content: "你好", status: "complete" },
      { id: "msg_2", seq: 2, role: "assistant", content: "嗨", reasoning: "想一想", status: "incomplete", model: "qwen3:8b", usage: { prompt_tokens: 3, completion_tokens: 2 } },
      { id: "msg_3", seq: 3, role: "assistant", content: "", status: "error", error_code: "upstream_error" },
    ],
  });
  const ui = toUIMessages(messages);
  assert.deepEqual(ui[0], { id: "msg_1", role: "user", parts: [{ type: "text", text: "你好", state: "done" }], metadata: { status: "complete" } });
  assert.deepEqual(ui[1].parts.map((part) => part.type), ["reasoning", "text"]);
  assert.deepEqual(ui[1].metadata, { status: "incomplete", model: "qwen3:8b", usage: { promptTokens: 3, completionTokens: 2 } });
  assert.deepEqual(ui[2], { id: "msg_3", role: "assistant", parts: [], metadata: { status: "error", errorCode: "upstream_error" } });
  assert.equal(messageText(ui[1]), "嗨");
});

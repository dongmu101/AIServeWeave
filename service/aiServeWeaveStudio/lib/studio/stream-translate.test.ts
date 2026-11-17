import assert from "node:assert/strict";
import { test } from "node:test";

import { createTranslator } from "./stream-translate.ts";

function run(frames: Array<[string, Record<string, unknown>]>, end = false) {
  const translator = createTranslator();
  const chunks = frames.flatMap(([event, data]) => translator.frame({ event, data }));
  if (end) chunks.push(...translator.end());
  return chunks;
}

const start: [string, Record<string, unknown>] = ["message_start", { message_id: "msg_r", model: "qwen3:8b", conversation: { id: "cnv_1", title: "问候" } }];

test("a complete reply becomes start, reasoning, text and finish", () => {
  const chunks = run([
    start,
    ["thinking_delta", { delta: "想" }],
    ["text_delta", { delta: "你" }],
    ["text_delta", { delta: "好" }],
    ["usage", { prompt_tokens: 4, completion_tokens: 2 }],
    ["message_stop", { message_id: "msg_r", status: "complete" }],
  ]);
  assert.deepEqual(chunks, [
    { type: "start", messageId: "msg_r", messageMetadata: { model: "qwen3:8b", title: "问候" } },
    { type: "reasoning-start", id: "reasoning-1" },
    { type: "reasoning-delta", id: "reasoning-1", delta: "想" },
    { type: "reasoning-end", id: "reasoning-1" },
    { type: "text-start", id: "text-2" },
    { type: "text-delta", id: "text-2", delta: "你" },
    { type: "text-delta", id: "text-2", delta: "好" },
    { type: "text-end", id: "text-2" },
    { type: "finish", finishReason: "stop", messageMetadata: { status: "complete", usage: { promptTokens: 4, completionTokens: 2 } } },
  ]);
});

test("an error frame closes open parts and carries Studio's wording, not the upstream's", () => {
  const chunks = run([start, ["text_delta", { delta: "半" }], ["error", { code: "upstream_error", message: "secret detail" }], ["message_stop", { status: "error" }]]);
  assert.deepEqual(chunks.slice(-2), [
    { type: "text-end", id: "text-1" },
    { type: "error", errorText: "模型返回了错误，请重试或换一个模型。" },
  ]);
});

test("an incomplete reply finishes as interrupted", () => {
  const chunks = run([start, ["text_delta", { delta: "半" }], ["message_stop", { status: "incomplete" }]]);
  assert.deepEqual(chunks.at(-1), { type: "finish", finishReason: "interrupted", messageMetadata: { status: "incomplete" } });
});

test("a stream cut before message_stop ends with an error", () => {
  const chunks = run([start, ["text_delta", { delta: "半" }]], true);
  assert.deepEqual(chunks.slice(-2), [
    { type: "text-end", id: "text-1" },
    { type: "error", errorText: "回复中断，请重试。" },
  ]);
  assert.deepEqual(run([start, ["message_stop", { status: "complete" }]], true).at(-1)?.type, "finish");
});

test("unknown frames and empty deltas are ignored", () => {
  assert.deepEqual(run([["ping", {}], ["text_delta", { delta: "" }]]), []);
});

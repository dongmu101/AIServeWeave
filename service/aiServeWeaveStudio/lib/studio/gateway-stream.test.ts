import assert from "node:assert/strict";
import { test } from "node:test";

import type { UIMessageChunk } from "ai";

import { StudioChatTransport } from "./chat-transport.ts";
import { errorMessage } from "./contract.ts";
import { translateGatewayStream } from "./gateway-stream.ts";

const encoder = new TextEncoder();

function frame(event: string, data: Record<string, unknown>): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
}

const complete = [
  frame("message_start", { message_id: "msg_r", model: "qwen3:8b", conversation: { id: "cnv_1", title: "问候" } }),
  frame("thinking_delta", { delta: "想" }),
  frame("text_delta", { delta: "你好" }),
  frame("usage", { prompt_tokens: 4, completion_tokens: 2 }),
  frame("message_stop", { message_id: "msg_r", status: "complete" }),
].join("");

const expected = [
  { type: "start", messageId: "msg_r", messageMetadata: { model: "qwen3:8b", title: "问候" } },
  { type: "reasoning-start", id: "reasoning-1" },
  { type: "reasoning-delta", id: "reasoning-1", delta: "想" },
  { type: "reasoning-end", id: "reasoning-1" },
  { type: "text-start", id: "text-2" },
  { type: "text-delta", id: "text-2", delta: "你好" },
  { type: "text-end", id: "text-2" },
  { type: "finish", finishReason: "stop", messageMetadata: { status: "complete", usage: { promptTokens: 4, completionTokens: 2 } } },
];

const interrupted = { type: "error", errorText: errorMessage("stream_interrupted") };

// source yields the given byte pieces, then either closes or fails with error.
// source 依次给出这些字节分片，然后关闭或以 error 失败。
function source(pieces: Uint8Array[], error?: unknown, onCancel?: (reason: unknown) => void): ReadableStream<Uint8Array> {
  let i = 0;
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      if (i < pieces.length) controller.enqueue(pieces[i++]);
      else if (error !== undefined) controller.error(error);
      else controller.close();
    },
    cancel(reason) { onCancel?.(reason); },
  });
}

// split cuts bytes every `size` bytes, so pieces end inside frames and inside multibyte characters.
// split 每 size 字节切一刀，分片会落在帧中间与多字节字符中间。
function split(text: string, size: number): Uint8Array[] {
  const bytes = encoder.encode(text);
  const pieces: Uint8Array[] = [];
  for (let i = 0; i < bytes.length; i += size) pieces.push(bytes.slice(i, i + size));
  return pieces;
}

async function collect(stream: ReadableStream<UIMessageChunk>): Promise<unknown[]> {
  const out: unknown[] = [];
  for await (const chunk of stream) out.push(chunk);
  return out;
}

test("a complete reply translates the same however the bytes are split", async () => {
  for (const size of [1, 2, 3, 7, 64, 100_000]) {
    assert.deepEqual(await collect(translateGatewayStream(source(split(complete, size)))), expected, `piece size ${size}`);
  }
});

test("a stream that closes before message_stop ends with the interrupted error", async () => {
  const cut = complete.slice(0, complete.indexOf("event: usage"));
  const chunks = await collect(translateGatewayStream(source([encoder.encode(cut)])));
  assert.deepEqual(chunks.at(-2), { type: "text-end", id: "text-2" });
  assert.deepEqual(chunks.at(-1), interrupted);
});

test("a network failure mid-stream ends with the interrupted error", async () => {
  const cut = complete.slice(0, complete.indexOf("event: usage"));
  const chunks = await collect(translateGatewayStream(source([encoder.encode(cut)], new TypeError("network error"))));
  assert.deepEqual(chunks.at(-1), interrupted);
});

test("a failure after message_stop adds no error", async () => {
  const chunks = await collect(translateGatewayStream(source([encoder.encode(complete)], new TypeError("network error"))));
  assert.deepEqual(chunks, expected);
});

test("an abort is passed on as an AbortError, not reported as an interruption", async () => {
  const abort = new DOMException("The user aborted a request.", "AbortError");
  const first = frame("message_start", { message_id: "msg_r" });
  const seen: unknown[] = [];
  await assert.rejects(async () => {
    for await (const chunk of translateGatewayStream(source([encoder.encode(first)], abort))) seen.push(chunk);
  }, (error: unknown) => (error as Error).name === "AbortError");
  assert.deepEqual(seen, [{ type: "start", messageId: "msg_r", messageMetadata: {} }]);
});

test("an empty body ends with the interrupted error", async () => {
  assert.deepEqual(await collect(translateGatewayStream(source([]))), [interrupted]);
});

test("cancelling the translated stream cancels the Gateway bytes", async () => {
  let cancelled = false;
  const stream = translateGatewayStream(source(split(complete, 5), undefined, () => { cancelled = true; }));
  const reader = stream.getReader();
  await reader.read();
  await reader.cancel("stopped");
  assert.equal(cancelled, true);
});

test("the transport posts the send and translates the relayed Gateway stream", async () => {
  const calls: Array<{ url: string; body: string }> = [];
  const transport = new StudioChatTransport({
    api: "/api/studio/conversations/cnv_1/stream",
    fetch: async (input, init) => {
      calls.push({ url: String(input), body: String(init?.body) });
      return new Response(complete, { headers: { "Content-Type": "text/event-stream" } });
    },
    prepareSendMessagesRequest: () => ({ body: { model: "qwen3:8b" } }),
  });
  const stream = await transport.sendMessages({ chatId: "cnv_1", messages: [], abortSignal: undefined, trigger: "submit-message", messageId: undefined });
  assert.deepEqual(await collect(stream), expected);
  assert.deepEqual(calls, [{ url: "/api/studio/conversations/cnv_1/stream", body: JSON.stringify({ model: "qwen3:8b" }) }]);
});

test("a refusal surfaces Studio's plain-text wording as the error", async () => {
  const wording = errorMessage("forbidden_origin");
  const transport = new StudioChatTransport({
    api: "/stream",
    fetch: async () => new Response(wording, { status: 403, headers: { "Content-Type": "text/plain; charset=utf-8" } }),
  });
  await assert.rejects(
    transport.sendMessages({ chatId: "cnv_1", messages: [], abortSignal: undefined, trigger: "submit-message", messageId: undefined }),
    (error: unknown) => (error as Error).message.includes(wording),
  );
});

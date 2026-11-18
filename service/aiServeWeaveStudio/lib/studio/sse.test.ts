import assert from "node:assert/strict";
import { test } from "node:test";

import { SSEParser } from "./sse.ts";

test("frames split across pieces are assembled", () => {
  const parser = new SSEParser();
  assert.deepEqual(parser.push('event: text_delta\ndata: {"del'), []);
  assert.deepEqual(parser.push('ta":"你"}\n\nevent: usage\r\ndata: {"prompt_tokens":1}\r\n\r\n'), [
    { event: "text_delta", data: { delta: "你" } },
    { event: "usage", data: { prompt_tokens: 1 } },
  ]);
});

test("frames without JSON object data are dropped", () => {
  const parser = new SSEParser();
  assert.deepEqual(parser.push(": comment\n\nevent: x\ndata: not json\n\nevent: y\ndata: [1]\n\n"), []);
});

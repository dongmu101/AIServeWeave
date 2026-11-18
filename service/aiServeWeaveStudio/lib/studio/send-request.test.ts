import assert from "node:assert/strict";
import { test } from "node:test";

import { buildSendBody, MAX_MESSAGE_CHARS, parseSendBody } from "./send-request.ts";

const user = (text: string) => ({ role: "user", parts: [{ type: "text", text }] });

test("buildSendBody sends only the newest message, or regenerate", () => {
  assert.deepEqual(buildSendBody({ trigger: "submit-message", messages: [user("一"), { role: "assistant", parts: [] }, user("二")], model: "m" }), { content: "二", model: "m" });
  assert.deepEqual(buildSendBody({ trigger: "regenerate-message", messages: [user("一")], model: "m" }), { regenerate: true, model: "m" });
});

test("parseSendBody accepts exactly the two shapes", () => {
  assert.deepEqual(parseSendBody({ content: "hi", model: "m" }), { content: "hi", model: "m" });
  assert.deepEqual(parseSendBody({ regenerate: true, model: "m" }), { regenerate: true, model: "m" });
  for (const bad of [
    null, [], {}, { content: "hi" }, { content: "  ", model: "m" }, { content: "hi", model: "" },
    { content: "hi", model: "m", regenerate: true }, { content: "hi", model: "m", tenant_id: "x" },
    { regenerate: false, model: "m" }, { content: "x".repeat(MAX_MESSAGE_CHARS + 1), model: "m" },
  ]) {
    assert.equal(parseSendBody(bad), null, JSON.stringify(bad)?.slice(0, 60));
  }
});

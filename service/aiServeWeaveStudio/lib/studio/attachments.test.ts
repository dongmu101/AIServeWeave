import assert from "node:assert/strict";
import { test } from "node:test";

import { admit, checkAttachment, displayName, formatSize, MAX_ATTACHMENT_BYTES, MAX_ATTACHMENTS } from "./attachments.ts";

test("checkAttachment accepts known types case-insensitively", () => {
  assert.deepEqual(checkAttachment({ name: "a.PNG", size: 10 }), { ok: true, kind: "image" });
  assert.deepEqual(checkAttachment({ name: "spec.pdf", size: 10 }), { ok: true, kind: "document" });
});

test("checkAttachment refuses unknown, empty and oversized files", () => {
  assert.equal(checkAttachment({ name: "run.exe", size: 10 }).ok, false);
  assert.equal(checkAttachment({ name: "png", size: 10 }).ok, false);
  assert.equal(checkAttachment({ name: "a.txt", size: 0 }).ok, false);
  assert.equal(checkAttachment({ name: "a.txt", size: MAX_ATTACHMENT_BYTES + 1 }).ok, false);
});

test("admit honours the per-message limit and deduplicates reasons", () => {
  const files = Array.from({ length: 3 }, (_, i) => ({ name: `${i}.png`, size: 1 }));
  const result = admit(MAX_ATTACHMENTS - 1, files);
  assert.equal(result.accepted.length, 1);
  assert.deepEqual(result.rejected, [`每条消息最多 ${MAX_ATTACHMENTS} 个附件`]);
});

test("displayName keeps short names and the extension of long ones", () => {
  assert.equal(displayName("a.png"), "a.png");
  const long = displayName(`${"x".repeat(60)}.png`);
  assert.equal(long.length, 40);
  assert.ok(long.endsWith("….png"));
});

test("formatSize picks a readable unit", () => {
  assert.equal(formatSize(512), "512 B");
  assert.equal(formatSize(2048), "2 KB");
  assert.equal(formatSize(3 * 1024 * 1024), "3.0 MB");
});

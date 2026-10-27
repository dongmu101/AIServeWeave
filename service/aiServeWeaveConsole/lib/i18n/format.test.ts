import assert from "node:assert/strict";
import test from "node:test";
import { formatNumber, formatPercent, formatBytes, formatDuration, formatRelativeTime } from "./format.ts";
test("numbers and percentages use the selected locale without changing values", () => {
 assert.equal(formatNumber(1234.5, "de-DE"), "1.234,5");
 assert.equal(formatPercent(.25, "en-US"), "25%");
 assert.equal(formatBytes(1024, "en-US"), "1 KiB");
 assert.equal(formatDuration(1250, "en-US"), "1.25 sec");
 assert.equal(formatRelativeTime(-2, "day", "en-US"), "2 days ago");
});

test("sub-byte and nonfinite metrics never display an undefined unit", () => {
 assert.equal(formatBytes(.5, "en-US"), "0.5 B");
 assert.equal(formatBytes(Number.NaN, "en-US"), "—");
});

import assert from "node:assert/strict";
import test from "node:test";

import {
  apiKeyState,
  DEFAULT_TTL_CHOICE,
  formatDateTime,
  formatLimit,
  isTerminalJobState,
  limitProblem,
  observationAge,
  matchesQuery,
  TTL_CHOICES,
  ttlSeconds,
  UNKNOWN_TIME,
} from "./format.ts";

test("an absent timestamp reads as a fact, not as a date", () => {
  assert.equal(formatDateTime(null), UNKNOWN_TIME);
  assert.equal(formatDateTime(null, "从未使用"), "从未使用");
  assert.equal(formatDateTime("not-a-time"), UNKNOWN_TIME);
  assert.notEqual(formatDateTime("2026-09-06T10:00:00Z"), UNKNOWN_TIME);
});

test("a key's shown state accounts for expiry, which the stored status does not", () => {
  const now = new Date("2026-09-06T12:00:00Z");
  const cases: {
    name: string;
    key: { status: string; expiresAt: string | null; revokedAt: string | null };
    want: string;
  }[] = [
    {
      name: "active and not yet expired",
      key: { status: "active", expiresAt: "2026-12-01T00:00:00Z", revokedAt: null },
      want: "active",
    },
    {
      name: "active with no expiry at all",
      key: { status: "active", expiresAt: null, revokedAt: null },
      want: "active",
    },
    {
      name: "stored active but past its expiry",
      key: { status: "active", expiresAt: "2026-09-06T11:59:59Z", revokedAt: null },
      want: "expired",
    },
    {
      name: "expiring exactly now is already over",
      key: { status: "active", expiresAt: "2026-09-06T12:00:00Z", revokedAt: null },
      want: "expired",
    },
    {
      name: "revoked by status",
      key: { status: "revoked", expiresAt: "2026-12-01T00:00:00Z", revokedAt: null },
      want: "revoked",
    },
    {
      name: "revoked wins over expired",
      key: { status: "revoked", expiresAt: "2020-01-01T00:00:00Z", revokedAt: "2026-09-05T00:00:00Z" },
      want: "revoked",
    },
    {
      name: "a revocation timestamp without the status",
      key: { status: "active", expiresAt: null, revokedAt: "2026-09-05T00:00:00Z" },
      want: "revoked",
    },
    {
      name: "an unfamiliar status is shown, not guessed",
      key: { status: "suspended", expiresAt: null, revokedAt: null },
      want: "unknown",
    },
  ];

  for (const item of cases) {
    assert.equal(apiKeyState(item.key, now).kind, item.want, item.name);
  }
});

test("every TTL choice is explicit, and never is the API's negative value", () => {
  assert.equal(ttlSeconds("never"), -1);
  assert.equal(ttlSeconds(DEFAULT_TTL_CHOICE), 90 * 24 * 60 * 60);
  assert.equal(ttlSeconds("365d"), 365 * 24 * 60 * 60);
  assert.equal(ttlSeconds("unknown-choice"), null);

  for (const choice of TTL_CHOICES) {
    assert.notEqual(
      choice.seconds,
      0,
      `${choice.value}: zero would mean "whatever the deployment defaults to"`
    );
    assert.ok(
      choice.seconds <= 365 * 24 * 60 * 60,
      `${choice.value}: the control plane refuses more than MaxKeyLifetime`
    );
  }
});

test("a local filter matches the fields a view names, case-insensitively", () => {
  const fields = ["Owner One", "owner@example.com", "owner"];
  const cases: { name: string; query: string; want: boolean }[] = [
    { name: "empty query keeps every row", query: "", want: true },
    { name: "whitespace only keeps every row", query: "   ", want: true },
    { name: "case-insensitive name", query: "OWNER one", want: true },
    { name: "substring of an email", query: "@example", want: true },
    { name: "no match", query: "member", want: false },
  ];

  for (const item of cases) {
    assert.equal(matchesQuery(fields, item.query), item.want, item.name);
  }
});

test("zero is unlimited, and is never shown as a bare 0", () => {
  const cases: { name: string; value: number; want: string }[] = [
    { name: "zero means unlimited", value: 0, want: "不限制" },
    { name: "a small limit", value: 60, want: "60" },
    { name: "a large limit is grouped", value: 90000, want: "90,000" },
  ];
  for (const item of cases) {
    assert.equal(formatLimit(item.value), item.want, item.name);
  }
});

test("a quota input is refused unless it is a non-negative integer", () => {
  const cases: { name: string; input: string; ok: boolean }[] = [
    { name: "zero, meaning unlimited", input: "0", ok: true },
    { name: "a plain number", input: "600", ok: true },
    { name: "surrounding whitespace", input: " 600 ", ok: true },
    // An empty box must not be read as zero: zero removes a limit, and this
    // endpoint writes all three dimensions at once, so a blank left next to
    // the field somebody did edit would be submitted too.
    //
    // 空输入框不能被读作零：零会移除一条限制，而这个端点是三项一次性写入的，因此
    // 留在被编辑字段旁边的那个空白也会一并提交。
    { name: "empty", input: "", ok: false },
    { name: "whitespace only", input: "   ", ok: false },
    { name: "negative", input: "-1", ok: false },
    { name: "fractional", input: "1.5", ok: false },
    { name: "not a number", input: "many", ok: false },
    { name: "beyond safe integers", input: "99999999999999999999", ok: false },
  ];
  for (const item of cases) {
    assert.equal(limitProblem(item.input) === null, item.ok, `${item.name}: ${JSON.stringify(item.input)}`);
  }
});

test("an unfamiliar job state is treated as still moving", () => {
  const cases: { name: string; state: string; terminal: boolean }[] = [
    { name: "succeeded", state: "succeeded", terminal: true },
    { name: "failed", state: "failed", terminal: true },
    { name: "cancelled", state: "cancelled", terminal: true },
    { name: "running", state: "running", terminal: false },
    { name: "pending", state: "pending", terminal: false },
    // A state this build does not know may still be moving. Calling it
    // settled would assert something the Gateway never claimed.
    //
    // 本次构建不认识的状态可能仍在变化。把它称作已结束，是在断言一件 Gateway 从未
    // 声称过的事。
    { name: "an unknown state", state: "reticulating", terminal: false },
    { name: "an empty state", state: "", terminal: false },
  ];
  for (const item of cases) {
    assert.equal(isTerminalJobState(item.state), item.terminal, item.name);
  }
});

test("a non-terminal state carries how long ago it was observed", () => {
  const now = new Date("2026-01-01T12:00:00Z");
  const cases: { name: string; observedAt: string; label: string }[] = [
    { name: "seconds ago", observedAt: "2026-01-01T11:59:40Z", label: "刚刚" },
    { name: "minutes ago", observedAt: "2026-01-01T11:43:00Z", label: "17 分钟前" },
    { name: "hours ago", observedAt: "2026-01-01T09:00:00Z", label: "3 小时前" },
    { name: "days ago", observedAt: "2025-12-30T12:00:00Z", label: "2 天前" },
  ];
  for (const item of cases) {
    assert.equal(observationAge(item.observedAt, now)?.label, item.label, item.name);
  }
  // An unparseable timestamp yields nothing rather than an age of zero: "just
  // now" would be the most misleading answer available.
  //
  // 无法解析的时间戳什么都不返回，而不是给出零龄：「刚刚」会是这里最具误导性的答案。
  assert.equal(observationAge("not-a-time", now), null);
});

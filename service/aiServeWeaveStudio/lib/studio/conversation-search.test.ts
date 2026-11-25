import assert from "node:assert/strict";
import { test } from "node:test";

import { formatUpdatedAt, highlightTitle, MAX_SEARCH_RUNES, normalizeSearch, searchURL } from "./conversation-search.ts";

test("normalizeSearch trims and caps the query by code points", () => {
  const cases: [string, string][] = [
    ["", ""],
    ["   ", ""],
    ["  Go 并发 ", "Go 并发"],
    ["好".repeat(MAX_SEARCH_RUNES), "好".repeat(MAX_SEARCH_RUNES)],
    ["好".repeat(MAX_SEARCH_RUNES + 1), "好".repeat(MAX_SEARCH_RUNES)],
    ["😀".repeat(MAX_SEARCH_RUNES + 5), "😀".repeat(MAX_SEARCH_RUNES)],
    ["a".repeat(MAX_SEARCH_RUNES - 1) + " b", "a".repeat(MAX_SEARCH_RUNES - 1)],
  ];
  for (const [raw, want] of cases) assert.equal(normalizeSearch(raw), want, JSON.stringify(raw).slice(0, 40));
});

test("searchURL asks for one page, with the query and cursor only when set", () => {
  const cases: [string, string, string][] = [
    ["", "", "/api/studio/conversations?limit=20"],
    ["  ", "", "/api/studio/conversations?limit=20"],
    ["Go", "", "/api/studio/conversations?limit=20&q=Go"],
    ["50% & 更多", "abc", "/api/studio/conversations?limit=20&q=50%25+%26+%E6%9B%B4%E5%A4%9A&cursor=abc"],
    ["", "abc", "/api/studio/conversations?limit=20&cursor=abc"],
  ];
  for (const [query, cursor, want] of cases) assert.equal(searchURL(query, cursor), want);
});

test("highlightTitle marks every case-insensitive match", () => {
  const mark = (title: string, query: string) => highlightTitle(title, query).map((p) => (p.match ? `[${p.text}]` : p.text)).join("");
  const cases: [string, string, string][] = [
    ["Go 并发", "", "Go 并发"],
    ["Go 并发", "go", "[Go] 并发"],
    ["go and GO", "Go", "[go] and [GO]"],
    ["并发并发", "并发", "[并发][并发]"],
    ["aaa", "aa", "[aa]a"],
    ["Go 并发", "rust", "Go 并发"],
    ["Go 并发", "  并发  ", "Go [并发]"],
    ["İstanbul", "i", "İstanbul"],
  ];
  for (const [title, query, want] of cases) assert.equal(mark(title, query), want, `${title} / ${query}`);
  assert.deepEqual(highlightTitle("", "go"), []);
});

test("formatUpdatedAt labels today, yesterday, this year and earlier", () => {
  const now = new Date(2026, 10, 25, 9, 30);
  const local = (y: number, m: number, d: number, h = 0, min = 0) => new Date(y, m - 1, d, h, min).toISOString();
  const cases: [string, string][] = [
    [local(2026, 11, 25, 8, 5), "08:05"],
    [local(2026, 11, 25, 0, 0), "00:00"],
    [local(2026, 11, 25, 23, 59), "23:59"],
    [local(2026, 11, 26, 1, 0), "01:00"],
    [local(2026, 11, 24, 23, 59), "昨天"],
    [local(2026, 11, 23, 12, 0), "11月23日"],
    [local(2026, 1, 1), "1月1日"],
    [local(2025, 12, 31), "2025年12月31日"],
    ["", ""],
    ["not a date", ""],
  ];
  for (const [iso, want] of cases) assert.equal(formatUpdatedAt(iso, now), want, iso);
});

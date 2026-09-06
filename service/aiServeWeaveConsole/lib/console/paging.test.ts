import assert from "node:assert/strict";
import test from "node:test";

import {
  advanceHistory,
  currentCursor,
  INITIAL_HISTORY,
  MAX_PAGE_HISTORY,
  pageQuery,
  retreatHistory,
} from "./paging.ts";

test("walking forward and back returns to the same positions", () => {
  let history = [...INITIAL_HISTORY];
  assert.equal(currentCursor(history), "", "the first page is read with no cursor");

  history = advanceHistory(history, "cursor-1");
  history = advanceHistory(history, "cursor-2");
  assert.equal(currentCursor(history), "cursor-2");
  assert.equal(history.length, 3, "three positions visited");

  history = retreatHistory(history);
  assert.equal(currentCursor(history), "cursor-1");
  history = retreatHistory(history);
  assert.equal(currentCursor(history), "");
});

test("the first page is never dropped, however often back is pressed", () => {
  let history = [...INITIAL_HISTORY];
  for (let i = 0; i < 5; i += 1) {
    history = retreatHistory(history);
  }
  assert.deepEqual(history, [""], "back from page one stays on page one");
  assert.equal(currentCursor(history), "");
});

test("paging history is bounded, so a long session cannot grow without limit", () => {
  let history = [...INITIAL_HISTORY];
  for (let i = 0; i < MAX_PAGE_HISTORY * 3; i += 1) {
    history = advanceHistory(history, `cursor-${i}`);
  }

  assert.equal(history.length, MAX_PAGE_HISTORY, "history is capped");
  assert.equal(
    currentCursor(history),
    `cursor-${MAX_PAGE_HISTORY * 3 - 1}`,
    "the newest position is kept"
  );
  assert.equal(
    history.includes(""),
    false,
    "the oldest positions were dropped, including page one"
  );
});

test("a page request carries the limit, the cursor, and only the filters in use", () => {
  const cases: {
    name: string;
    filters: Record<string, string>;
    cursor: string;
    want: Record<string, string>;
  }[] = [
    {
      name: "first page, no filters",
      filters: { q: "", role: "" },
      cursor: "",
      want: { limit: "50" },
    },
    {
      name: "a filter in use",
      filters: { q: "ada", role: "" },
      cursor: "",
      want: { limit: "50", q: "ada" },
    },
    {
      name: "two filters and a cursor",
      filters: { q: "ada", role: "admin" },
      cursor: "cursor-1",
      want: { limit: "50", q: "ada", role: "admin", cursor: "cursor-1" },
    },
    {
      name: "a filter whose value is a zero-like string is still sent",
      filters: { limit_override: "0" },
      cursor: "",
      want: { limit: "50", limit_override: "0" },
    },
  ];

  for (const item of cases) {
    assert.deepEqual(pageQuery(item.filters, 50, item.cursor), item.want, item.name);
  }
});

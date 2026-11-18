import assert from "node:assert/strict";
import { test } from "node:test";

import { parseCatalogHistoryDetail } from "./catalog-history.ts";

test("parseCatalogHistoryDetail reads an approval", () => {
  assert.deepEqual(parseCatalogHistoryDetail("kind=change input=2500000→3000000 output=-→10000000 version=7 model=gpt-4o"), {
    kind: "change", inputBefore: 2500000, inputAfter: 3000000, outputBefore: null, outputAfter: 10000000, maxChange: "",
  });
});

test("parseCatalogHistoryDetail reads a rejection and an automatic application", () => {
  assert.deepEqual(parseCatalogHistoryDetail("kind=remove model=x"), {
    kind: "remove", inputBefore: null, inputAfter: null, outputBefore: null, outputAfter: null, maxChange: "",
  });
  assert.equal(parseCatalogHistoryDetail("max_change=12.50% version=3 model=x").maxChange, "12.50%");
});

test("parseCatalogHistoryDetail ignores malformed parts", () => {
  assert.deepEqual(parseCatalogHistoryDetail("garbage =x input=abc→1.5"), {
    kind: "", inputBefore: null, inputAfter: null, outputBefore: null, outputAfter: null, maxChange: "",
  });
});

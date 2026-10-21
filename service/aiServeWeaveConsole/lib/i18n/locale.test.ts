import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { parseLocale, resolveLocale } from "./locale.ts";

const cases = JSON.parse(readFileSync(new URL("../../../../common/browserlang/testdata/negotiation.json", import.meta.url), "utf8")) as { name: string; saved: string; accept: string; want: string }[];
for (const c of cases) test(`negotiate: ${c.name}`, () => {
  assert.equal(resolveLocale(c.saved, c.accept), c.want);
});
test("cookie values cannot become arbitrary module paths or aliases", () => {
  for (const raw of ["de", "DE-de", "../../de-DE", "de-DE\n", "__proto__", ""]) assert.equal(parseLocale(raw), null, raw);
  assert.equal(parseLocale("de-DE"), "de-DE");
});

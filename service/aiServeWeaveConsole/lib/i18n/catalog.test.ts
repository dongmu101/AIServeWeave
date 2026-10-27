import assert from "node:assert/strict";
import test from "node:test";
import { LOCALES } from "./locale.ts";
import { loadMessages } from "./messages.ts";

function parts(entry: string | {one?: string; other: string}) {return typeof entry === "string" ? [entry] : Object.values(entry);}
function params(text: string) {return [...new Set([...text.matchAll(/(?<!\{)\{([a-zA-Z]\w*)\}(?!\})/g)].map(x=>x[1]))].sort();}
test("every locale has complete, nonempty entries and matching named parameters", async () => {
 const base = await loadMessages("zh-CN");
 for (const locale of LOCALES) {
  const messages = await loadMessages(locale);
  assert.deepEqual(Object.keys(messages).sort(), Object.keys(base).sort(), locale);
  for (const key of Object.keys(base) as (keyof typeof base)[]) {
   for (const text of parts(messages[key])) {
    assert.ok(text.trim(), `${locale}: ${key}`);
    assert.deepEqual(params(text), params(parts(base[key])[0]), `${locale}: ${key}`);
   }
  }
 }
});

import assert from "node:assert/strict";
import test from "node:test";
import { createLocaleSelection } from "./selection.ts";
import { loadMessages, type Messages } from "./messages.ts";
import type { Locale } from "./locale.ts";

function deferred() {
  let resolve!: (value: Messages) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<Messages>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test("only the latest selection commits when catalogues arrive out of order", async () => {
  const de = deferred(); const fr = deferred();
  const committed: Locale[] = [];
  const selection = createLocaleSelection("zh-CN", locale => locale === "de-DE" ? de.promise : fr.promise, next => committed.push(next.locale));
  const first = selection.select("de-DE"); const last = selection.select("fr-FR");
  fr.resolve(await loadMessages("fr-FR")); await last;
  de.resolve(await loadMessages("de-DE")); await first;
  assert.deepEqual(committed, ["fr-FR"]);
});

test("a failed current selection leaves the interface and preference untouched", async () => {
  const writes: Locale[] = [];
  const selection = createLocaleSelection("ja-JP", async () => { throw new Error("offline"); }, next => writes.push(next.locale));
  await assert.rejects(selection.select("en-US"), /offline/);
  assert.deepEqual(writes, []);
});

test("selecting the active language cancels an older pending change", async () => {
  const pending = deferred(); const writes: Locale[] = [];
  const selection = createLocaleSelection("ja-JP", () => pending.promise, next => writes.push(next.locale));
  const old = selection.select("de-DE");
  await selection.select("ja-JP");
  pending.resolve(await loadMessages("de-DE")); await old;
  assert.deepEqual(writes, []);
});

test("stale failures do not replace a successful newer selection", async () => {
  const old = deferred(); const writes: Locale[] = [];
  const selection = createLocaleSelection("zh-CN", locale => locale === "de-DE" ? old.promise : loadMessages(locale), next => writes.push(next.locale));
  const first = selection.select("de-DE"); await selection.select("fr-FR");
  old.reject(new Error("late failure")); await first;
  assert.deepEqual(writes, ["fr-FR"]);
  await assert.rejects(selection.select("../../xx" as Locale), /locale/);
});

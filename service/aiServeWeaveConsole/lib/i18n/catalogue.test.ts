import assert from "node:assert/strict";
import test from "node:test";
import { catalogue, createCatalogue, loadMessages, type Messages } from "./messages.ts";
import { LOCALES, type Locale } from "./locale.ts";

function counting(result: (locale: Locale, call: number) => Promise<Messages>) {
  const calls: Locale[] = [];
  const load = createCatalogue(locale => { calls.push(locale); return result(locale, calls.length); });
  return { load, calls };
}

const empty = {} as Messages;

test("each locale is loaded once and every caller gets the same promise", async () => {
  const { load, calls } = counting(async () => empty);
  const first = load("de-DE");
  assert.equal(load("de-DE"), first);
  assert.notEqual(load("fr-FR"), first);
  await first;
  assert.equal(load("de-DE"), first);
  assert.deepEqual(calls, ["de-DE", "fr-FR"]);
});

test("a failed load is forgotten so the next call retries", async () => {
  const { load, calls } = counting(async (_, call) => { if (call === 1) throw new Error("offline"); return empty; });
  await assert.rejects(load("ja-JP"), /offline/);
  assert.equal(await load("ja-JP"), empty);
  assert.deepEqual(calls, ["ja-JP", "ja-JP"]);
});

test("a retry that started before the failure settled is not dropped", async () => {
  let fail!: (error: Error) => void;
  const { load } = counting((_, call) => call === 1 ? new Promise<Messages>((_, no) => { fail = no; }) : Promise.resolve(empty));
  const failing = load("ko-KR");
  fail(new Error("offline"));
  await assert.rejects(failing);
  const retried = load("ko-KR");
  await retried;
  assert.equal(load("ko-KR"), retried);
});

test("the shared catalogue serves the real catalogue of every locale", async () => {
  for (const locale of LOCALES) assert.deepEqual(await catalogue(locale), await loadMessages(locale));
});

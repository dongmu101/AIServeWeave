import assert from "node:assert/strict";
import test from "node:test";
import { createTtlCache } from "./ttl-cache.ts";

function harness(results: Array<string | Error>, ttlMs = 1000) {
  let clock = 0;
  let calls = 0;
  const pending: Array<() => void> = [];
  let hold = false;
  const read = createTtlCache(() => {
    const result = results[Math.min(calls, results.length - 1)];
    calls += 1;
    const settle = () => result instanceof Error ? Promise.reject(result) : Promise.resolve(result);
    if (!hold) return settle();
    return new Promise<string>((yes, no) => pending.push(() => settle().then(yes, no)));
  }, { ttlMs, keep: value => value !== "failed", now: () => clock });
  return {
    read,
    calls: () => calls,
    advance: (ms: number) => { clock += ms; },
    holdLoads: () => { hold = true; },
    release: () => { hold = false; pending.splice(0).forEach(settle => settle()); },
  };
}

test("a kept result is reused until it expires", async () => {
  const h = harness(["v1", "v2"]);
  assert.equal(await h.read(), "v1");
  h.advance(999);
  assert.equal(await h.read(), "v1");
  assert.equal(h.calls(), 1);
  h.advance(1);
  assert.equal(await h.read(), "v2");
  assert.equal(h.calls(), 2);
});

test("a result keep() rejects is returned but not stored", async () => {
  const h = harness(["failed", "v1"]);
  assert.equal(await h.read(), "failed");
  assert.equal(await h.read(), "v1");
  assert.equal(await h.read(), "v1");
  assert.equal(h.calls(), 2);
});

test("a thrown load reaches its callers and the next call retries", async () => {
  const h = harness([new Error("unreachable"), "v1"]);
  await assert.rejects(h.read(), /unreachable/);
  assert.equal(await h.read(), "v1");
  assert.equal(h.calls(), 2);
});

test("callers arriving during a load share it", async () => {
  const h = harness(["v1"]);
  h.holdLoads();
  const all = Promise.all([h.read(), h.read(), h.read()]);
  h.release();
  assert.deepEqual(await all, ["v1", "v1", "v1"]);
  assert.equal(h.calls(), 1);
});

test("an expiry under load costs one upstream call", async () => {
  const h = harness(["v1", "v2"]);
  await h.read();
  h.advance(1000);
  h.holdLoads();
  const all = Promise.all([h.read(), h.read()]);
  h.release();
  assert.deepEqual(await all, ["v2", "v2"]);
  assert.equal(h.calls(), 2);
});

test("a shared failed load is not stored for later callers", async () => {
  const h = harness(["failed", "v1"]);
  h.holdLoads();
  const all = Promise.all([h.read(), h.read()]);
  h.release();
  assert.deepEqual(await all, ["failed", "failed"]);
  assert.equal(await h.read(), "v1");
  assert.equal(h.calls(), 2);
});

test("a zero ttl stores nothing", async () => {
  const h = harness(["v1", "v2"], 0);
  assert.equal(await h.read(), "v1");
  assert.equal(await h.read(), "v2");
});

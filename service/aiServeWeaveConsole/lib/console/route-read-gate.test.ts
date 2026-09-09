import { test } from "node:test";
import assert from "node:assert/strict";
import { createRouteReadGate } from "./route-read-gate.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

test("delayed initial read cannot overwrite a newer current version and edited draft", async () => {
  const gate = createRouteReadGate();
  const initial = deferred<string>(), refresh = deferred<string>();
  let current = "", draft = "";
  const acceptInitial = gate.begin("current");
  const initialDone = initial.promise.then((value) => { if (acceptInitial()) { current = value; draft = value; } });
  const acceptRefresh = gate.begin("current");
  const refreshDone = refresh.promise.then((value) => { if (acceptRefresh()) current = value; });
  refresh.resolve("revision 2"); await refreshDone;
  draft = "local edit";
  initial.resolve("revision 1"); await initialDone;
  assert.deepEqual({ current, draft }, { current: "revision 2", draft: "local edit" });
});

test("publication invalidates outstanding status and history including complete observations", async () => {
  const gate = createRouteReadGate();
  const staleStatus = deferred<boolean>(), staleHistory = deferred<number>();
  let complete = false, historyRevision = 2;
  const acceptStatus = gate.begin("status"), acceptHistory = gate.begin("history");
  const statusDone = staleStatus.promise.then((value) => { if (acceptStatus()) complete = value; });
  const historyDone = staleHistory.promise.then((value) => { if (acceptHistory()) historyRevision = value; });
  gate.invalidate();
  const acceptFreshStatus = gate.begin("status");
  if (acceptFreshStatus()) complete = false;
  staleStatus.resolve(true); staleHistory.resolve(1);
  await Promise.all([statusDone, historyDone]);
  assert.deepEqual({ complete, historyRevision }, { complete: false, historyRevision: 2 });
});

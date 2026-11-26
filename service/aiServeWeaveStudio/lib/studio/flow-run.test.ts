import assert from "node:assert/strict";
import { test } from "node:test";

import { applyFrame, duration, initialRunState, type RunState } from "./flow-run.ts";
import { parseRun } from "./flows.ts";
import { SSEParser } from "./sse.ts";

const snapshot = `event: run_snapshot
data: {"run":{"id":"frn_1","flow_id":"flw_1","flow_version":1,"status":"running","usage":{},"created_at":"2026-11-26T08:00:00Z","steps":[{"node_id":"start","node_type":"input","status":"succeeded","started_at":"a","finished_at":"b"}]},"running":[{"node_id":"draft","text":"Hel"}]}

`;

function play(text: string, from?: RunState): RunState {
  let state = from ?? initialRunState(parseRun({ id: "frn_1", status: "running" }));
  for (const frame of new SSEParser().push(text)) state = applyFrame(state, frame);
  return state;
}

test("a snapshot restores the steps and the text still arriving", () => {
  const state = play(snapshot);
  assert.equal(state.run.steps.length, 1);
  assert.deepEqual(state.nodes, { start: "succeeded", draft: "running" });
  assert.deepEqual(state.live, { draft: "Hel" });
  assert.equal(state.finished, false);
});

test("live frames move the run on, and the end freezes it", () => {
  let state = play(snapshot);
  state = play(
    `event: node_delta
data: {"node_id":"draft","delta":"lo"}

: keep-alive

event: node_finished
data: {"node_id":"draft","node_type":"llm","status":"succeeded","outputs":{"text":"Hello"},"usage":{"prompt_tokens":4,"completion_tokens":1},"started_at":"2026-11-26T08:00:00Z","finished_at":"2026-11-26T08:00:02Z"}

event: node_started
data: {"node_id":"fix","started_at":"2026-11-26T08:00:02Z"}

event: node_skipped
data: {"node_id":"other","node_type":"template","status":"skipped","started_at":"x","finished_at":"x"}

`,
    state,
  );
  assert.equal(state.live.draft, undefined);
  assert.deepEqual(state.nodes, { start: "succeeded", draft: "succeeded", fix: "running", other: "skipped" });
  assert.equal(state.run.steps.find((s) => s.nodeId === "draft")?.outputs.text, "Hello");
  assert.equal(state.started.fix, "2026-11-26T08:00:02Z");

  state = play(
    `event: run_finished
data: {"status":"canceled","outputs":{},"usage":{"prompt_tokens":4,"completion_tokens":1},"finished_at":"2026-11-26T08:00:05Z"}

`,
    state,
  );
  assert.equal(state.finished, true);
  assert.equal(state.run.status, "canceled");
  assert.equal(state.nodes.fix, "canceled");
  assert.deepEqual(state.run.usage, { promptTokens: 4, completionTokens: 1 });
});

test("frames that cannot be read leave the state as it was", () => {
  const before = play(snapshot);
  const after = play(
    `event: node_finished
data: {"node_id":"draft","status":"exploded"}

event: node_delta
data: {"delta":"no node"}

event: something_new
data: {}

`,
    before,
  );
  assert.deepEqual(after, before);
});

test("duration renders short and long spans", () => {
  assert.equal(duration("2026-11-26T08:00:00Z", "2026-11-26T08:00:00.250Z"), "250 毫秒");
  assert.equal(duration("2026-11-26T08:00:00Z", "2026-11-26T08:00:03.5Z"), "3.5 秒");
  assert.equal(duration("2026-11-26T08:00:00Z", "2026-11-26T08:02:05Z"), "2 分 5 秒");
  assert.equal(duration("", "2026-11-26T08:00:00Z"), "");
});

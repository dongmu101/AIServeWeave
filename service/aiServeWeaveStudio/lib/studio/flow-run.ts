import { parseRun, parseRunStep, type Run, type RunStatus, type RunStep } from "./flows.ts";
import type { GatewayFrame } from "./sse.ts";

/**
 * The run view's state, built from the Gateway's run event stream: a
 * run_snapshot frame resets it, and node and run frames move it on. Frames
 * it cannot read are ignored, so one bad frame does not end the view; a
 * reconnect starts again from a fresh snapshot.
 *
 * 运行视图的状态，由 Gateway 的运行事件流构建：run_snapshot 帧重置它，节点与运行帧推进
 * 它。读不懂的帧被忽略，一帧出错不会让视图终止；重连时从新的快照重新开始。
 */

export type NodeState = "running" | RunStep["status"];

export interface RunState {
  run: Run;
  /** text so far of the nodes whose model call is in flight
   * 模型调用仍在进行中的节点迄今的文本 */
  live: Record<string, string>;
  /** where each node stands; a node not yet reached is absent
   * 每个节点所处的状态；尚未到达的节点不在其中 */
  nodes: Record<string, NodeState>;
  started: Record<string, string>;
  finished: boolean;
}

const MAX_LIVE_CHARS = 200_000;

export function initialRunState(run: Run): RunState {
  const nodes: Record<string, NodeState> = {};
  for (const step of run.steps) nodes[step.nodeId] = step.status;
  return { run, live: {}, nodes, started: {}, finished: run.status !== "running" };
}

/** applyFrame returns the state after one frame. / applyFrame 返回应用一帧之后的状态。 */
export function applyFrame(state: RunState, frame: GatewayFrame): RunState {
  try {
    switch (frame.event) {
      case "run_snapshot":
        return fromSnapshot(frame.data);
      case "node_started": {
        const node = nodeId(frame.data);
        const startedAt = typeof frame.data.started_at === "string" ? frame.data.started_at : "";
        return { ...state, nodes: { ...state.nodes, [node]: "running" }, live: { ...state.live, [node]: "" }, started: { ...state.started, [node]: startedAt } };
      }
      case "node_delta": {
        const node = nodeId(frame.data);
        const delta = typeof frame.data.delta === "string" ? frame.data.delta : "";
        const text = (state.live[node] ?? "") + delta;
        return { ...state, live: { ...state.live, [node]: text.length > MAX_LIVE_CHARS ? text.slice(0, MAX_LIVE_CHARS) : text } };
      }
      case "node_finished":
      case "node_skipped": {
        const step = parseRunStep(frame.data);
        const live = { ...state.live };
        delete live[step.nodeId];
        const steps = [...state.run.steps.filter((s) => s.nodeId !== step.nodeId), step];
        return { ...state, run: { ...state.run, steps }, live, nodes: { ...state.nodes, [step.nodeId]: step.status } };
      }
      case "run_finished":
        return finish(state, frame.data);
      default:
        return state;
    }
  } catch {
    return state;
  }
}

function nodeId(data: Record<string, unknown>): string {
  if (typeof data.node_id !== "string" || data.node_id === "") throw new Error("frame without node");
  return data.node_id;
}

function fromSnapshot(data: Record<string, unknown>): RunState {
  const state = initialRunState(parseRun(data.run));
  const running = Array.isArray(data.running) ? data.running : [];
  for (const item of running) {
    if (typeof item !== "object" || item === null) continue;
    const entry = item as Record<string, unknown>;
    if (typeof entry.node_id !== "string") continue;
    state.nodes[entry.node_id] = "running";
    state.live[entry.node_id] = typeof entry.text === "string" ? entry.text : "";
  }
  return state;
}

const RUN_STATUSES: RunStatus[] = ["running", "succeeded", "failed", "canceled", "interrupted"];

function finish(state: RunState, data: Record<string, unknown>): RunState {
  const status = RUN_STATUSES.find((s) => s === data.status) ?? "failed";
  const outputs: Record<string, string> = {};
  if (typeof data.outputs === "object" && data.outputs !== null) {
    for (const [key, value] of Object.entries(data.outputs as Record<string, unknown>)) {
      if (typeof value === "string") outputs[key] = value;
    }
  }
  const usage = typeof data.usage === "object" && data.usage !== null ? (data.usage as Record<string, unknown>) : {};
  const nodes = { ...state.nodes };
  for (const [node, value] of Object.entries(nodes)) {
    // A node still marked running when the run ended was cut short.
    // 运行结束时仍标记为运行中的节点被中途截断了。
    if (value === "running") nodes[node] = "canceled";
  }
  return {
    ...state,
    finished: true,
    live: {},
    nodes,
    run: {
      ...state.run,
      status,
      errorCode: typeof data.error_code === "string" ? data.error_code : "",
      failedNode: typeof data.failed_node === "string" ? data.failed_node : "",
      outputs,
      usage: {
        promptTokens: typeof usage.prompt_tokens === "number" ? usage.prompt_tokens : 0,
        completionTokens: typeof usage.completion_tokens === "number" ? usage.completion_tokens : 0,
      },
      finishedAt: typeof data.finished_at === "string" ? data.finished_at : state.run.finishedAt,
    },
  };
}

/** duration renders the time between two instants, or "" when either is
 * missing.
 *
 * duration 渲染两个时刻之间的时长，任一缺失时为空字符串。 */
export function duration(from: string, to: string): string {
  const start = Date.parse(from);
  const end = Date.parse(to);
  if (Number.isNaN(start) || Number.isNaN(end) || end < start) return "";
  const ms = end - start;
  if (ms < 1000) return `${ms} 毫秒`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 1 : 0)} 秒`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes} 分 ${Math.round(seconds % 60)} 秒`;
}

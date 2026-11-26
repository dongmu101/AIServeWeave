import { redirect } from "next/navigation";

import { parseModels, studioErrorCode, type StudioModel } from "@/lib/studio/contract";
import {
  parseConversation,
  parseConversationPage,
  parseMessages,
  type Conversation,
  type ConversationPage,
  type StoredMessage,
} from "@/lib/studio/conversations";
import {
  parseFlow,
  parseFlowPage,
  parseFlowVersion,
  parseRun,
  parseRunPage,
  type Flow,
  type FlowDefinition,
  type FlowPage,
  type Run,
  type RunPage,
} from "@/lib/studio/flows";
import type { SessionPayload } from "@/lib/studio/session-payload";
import { readSession } from "@/lib/server/session";
import { callGateway } from "@/lib/server/upstream";

/**
 * Server-side reads of the Studio API for page rendering.
 * 页面渲染所用的 Studio API 服务端读取。
 */

/** requireSession returns the session or sends the visitor to sign in.
 * requireSession 返回会话，否则把访客送去登录。 */
export async function requireSession(returnTo: string): Promise<SessionPayload> {
  const session = await readSession();
  if (!session) redirect(`/login?next=${encodeURIComponent(returnTo)}`);
  return session;
}

export type ModelsLoad = { kind: "ok"; models: StudioModel[] } | { kind: "error"; code: string };

/** loadModels reads the user's models. A session the Gateway refuses is sent
 * through /api/session/expired, which clears the dead cookie before sign-in
 * so the sign-in page does not bounce straight back here.
 *
 * loadModels 读取用户可用的模型。Gateway 拒绝的会话会经 /api/session/expired 处理：
 * 先清除失效的 Cookie 再去登录，登录页才不会立刻把人弹回这里。 */
export async function loadModels(session: SessionPayload, returnTo: string): Promise<ModelsLoad> {
  const result = await callGateway({ method: "GET", path: "/studio/v1/models", token: session.token });
  if (result.kind !== "response") return { kind: "error", code: "upstream" };
  if (result.status === 401) redirect(`/api/session/expired?next=${encodeURIComponent(returnTo)}`);
  if (result.status !== 200) return { kind: "error", code: studioErrorCode(result.text) || "upstream" };
  try {
    return { kind: "ok", models: parseModels(JSON.parse(result.text)) };
  } catch {
    return { kind: "error", code: "upstream_contract" };
  }
}

export type ConversationsLoad = { kind: "ok"; page: ConversationPage } | { kind: "error"; code: string };

/** loadConversations reads the first page of the user's conversations for the
 * sidebar. A failure leaves the sidebar empty with a note rather than failing
 * the page.
 *
 * loadConversations 为侧边栏读取用户对话的第一页。失败时侧边栏为空并附提示，而不是让整个
 * 页面失败。 */
export async function loadConversations(session: SessionPayload, returnTo: string): Promise<ConversationsLoad> {
  const result = await callGateway({ method: "GET", path: "/studio/v1/conversations", search: "?limit=50", token: session.token });
  if (result.kind !== "response") return { kind: "error", code: "upstream" };
  if (result.status === 401) redirect(`/api/session/expired?next=${encodeURIComponent(returnTo)}`);
  if (result.status !== 200) return { kind: "error", code: studioErrorCode(result.text) || "upstream" };
  try {
    return { kind: "ok", page: parseConversationPage(JSON.parse(result.text)) };
  } catch {
    return { kind: "error", code: "upstream_contract" };
  }
}

export type ConversationLoad =
  | { kind: "ok"; conversation: Conversation; messages: StoredMessage[] }
  | { kind: "not_found" }
  | { kind: "error"; code: string };

/** loadConversation reads one conversation and its messages.
 * loadConversation 读取一个对话及其消息。 */
export async function loadConversation(session: SessionPayload, id: string, returnTo: string): Promise<ConversationLoad> {
  if (!/^cnv_[A-Za-z0-9_-]{1,36}$/.test(id)) return { kind: "not_found" };
  const [conversation, messages] = await Promise.all([
    callGateway({ method: "GET", path: `/studio/v1/conversations/${id}`, token: session.token }),
    callGateway({ method: "GET", path: `/studio/v1/conversations/${id}/messages`, token: session.token }),
  ]);
  const texts: string[] = [];
  for (const result of [conversation, messages]) {
    if (result.kind !== "response") return { kind: "error", code: "upstream" };
    if (result.status === 401) redirect(`/api/session/expired?next=${encodeURIComponent(returnTo)}`);
    if (result.status === 404) return { kind: "not_found" };
    if (result.status !== 200) return { kind: "error", code: studioErrorCode(result.text) || "upstream" };
    texts.push(result.text);
  }
  try {
    return { kind: "ok", conversation: parseConversation(JSON.parse(texts[0])), messages: parseMessages(JSON.parse(texts[1])) };
  } catch {
    return { kind: "error", code: "upstream_contract" };
  }
}

export type Load<T> = { kind: "ok"; value: T } | { kind: "not_found" } | { kind: "error"; code: string };

/** loadStudio reads one Studio API document and parses it. A refused session
 * goes to sign-in; 404 is reported as such so the page can say so.
 *
 * loadStudio 读取一份 Studio API 文档并解析。被拒绝的会话转去登录；404 如实报告，页面
 * 据此提示。 */
async function loadStudio<T>(session: SessionPayload, path: string, returnTo: string, parse: (value: unknown) => T, search = ""): Promise<Load<T>> {
  const result = await callGateway({ method: "GET", path, search, token: session.token });
  if (result.kind !== "response") return { kind: "error", code: "upstream" };
  if (result.status === 401) redirect(`/api/session/expired?next=${encodeURIComponent(returnTo)}`);
  if (result.status === 404) return { kind: "not_found" };
  if (result.status !== 200) return { kind: "error", code: studioErrorCode(result.text) || "upstream" };
  try {
    return { kind: "ok", value: parse(JSON.parse(result.text)) };
  } catch {
    return { kind: "error", code: "upstream_contract" };
  }
}

const FLOW_ID = /^flw_[A-Za-z0-9_-]{1,36}$/;
const RUN_ID = /^frn_[A-Za-z0-9_-]{1,36}$/;

/** loadFlows reads the first page of the user's flows. / loadFlows 读取用户编排的第一页。 */
export function loadFlows(session: SessionPayload, returnTo: string): Promise<Load<FlowPage>> {
  return loadStudio(session, "/studio/v1/flows", returnTo, parseFlowPage, "?limit=100");
}

/** loadFlow reads one flow with its latest definition. / loadFlow 读取一个编排及其最新定义。 */
export function loadFlow(session: SessionPayload, id: string, returnTo: string): Promise<Load<Flow>> {
  if (!FLOW_ID.test(id)) return Promise.resolve({ kind: "not_found" });
  return loadStudio(session, `/studio/v1/flows/${id}`, returnTo, parseFlow);
}

/** loadFlowVersion reads the definition a run ran. / loadFlowVersion 读取某次运行所用的定义。 */
export function loadFlowVersion(session: SessionPayload, id: string, version: number, returnTo: string): Promise<Load<{ version: number; definition: FlowDefinition }>> {
  if (!FLOW_ID.test(id) || !Number.isInteger(version) || version < 1) return Promise.resolve({ kind: "not_found" });
  return loadStudio(session, `/studio/v1/flows/${id}/versions/${version}`, returnTo, parseFlowVersion);
}

/** loadRun reads one run with its steps. / loadRun 读取一次运行及其步骤。 */
export function loadRun(session: SessionPayload, id: string, returnTo: string): Promise<Load<Run>> {
  if (!RUN_ID.test(id)) return Promise.resolve({ kind: "not_found" });
  return loadStudio(session, `/studio/v1/runs/${id}`, returnTo, parseRun);
}

/** loadRuns reads one page of a flow's runs. / loadRuns 读取编排运行记录的一页。 */
export function loadRuns(session: SessionPayload, flowId: string, returnTo: string, cursor = ""): Promise<Load<RunPage>> {
  if (!FLOW_ID.test(flowId)) return Promise.resolve({ kind: "not_found" });
  const search = new URLSearchParams({ limit: "30" });
  if (cursor) search.set("cursor", cursor);
  return loadStudio(session, `/studio/v1/flows/${flowId}/runs`, returnTo, parseRunPage, `?${search}`);
}

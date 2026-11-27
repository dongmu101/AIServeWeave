"use client";

import { sendToSignIn } from "@/components/studio/session-expired";
import { errorMessage } from "@/lib/studio/contract";
import { flowErrorText, parseIssues, parseRun, type FlowIssue, type Run } from "@/lib/studio/flows";
import { runStartBody, type BuiltRun } from "@/lib/studio/run-inputs";

/**
 * Browser calls to the flow and run endpoints through /api/studio. A session
 * the Gateway refuses sends the user to sign in; a refusal carries its stable
 * code and, for a flow or its inputs, the issues to show.
 *
 * 浏览器经 /api/studio 对编排与运行端点的调用。Gateway 拒绝的会话把用户送去登录；拒绝时
 * 带有稳定错误码，对编排或其输入还带有要展示的问题列表。
 */

export type CallResult<T> =
  | { ok: true; data: T }
  | { ok: false; status: number; code: string; issues: FlowIssue[] };

export async function studioCall<T>(method: "GET" | "POST" | "PATCH" | "DELETE", path: string, body?: unknown, parse?: (value: unknown) => T): Promise<CallResult<T>> {
  let response: Response;
  try {
    // A FormData goes as it is: the browser writes its multipart type with the
    // boundary, which must not be overridden.
    // FormData 原样发送：浏览器会写入带分隔符的 multipart 类型，不能覆盖。
    const form = typeof FormData !== "undefined" && body instanceof FormData;
    response = await fetch(`/api/studio/${path}`, {
      method,
      headers: body === undefined || form ? undefined : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : form ? (body as FormData) : JSON.stringify(body),
      cache: "no-store",
    });
  } catch {
    return { ok: false, status: 0, code: "upstream", issues: [] };
  }
  if (response.status === 401) {
    sendToSignIn(window.location.pathname);
    return { ok: false, status: 401, code: "unauthorized", issues: [] };
  }
  const text = await response.text().catch(() => "");
  let parsed: unknown = null;
  try {
    parsed = text ? JSON.parse(text) : null;
  } catch {
    parsed = null;
  }
  if (!response.ok) {
    let code = typeof parsed === "object" && parsed !== null && typeof (parsed as { error?: unknown }).error === "string" ? (parsed as { error: string }).error : "upstream";
    // A 413 from a proxy in front of Studio carries no code of ours.
    // Studio 前面的代理给出的 413 不带我们的错误码。
    if (response.status === 413 && code === "upstream") code = "too_large";
    return { ok: false, status: response.status, code, issues: parseIssues(parsed) };
  }
  if (!parse) return { ok: true, data: undefined as T };
  try {
    return { ok: true, data: parse(parsed) };
  } catch {
    return { ok: false, status: response.status, code: "upstream_contract", issues: [] };
  }
}

/** startFlowRun starts a run of a flow, uploading the files that go with it.
 * startFlowRun 启动编排的一次运行，并上传随之而来的文件。 */
export function startFlowRun(flowId: string, built: BuiltRun): Promise<CallResult<Run>> {
  return studioCall("POST", `flows/${flowId}/runs/start`, runStartBody(built), parseRun);
}

/** failureText is the wording for a refused call. / failureText 是调用被拒绝时的文案。 */
export function failureText(code: string): string {
  return flowErrorText(code, errorMessage);
}

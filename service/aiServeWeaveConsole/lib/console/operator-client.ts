import { ApiError, kindForStatus } from "./errors.ts";
import type { OperatorIdentity } from "./operator-session.ts";

/** operatorSignIn exchanges platform credentials without exposing the upstream token. / operatorSignIn 换取平台会话，不暴露上游令牌。 */
export async function operatorSignIn(email: string, password: string, fetchImpl: typeof fetch = fetch): Promise<OperatorIdentity> {
  const response = await call("POST", { email, password }, fetchImpl);
  if (!response.ok) throw new ApiError(response.status === 401 ? "invalid_credentials" : kindForStatus(response.status), response.status);
  let value;
  try { value = await response.json(); } catch { throw new ApiError("contract"); }
  const operator = value?.operator;
  if (!operator || typeof operator.id !== "string" || typeof operator.email !== "string" || typeof operator.name !== "string") throw new ApiError("contract");
  return { id: operator.id, email: operator.email, name: operator.name };
}

/** operatorSignOut clears only the platform session cookie. / operatorSignOut 仅清除平台会话 Cookie。 */
export async function operatorSignOut(fetchImpl: typeof fetch = fetch): Promise<void> {
  const response = await call("DELETE", undefined, fetchImpl);
  if (!response.ok && response.status !== 401) throw new ApiError(kindForStatus(response.status), response.status);
}

/** call bounds one session request; writes are never retried. / call 限定一次会话请求，写操作从不重试。 */
async function call(method: string, body: unknown, fetchImpl: typeof fetch): Promise<Response> {
  try {
    return await fetchImpl("/api/operator-session", { method, credentials: "same-origin", cache: "no-store", headers: {"Content-Type": "application/json"},
      body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(15_000) });
  } catch (error) {
    throw new ApiError(error instanceof DOMException && error.name === "TimeoutError" ? "timeout" : "network");
  }
}

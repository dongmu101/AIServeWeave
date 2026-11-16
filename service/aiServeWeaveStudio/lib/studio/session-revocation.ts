/** LogoutUpstream is the part of a ControlPlane result that determines
 * whether the local cookie may be cleared.
 *
 * LogoutUpstream 是决定本地 Cookie 能否清除的 ControlPlane 结果部分。 */
export type LogoutUpstream =
  | { kind: "response"; status: number }
  | { kind: "timeout" }
  | { kind: "unreachable" }
  | null;

/** logoutDisposition keeps a retryable local session when server-side
 * revocation could not be confirmed.
 *
 * logoutDisposition 在无法确认服务端吊销时保留可重试的本地会话。 */
export function logoutDisposition(result: LogoutUpstream): {
  clear: boolean;
  status: number;
  error: "upstream" | null;
} {
  if (result === null) {
    return { clear: true, status: 204, error: null };
  }
  if (result.kind === "timeout") {
    return { clear: false, status: 504, error: "upstream" };
  }
  if (result.kind === "unreachable") {
    return { clear: false, status: 502, error: "upstream" };
  }
  if (result.status === 204 || result.status === 401) {
    return { clear: true, status: 204, error: null };
  }
  return { clear: false, status: result.status, error: "upstream" };
}

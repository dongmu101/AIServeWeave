import { cookies } from "next/headers";

import {
  isExpired,
  open,
  seal,
  SESSION_COOKIE,
  type SessionPayload,
} from "@/lib/console/session-payload";
import { serverConfig } from "@/lib/server/config";

/**
 * readSession returns the caller's session, or null when there is none, it
 * cannot be opened, or the control plane token in it has expired.
 *
 * readSession 返回调用方的会话；没有会话、无法打开、或其中的控制面令牌已过期时返回
 * null。
 */
export async function readSession(
  now: Date = new Date()
): Promise<SessionPayload | null> {
  const raw = (await cookies()).get(SESSION_COOKIE)?.value;
  if (!raw) {
    return null;
  }
  const payload = open(raw, serverConfig().sessionKey);
  if (!payload || isExpired(payload, now)) {
    return null;
  }
  return payload;
}

/**
 * writeSession stores a session in the response.
 *
 * The cookie is HttpOnly, so no script can read the control plane token;
 * SameSite=Lax, so a cross-site request carries no session at all; Secure in
 * production; and its lifetime is the token's own, so the browser stops
 * sending a credential the control plane would reject anyway.
 *
 * writeSession 把一个会话写入响应。
 *
 * 该 cookie 是 HttpOnly 的，因此没有脚本能读到控制面令牌；SameSite=Lax，因此跨站请求
 * 根本不携带会话；生产环境为 Secure；它的存活时长就是令牌自身的时长，因此浏览器不会
 * 继续发送一个控制面反正会拒绝的凭据。
 */
export async function writeSession(payload: SessionPayload): Promise<void> {
  const config = serverConfig();
  (await cookies()).set({
    name: SESSION_COOKIE,
    value: seal(payload, config.sessionKey),
    httpOnly: true,
    sameSite: "lax",
    secure: config.cookieSecure,
    path: "/",
    expires: new Date(payload.expiresAt),
  });
}

/**
 * clearSession removes the session cookie.
 *
 * Callers invoke the ControlPlane revocation endpoint before reaching this
 * helper, so deleting the cookie completes an already revoked session.
 *
 * clearSession 移除会话 cookie。
 *
 * 调用方会先调用 ControlPlane 吊销端点再抵达本辅助函数，因此删除 Cookie 是在完成
 * 一个已经吊销的会话。
 */
export async function clearSession(): Promise<void> {
  (await cookies()).delete(SESSION_COOKIE);
}

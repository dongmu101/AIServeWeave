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
 * This ends the Console's session and nothing else: the control plane token it
 * held stays valid until it expires, because this service has no endpoint to
 * revoke one. The UI must not claim otherwise.
 *
 * clearSession 移除会话 cookie。
 *
 * 它结束的只是 Console 的会话：其中持有的控制面令牌在过期之前依然有效，因为本服务没有
 * 可用来吊销它的端点。界面上不得作相反的表述。
 */
export async function clearSession(): Promise<void> {
  (await cookies()).delete(SESSION_COOKIE);
}

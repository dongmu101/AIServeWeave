import "server-only";
import { cookies } from "next/headers";
import { OPERATOR_COOKIE, openOperator, sealOperator, type OperatorSession } from "@/lib/console/operator-session";
import { serverConfig } from "@/lib/server/config";

/** readOperatorSession reads only the platform cookie. / readOperatorSession 仅读取平台 Cookie。 */
export async function readOperatorSession(now: Date = new Date()): Promise<OperatorSession | null> {
  const raw = (await cookies()).get(OPERATOR_COOKIE)?.value;
  return raw ? openOperator(raw, serverConfig().sessionKey, now) : null;
}

/** writeOperatorSession sets the independent HttpOnly platform cookie. / writeOperatorSession 设置独立的 HttpOnly 平台 Cookie。 */
export async function writeOperatorSession(session: OperatorSession): Promise<void> {
  const config = serverConfig();
  (await cookies()).set({ name: OPERATOR_COOKIE, value: sealOperator(session, config.sessionKey), httpOnly: true,
    sameSite: "lax", secure: config.cookieSecure, path: "/", expires: new Date(session.expiresAt) });
}

/** clearOperatorSession ends only the Console platform session, not its upstream JWT. / clearOperatorSession 仅结束 Console 平台会话，不吊销上游 JWT。 */
export async function clearOperatorSession(): Promise<void> {
  (await cookies()).delete(OPERATOR_COOKIE);
}

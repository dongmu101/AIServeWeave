import { NextResponse } from "next/server";

import { loginPath } from "@/lib/studio/internal-path";
import { clearSession } from "@/lib/server/session";

/**
 * GET clears a cookie the Gateway has already refused and sends the user to
 * sign in again. It revokes nothing — the session is already dead upstream —
 * so a cross-site link to it can at worst sign a user out of a dead session.
 *
 * GET 清除一个已被 Gateway 拒绝的 Cookie，并把用户送去重新登录。它不吊销任何东西——
 * 上游会话已经失效——因此跨站链接到这里，最坏也只是让用户退出一个已失效的会话。
 */
export async function GET(request: Request): Promise<Response> {
  await clearSession();
  const next = new URL(request.url).searchParams.get("next");
  const response = NextResponse.redirect(new URL(loginPath(next), request.url));
  response.headers.set("Cache-Control", "no-store");
  return response;
}

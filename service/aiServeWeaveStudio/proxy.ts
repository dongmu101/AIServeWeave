import { NextResponse, type NextRequest } from "next/server";

import { loginPath } from "@/lib/studio/internal-path";
import { SESSION_COOKIE } from "@/lib/studio/session-payload";

/**
 * proxy sends a visitor without a session cookie to the sign-in page. It only
 * checks presence: whether the cookie opens, and whether the session behind it
 * is live, is decided by the page itself and finally by the Gateway.
 *
 * proxy 把没有会话 Cookie 的访客送去登录页。它只检查是否存在：Cookie 能否打开、背后的
 * 会话是否有效，由页面本身、最终由 Gateway 判定。
 */
export function proxy(request: NextRequest): NextResponse {
  if (request.cookies.has(SESSION_COOKIE)) return NextResponse.next();
  const { pathname, search } = request.nextUrl;
  return NextResponse.redirect(new URL(loginPath(`${pathname}${search}`), request.url));
}

export const config = {
  matcher: ["/((?!login|api|_next|favicon.ico).*)"],
};

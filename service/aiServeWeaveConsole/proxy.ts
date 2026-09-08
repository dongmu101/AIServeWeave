import { NextResponse, type NextRequest } from "next/server";

import { OPERATOR_COOKIE } from "@/lib/console/operator-session";
import { loginPath } from "@/lib/console/internal-path";
import { SESSION_COOKIE } from "@/lib/console/session-payload";

/**
 * This is an optimistic redirect, not an authorization check.
 *
 * It only asks whether a session cookie is present, so a visitor without one
 * lands on sign-in with the address they wanted preserved, instead of watching
 * a console shell render and then bounce. Whether that cookie is genuine, and
 * whether the session behind it may do anything, is decided twice further in:
 * by the console layout, which opens the sealed cookie with a key only the
 * server has, and by the control plane, which is the only side that authorizes
 * anything. Next.js documents this split, and the reason is that this file
 * runs before the request reaches the application.
 *
 * 这是一次乐观的跳转，不是授权检查。
 *
 * 它只问会话 cookie 在不在，好让没有它的访客直接落在登录页、并保留原本想去的地址，
 * 而不是眼看着控制台外壳渲染出来又被弹走。那个 cookie 是否属实、它背后的会话能做什么，
 * 由更靠里的两处决定：控制台布局用一把只有服务端持有的密钥打开密封的 cookie，以及
 * 控制面——唯一进行授权的一侧。Next.js 文档就是这么划分的，理由是本文件运行在请求
 * 抵达应用之前。
 */
export function proxy(request: NextRequest): NextResponse {
  if (request.nextUrl.pathname.startsWith("/operator")) {
    if (request.nextUrl.pathname === "/operator/login") return NextResponse.next();
    const target = request.nextUrl.pathname + request.nextUrl.search;
    if (!request.cookies.has(OPERATOR_COOKIE)) {
      return NextResponse.redirect(new URL(`/operator/login?next=${encodeURIComponent(target)}`, request.url));
    }
    // Overwrite the routing hint; the layout validates it before use.
    // 覆盖路由提示，布局使用前仍会校验。
    const headers = new Headers(request.headers);
    headers.set("x-aisw-operator-return", target);
    return NextResponse.next({ request: { headers } });
  }
  if (request.cookies.has(SESSION_COOKIE)) {
    return NextResponse.next();
  }
  const { pathname, search } = request.nextUrl;
  return NextResponse.redirect(
    new URL(loginPath(`${pathname}${search}`), request.url)
  );
}

/**
 * config limits this file to the console pages. The API routes are excluded on
 * purpose: they answer 401 with a body a `fetch` caller can act on, and
 * redirecting them to an HTML sign-in page would turn an expired session into
 * a parse error instead.
 *
 * config 把本文件限定在控制台页面上。API 路由被刻意排除：它们以 401 和一个 `fetch`
 * 调用方能够处置的响应体作答，而把它们跳转到 HTML 登录页，只会把一次会话过期变成
 * 一次解析错误。
 */
export const config = {
  matcher: ["/console/:path*", "/operator/:path*"],
};

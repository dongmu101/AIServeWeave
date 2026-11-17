"use client";

/**
 * sendToSignIn leaves the app through /api/session/expired, which clears the
 * dead cookie and redirects to sign-in. It is a full navigation because the
 * target is a route handler, not a page.
 *
 * sendToSignIn 经 /api/session/expired 离开应用：清除失效的 Cookie 并重定向到登录页。
 * 目标是路由处理器而不是页面，因此做整页跳转。
 */
export function sendToSignIn(returnTo: string) {
  const target = new URL("/api/session/expired", window.location.origin);
  target.searchParams.set("next", returnTo);
  window.location.assign(target.toString());
}

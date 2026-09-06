/**
 * DEFAULT_LANDING is where a sign-in with no usable return address goes.
 *
 * DEFAULT_LANDING 是一次没有可用返回地址的登录最终落到的位置。
 */
export const DEFAULT_LANDING = "/console";

/**
 * internalPath sanitizes a return address before it is used to navigate.
 *
 * The value comes from a query string, which anybody can write, and it is
 * about to become the destination of a redirect performed on an authenticated
 * session. Everything that is not plainly a path inside this Console falls
 * back to the landing page: an absolute URL, a protocol-relative "//host" that
 * a reader mistakes for a path, a backslash form some browsers normalize into
 * one, and the sign-in page itself, which would loop.
 *
 * internalPath 在一个返回地址被用于跳转之前对它做净化。
 *
 * 这个值来自查询串，谁都能写，而它即将成为一次在已认证会话上执行的跳转的目的地。凡是
 * 不明确属于本 Console 内部路径的，一律回退到落地页：绝对 URL、会被读者误认作路径的
 * 协议相对形式 "//host"、某些浏览器会归一化成它的反斜杠写法，以及登录页自身——那会
 * 形成循环。
 */
export function internalPath(value: string | null | undefined): string {
  if (!value || !value.startsWith("/")) {
    return DEFAULT_LANDING;
  }
  if (value.startsWith("//") || value.startsWith("/\\")) {
    return DEFAULT_LANDING;
  }
  if (value === "/login" || value.startsWith("/login?")) {
    return DEFAULT_LANDING;
  }
  return value;
}

/**
 * loginPath builds the sign-in address that returns to where the caller was.
 *
 * loginPath 构造一个能返回调用方原处的登录地址。
 */
export function loginPath(returnTo?: string | null): string {
  const target = internalPath(returnTo);
  if (target === DEFAULT_LANDING) {
    return "/login";
  }
  return `/login?next=${encodeURIComponent(target)}`;
}

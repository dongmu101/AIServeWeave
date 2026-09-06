/**
 * RequestOrigin is the part of a request this check looks at.
 *
 * RequestOrigin 是本项检查所审视的那部分请求信息。
 */
export interface RequestOrigin {
  method: string;
  /** origin is the `Origin` header, absent as null.
   *
   * origin 是 `Origin` 头，缺席时为 null。 */
  origin: string | null;
  /** host is the `Host` header the request arrived with.
   *
   * host 是请求到达时携带的 `Host` 头。 */
  host: string | null;
  /** secFetchSite is the `Sec-Fetch-Site` header, absent as null.
   *
   * secFetchSite 是 `Sec-Fetch-Site` 头，缺席时为 null。 */
  secFetchSite: string | null;
}

/**
 * isSafeMethod reports whether a method only reads. These are exempt from the
 * origin check: they change nothing, and requiring an Origin header on them
 * would break ordinary navigation.
 *
 * isSafeMethod 报告一个方法是否只读。这些方法免于来源检查：它们不改变任何东西，而对
 * 它们强制要求 Origin 头会破坏普通的页面导航。
 */
export function isSafeMethod(method: string): boolean {
  const normalized = method.toUpperCase();
  return normalized === "GET" || normalized === "HEAD" || normalized === "OPTIONS";
}

/**
 * isTrustedWrite reports whether a state-changing request may proceed.
 *
 * This is the Console's CSRF defense, and it is a same-origin check rather
 * than a token: the session cookie is SameSite=Lax, so a cross-site form post
 * carries no session at all, and what remains to guard against is a
 * script-initiated cross-origin request — which the browser always labels with
 * an Origin header it will not let the page forge. A write with no Origin at
 * all is refused rather than allowed, because "no header" is exactly what a
 * client that is not a browser looks like, and every legitimate Console write
 * comes from a page.
 *
 * Deployments must let the reverse proxy pass the browser's Host through
 * unchanged; a proxy that rewrites it makes every write look cross-origin.
 *
 * isTrustedWrite 报告一次改变状态的请求是否可以继续。
 *
 * 这是 Console 的 CSRF 防护，采用同源检查而不是令牌：会话 cookie 是 SameSite=Lax，
 * 因此跨站表单提交根本不会带上会话，剩下要防的是脚本发起的跨源请求——而浏览器总会给
 * 它打上一个页面无法伪造的 Origin 头。完全没有 Origin 的写请求会被拒绝而不是放行，
 * 因为「没有这个头」正是非浏览器客户端的样子，而 Console 每一次正当的写操作都来自页面。
 *
 * 部署时必须让反向代理原样透传浏览器的 Host；改写它的代理会让每一次写操作都看起来跨源。
 */
export function isTrustedWrite(request: RequestOrigin): boolean {
  if (isSafeMethod(request.method)) {
    return true;
  }
  // A browser that sends this header has already made the judgement, and its
  // answer is stricter than ours: honor it when present.
  //
  // 会发送这个头的浏览器已经做出了判断，而它的结论比我们的更严格：存在时就采信它。
  if (request.secFetchSite !== null && request.secFetchSite !== "same-origin") {
    return false;
  }
  if (request.origin === null || request.host === null) {
    return false;
  }
  let originHost: string;
  try {
    originHost = new URL(request.origin).host;
  } catch {
    return false;
  }
  return originHost !== "" && originHost === request.host;
}

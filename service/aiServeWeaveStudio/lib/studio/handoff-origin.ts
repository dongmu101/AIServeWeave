/**
 * handoffOriginAllowed decides whether a ticket post may sign anyone in. The
 * ticket proves the Console issued it, but not that this browser's user asked
 * for it: a page elsewhere could post its own account's ticket and sign the
 * visitor into the wrong account. So the post must come from a configured
 * Console origin, or, when none is configured, from the same site.
 *
 * handoffOriginAllowed 决定一次票据提交能否让人登录。票据证明它出自 Console，却证明不了
 * 是这个浏览器的使用者要的：别处的页面可以提交它自己账户的票据，把访客登进错误的账户。
 * 因此提交必须来自配置的 Console 来源；未配置时，须来自同一站点。
 */
export function handoffOriginAllowed(input: {
  origin: string | null;
  secFetchSite: string | null;
  consoleOrigins: string[];
}): boolean {
  if (input.consoleOrigins.length > 0) {
    return input.origin !== null && input.consoleOrigins.includes(input.origin);
  }
  return input.secFetchSite === "same-site" || input.secFetchSite === "same-origin";
}

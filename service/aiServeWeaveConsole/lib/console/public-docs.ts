/**
 * The API reference is public: a visitor reads it before signing in. This
 * module names what that covers — the reference pages and the two read-only
 * Admin API calls behind them — so the proxy, the console layout and the API
 * entry point agree on one definition. The control plane serves those calls
 * without a session and returns only published pages.
 *
 * API 参考文档是公开的：访客登录前即可阅读。本模块界定其覆盖范围——参考文档页面及其背后的
 * 两个只读 Admin API 调用——使 proxy、控制台布局与 API 入口共用同一个定义。控制面无需会话
 * 即可响应这两个调用，且只返回已发布页面。
 */

/** PUBLIC_DOCS_HEADER is set by the proxy on reference pages, and stripped
 * everywhere else. / PUBLIC_DOCS_HEADER 由 proxy 在参考文档页面上设置，其他路径一律移除。 */
export const PUBLIC_DOCS_HEADER = "x-aisw-public-docs";

/** PUBLIC_DOCS_ROOT is the reference's landing page. / PUBLIC_DOCS_ROOT 是参考文档首页。 */
export const PUBLIC_DOCS_ROOT = "/console/docs";

/** isPublicDocsPage reports whether a page path is part of the reference.
 * isPublicDocsPage 判断一个页面路径是否属于参考文档。 */
export function isPublicDocsPage(pathname: string): boolean {
  return pathname === PUBLIC_DOCS_ROOT || pathname.startsWith(`${PUBLIC_DOCS_ROOT}/`);
}

/** isPublicDocsRead reports whether a resolved upstream call may go out
 * without a session. / isPublicDocsRead 判断一次已求解的上游调用能否不带会话发出。 */
export function isPublicDocsRead(method: string, upstreamPath: string): boolean {
  return method === "GET" && /^\/admin\/v1\/api-docs(\/[^/]+)?$/.test(upstreamPath);
}

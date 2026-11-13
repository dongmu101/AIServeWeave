import { redirect } from "next/navigation";

/**
 * Request search is now a tab of the audit page; this route keeps old links
 * working. Filters are not carried over: the tab starts unfiltered.
 *
 * 请求检索现在是审计页的一个 Tab；保留此路由让旧链接继续可用。筛选条件不带过去，
 * Tab 以无筛选状态打开。
 */
export default function RequestsPage() {
  redirect("/console/audit?tab=requests");
}

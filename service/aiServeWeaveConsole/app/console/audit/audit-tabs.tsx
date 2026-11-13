"use client";

import { useSearchParams } from "next/navigation";

import { AuditView } from "@/app/console/audit/audit-view";
import { RequestLogView } from "@/app/console/requests/request-log-view";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Message } from "@/components/i18n/message";

type AuditTab = "audit" | "requests";

/**
 * AuditTabs puts the audit trail and request search on one page. The open tab
 * lives in `?tab=`; switching drops the other tab's filters, because both
 * views read `since`/`until`/`size` from the same query and would otherwise
 * inherit each other's. Only the open tab is mounted, so only it fetches.
 *
 * AuditTabs 把审计与请求检索放在同一页。当前 Tab 保存在 `?tab=`；切换时清掉另一个
 * Tab 的筛选条件，因为两个视图从同一组查询参数读取 `since`/`until`/`size`，否则会
 * 互相继承。只有当前 Tab 挂载，因此只有它发起请求。
 */
export function AuditTabs() {
  const searchParams = useSearchParams();
  const tab: AuditTab = searchParams.get("tab") === "requests" ? "requests" : "audit";
  const select = (next: unknown) => {
    if (next !== "audit" && next !== "requests") return;
    window.history.replaceState(null, "", next === "audit" ? window.location.pathname : "?tab=requests");
  };
  return (
    <Tabs value={tab} onValueChange={select}>
      <TabsList className="w-fit">
        <TabsTrigger value="audit" className="flex-none px-3"><Message id="tenant.audit" /></TabsTrigger>
        <TabsTrigger value="requests" className="flex-none px-3"><Message id="tenant.requestSearch" /></TabsTrigger>
      </TabsList>
      <TabsContent value="audit" className="mt-2"><AuditView /></TabsContent>
      <TabsContent value="requests" className="mt-2"><RequestLogView /></TabsContent>
    </Tabs>
  );
}

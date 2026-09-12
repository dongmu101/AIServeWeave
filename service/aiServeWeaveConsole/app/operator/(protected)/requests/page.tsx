import { Suspense } from "react";
import { RequestLogView } from "@/app/console/requests/request-log-view";
import { LoadingState } from "@/components/console/states";

/** RequestsPage reads only the platform cross-tenant request search surface.
 * RequestsPage 仅读取平台跨租户请求检索入口。 */
export default function RequestsPage() {
  return <Suspense fallback={<LoadingState label="正在读取请求记录" />}><RequestLogView surface="operator" /></Suspense>;
}

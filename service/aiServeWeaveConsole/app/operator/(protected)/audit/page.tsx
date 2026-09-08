import { Suspense } from "react";
import { AuditView } from "@/app/console/audit/audit-view";
import { LoadingState } from "@/components/console/states";

/** AuditPage reads only the platform audit surface.
 * AuditPage 仅读取平台审计入口。 */
export default function AuditPage() {
  return <Suspense fallback={<LoadingState label="正在读取平台审计" />}><AuditView surface="operator" /></Suspense>;
}

import { Suspense } from "react";
import { AlertsView } from "@/app/operator/alerts/alerts-view";
import { LoadingState } from "@/components/console/states";

/** AlertsPage lists fired alert instances; it exists only under /operator,
 * there is no tenant-facing equivalent.
 *
 * AlertsPage 列出已触发的告警实例；它只存在于 /operator 下，没有租户侧
 * 对应页面。 */
export default function AlertsPage() {
  return (
    <Suspense fallback={<LoadingState label="正在加载告警" />}>
      <AlertsView />
    </Suspense>
  );
}

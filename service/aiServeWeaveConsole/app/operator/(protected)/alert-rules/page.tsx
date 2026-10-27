import { Message } from "@/components/i18n/message";
import { Suspense } from "react";
import { AlertRulesView } from "@/app/operator/alert-rules/alert-rules-view";
import { LoadingState } from "@/components/console/states";

/** AlertRulesPage manages alert rules; it exists only under /operator, there
 * is no tenant-facing equivalent.
 *
 * AlertRulesPage 管理告警规则；它只存在于 /operator 下，没有租户侧对应页面。 */
export default function AlertRulesPage() {
  return (
    <Suspense fallback={<LoadingState label={<Message id="operator.loadingAlertRules" />} />}>
      <AlertRulesView />
    </Suspense>
  );
}

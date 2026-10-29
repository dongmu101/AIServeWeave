import { BillingAdminView } from "./billing-view";

/** BillingPage is the platform's prepaid billing administration: packages,
 * model prices, tenant modes and balances, and the cross-tenant order and
 * ledger history. Payments themselves stay with the control plane, which
 * alone holds the payment provider's credentials.
 *
 * BillingPage 是平台的预充值计费管理：充值包、模型单价、租户计费模式与余额，以及跨
 * 租户的订单与账目历史。支付本身留在控制面，只有它持有支付服务商的凭据。 */
export default function BillingPage() {
  return <BillingAdminView />;
}

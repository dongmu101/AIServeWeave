import { BillingAdminView } from "./billing-view";

/** BillingPage is the platform's prepaid billing administration: packages,
 * token pricing (model pricing, catalog review, price plans), tenant modes,
 * plans and balances, and the cross-tenant order and
 * ledger history. Payments themselves stay with the control plane, which
 * alone holds the payment provider's credentials.
 *
 * BillingPage 是平台的预充值计费管理：充值包、token 计价（模型定价、价目表审核、价格
 * 方案）、租户计费模式、方案与余额，以及跨
 * 租户的订单与账目历史。支付本身留在控制面，只有它持有支付服务商的凭据。 */
export default function BillingPage() {
  return <BillingAdminView />;
}

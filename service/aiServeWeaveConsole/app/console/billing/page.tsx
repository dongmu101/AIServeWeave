import { redirect } from "next/navigation";

import { BillingView } from "@/app/console/billing/billing-view";
import { isPaymentReturn } from "@/lib/console/billing";
import { readSession } from "@/lib/server/session";

/**
 * The billing page: the tenant's prepaid credit balance, the packages it can
 * buy, and its ledger and orders. Every role may read it; only owner and
 * admin may buy or open the payment portal, which the control plane enforces
 * and the role read from the session here merely reflects.
 *
 * The payment provider sends the buyer back here with `status` and
 * `payment_id` in the query. Their presence only tells the view to watch for
 * the server's confirmation for a short while; their values are never read
 * as the outcome.
 *
 * 计费页：本租户的预充值点数余额、可购买的充值包，以及账目与订单。任何角色都可以
 * 查看；只有 owner 与 admin 可以购买或打开支付门户——这由控制面强制执行，这里从会话
 * 读取的角色只是如实反映。
 *
 * 支付服务商会带着查询参数 `status` 与 `payment_id` 把购买者送回这里。它们的出现只
 * 告诉视图在短时间内等待服务端确认；它们的取值从不被当作支付结果。
 */
export default async function BillingPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <BillingView role={session.user.role} returning={isPaymentReturn(await searchParams)} />;
}

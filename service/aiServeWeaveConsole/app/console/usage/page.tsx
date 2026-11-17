import { redirect } from "next/navigation";

import { UsageView } from "@/app/console/usage/usage-view";
import { readUsageLink } from "@/lib/console/usage-records";
import { readSession } from "@/lib/server/session";

/**
 * The usage page: this tenant's token consumption, drawn from the persisted
 * usage ledger (STATUS.md's usage_records table, P2): totals, by model, user
 * and key, and every request one by one. With billing on it also shows the
 * credits each request used, as charged when it was ingested; prices
 * themselves are on the billing page. The query may name a tab and a time
 * window, which is how a usage ledger entry links to its requests.
 *
 * 用量页：本租户的 token 消耗，来自持久化用量账本（STATUS.md 的 usage_records 表，
 * P2）：合计、按模型 / 用户 / Key，以及逐笔请求。开启计费时另显示每个请求入账时扣除的
 * 点数；单价本身在计费页。查询参数可以指定标签页与时间窗口，用量账目就是这样链接到
 * 它所涵盖的请求的。
 */
export default async function UsagePage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <UsageView link={readUsageLink(await searchParams)} />;
}

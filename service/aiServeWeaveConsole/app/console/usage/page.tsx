import { redirect } from "next/navigation";

import { UsageView } from "@/app/console/usage/usage-view";
import { readSession } from "@/lib/server/session";

/**
 * The usage page: per-model token consumption for this tenant, drawn from the
 * persisted usage ledger (STATUS.md's usage_records table, P2). Pricing and
 * invoicing are deliberately out of scope here, same as on the backend — this
 * answers "how much did we use", not "what do we owe".
 *
 * 用量页：本租户按模型统计的 token 消耗，来自持久化用量账本
 * (STATUS.md 的 usage_records 表，P2)。定价与开票在这里同样刻意不在范围内——
 * 这个页面回答的是「用了多少」，不是「欠多少钱」。
 */
export default async function UsagePage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <UsageView />;
}

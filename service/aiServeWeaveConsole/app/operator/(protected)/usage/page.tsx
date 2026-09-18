import { UsageView } from "./usage-view";

/** UsagePage displays per-tenant, per-model token consumption for platform
 * operators, drawn from the persisted usage ledger (STATUS.md's
 * usage_records table, P2). Pricing and invoicing remain out of scope.
 *
 * UsagePage 向平台运维展示按租户、按模型统计的 token 消耗，来自持久化用量账本
 * (STATUS.md 的 usage_records 表，P2)。定价与开票仍不在范围内。 */
export default function UsagePage() {
  return <UsageView />;
}

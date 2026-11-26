import { FlowList } from "@/components/studio/flows/flow-list";
import { errorMessage } from "@/lib/studio/contract";
import { loadFlows, requireSession } from "@/lib/server/studio";

/**
 * The user's flows. / 用户的编排列表。
 */
export default async function FlowsPage() {
  const session = await requireSession("/flows");
  const loaded = await loadFlows(session, "/flows");
  return <FlowList flows={loaded.kind === "ok" ? loaded.value.flows : []} error={loaded.kind === "error" ? errorMessage(loaded.code) : ""} />;
}

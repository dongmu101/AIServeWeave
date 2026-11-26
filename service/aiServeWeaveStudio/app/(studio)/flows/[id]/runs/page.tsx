import { notFound } from "next/navigation";

import { RunHistory } from "@/components/studio/flows/run-history";
import { errorMessage } from "@/lib/studio/contract";
import { loadFlow, loadRuns, requireSession } from "@/lib/server/studio";

/**
 * A flow's run history. / 编排的运行记录。
 */
export default async function RunsPage({ params }: PageProps<"/flows/[id]/runs">) {
  const { id } = await params;
  const returnTo = `/flows/${id}/runs`;
  const session = await requireSession(returnTo);
  const [flow, runs] = await Promise.all([loadFlow(session, id, returnTo), loadRuns(session, id, returnTo)]);
  if (flow.kind === "not_found" || runs.kind === "not_found") notFound();
  return (
    <RunHistory
      flowId={id}
      flowName={flow.kind === "ok" ? flow.value.name : "编排"}
      runs={runs.kind === "ok" ? runs.value.runs : []}
      next={runs.kind === "ok" ? runs.value.next : ""}
      error={runs.kind === "error" ? errorMessage(runs.code) : flow.kind === "error" ? errorMessage(flow.code) : ""}
    />
  );
}

import { notFound } from "next/navigation";

import { RunView } from "@/components/studio/flows/run-view";
import { errorMessage } from "@/lib/studio/contract";
import { loadFlow, loadFlowVersion, loadRun, requireSession } from "@/lib/server/studio";

/**
 * One run, shown on the definition of the version it ran.
 * 单次运行，展示在它所运行版本的定义上。
 */
export default async function RunPage({ params }: PageProps<"/flows/[id]/runs/[runId]">) {
  const { id, runId } = await params;
  const returnTo = `/flows/${id}/runs/${runId}`;
  const session = await requireSession(returnTo);
  const [run, flow] = await Promise.all([loadRun(session, runId, returnTo), loadFlow(session, id, returnTo)]);
  if (run.kind === "not_found" || flow.kind === "not_found" || (run.kind === "ok" && run.value.flowId !== id)) notFound();
  if (run.kind === "error" || flow.kind === "error") {
    const code = run.kind === "error" ? run.code : flow.kind === "error" ? flow.code : "upstream";
    return <div className="flex flex-1 items-center justify-center px-6 text-sm text-muted-foreground">运行加载失败：{errorMessage(code)}</div>;
  }
  const version = await loadFlowVersion(session, id, run.value.flowVersion, returnTo);
  const definition = version.kind === "ok" ? version.value.definition : flow.value.definition;
  return <RunView key={run.value.id} flowId={id} flowName={flow.value.name} run={run.value} definition={definition} />;
}

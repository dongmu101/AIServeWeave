import { notFound } from "next/navigation";

import { FlowEditor } from "@/components/studio/flows/flow-editor";
import { errorMessage } from "@/lib/studio/contract";
import { loadFlow, loadModels, requireSession } from "@/lib/server/studio";

/**
 * The editor of one flow, with the models the user may pick from.
 * 单个编排的编辑器，附带用户可选的模型。
 */
export default async function FlowPage({ params }: PageProps<"/flows/[id]">) {
  const { id } = await params;
  const returnTo = `/flows/${id}`;
  const session = await requireSession(returnTo);
  const [loaded, models] = await Promise.all([loadFlow(session, id, returnTo), loadModels(session, returnTo)]);
  if (loaded.kind === "not_found") notFound();
  if (loaded.kind === "error") {
    return <div className="flex flex-1 items-center justify-center px-6 text-sm text-muted-foreground">编排加载失败：{errorMessage(loaded.code)}</div>;
  }
  return (
    <FlowEditor
      key={`${loaded.value.id}:${loaded.value.version}`}
      flow={loaded.value}
      models={models.kind === "ok" ? models.models : []}
      modelsError={models.kind === "error" ? errorMessage(models.code) : ""}
    />
  );
}

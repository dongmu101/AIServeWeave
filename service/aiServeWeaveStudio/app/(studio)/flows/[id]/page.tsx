import { notFound } from "next/navigation";

import { FlowEditor } from "@/components/studio/flows/flow-editor";
import { errorMessage } from "@/lib/studio/contract";
import { loadFlow, loadImageModels, loadModels, loadVideoModels, loadWorkflowTemplates, requireSession } from "@/lib/server/studio";

/**
 * The editor of one flow, with what the user may pick from: chat models,
 * image models, video models and workflow templates. A catalog that fails to
 * load leaves the editor usable, with a note.
 * 单个编排的编辑器，附带用户可选的内容：对话模型、图像模型、视频模型与工作流模板。某个目录加载失败时
 * 编辑器仍可使用，并附提示。
 */
export default async function FlowPage({ params }: PageProps<"/flows/[id]">) {
  const { id } = await params;
  const returnTo = `/flows/${id}`;
  const session = await requireSession(returnTo);
  const [loaded, models, imageModels, templates, videoModels] = await Promise.all([
    loadFlow(session, id, returnTo),
    loadModels(session, returnTo),
    loadImageModels(session, returnTo),
    loadWorkflowTemplates(session, returnTo),
    loadVideoModels(session, returnTo),
  ]);
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
      imageModels={imageModels.kind === "ok" ? imageModels.value : null}
      templates={templates.kind === "ok" ? templates.value : null}
      videoModels={videoModels.kind === "ok" ? videoModels.value : null}
      catalogError={imageModels.kind === "ok" && templates.kind === "ok" && videoModels.kind === "ok" ? "" : "图像模型、视频模型或工作流模板列表加载失败，相关节点暂时无法检查可用性"}
    />
  );
}

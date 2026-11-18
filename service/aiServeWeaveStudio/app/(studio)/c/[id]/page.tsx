import { notFound } from "next/navigation";

import { ChatView } from "@/components/studio/chat-view";
import { errorMessage } from "@/lib/studio/contract";
import { toUIMessages } from "@/lib/studio/conversations";
import { loadConversation, loadModels, requireSession } from "@/lib/server/studio";

/**
 * One conversation. The stored history is read on the server and handed to
 * the chat view as its initial messages.
 *
 * 单个对话。已存储的历史在服务端读取，作为初始消息交给对话视图。
 */
export default async function ConversationPage({ params }: PageProps<"/c/[id]">) {
  const { id } = await params;
  const returnTo = `/c/${id}`;
  const session = await requireSession(returnTo);
  const [loaded, models] = await Promise.all([loadConversation(session, id, returnTo), loadModels(session, returnTo)]);
  if (loaded.kind === "not_found") notFound();
  if (loaded.kind === "error") {
    return (
      <div className="flex flex-1 items-center justify-center px-6 text-sm text-muted-foreground">
        对话加载失败：{errorMessage(loaded.code)}
      </div>
    );
  }
  return (
    <ChatView
      key={loaded.conversation.id}
      conversation={loaded.conversation}
      initialMessages={toUIMessages(loaded.messages)}
      models={models.kind === "ok" ? models.models : []}
      modelsError={models.kind === "error" ? errorMessage(models.code) : ""}
    />
  );
}

import { StudioShell } from "@/components/studio/studio-shell";
import { errorMessage, greetingName } from "@/lib/studio/contract";
import { loadConversations, requireSession } from "@/lib/server/studio";

/**
 * The signed-in frame: sidebar plus main area. Only display fields of the
 * user cross to the client; the token stays on the server.
 *
 * 已登录的框架：侧边栏加主区域。只有用户的展示字段会传到客户端，令牌留在服务端。
 */
export default async function StudioLayout({ children }: LayoutProps<"/">) {
  const session = await requireSession("/");
  const conversations = await loadConversations(session, "/");
  return (
    <StudioShell
      user={{ displayName: greetingName(session.user), email: session.user.email, role: session.user.role }}
      conversations={
        conversations.kind === "ok"
          ? { items: conversations.page.conversations, error: "" }
          : { items: [], error: errorMessage(conversations.code) }
      }
    >
      {children}
    </StudioShell>
  );
}

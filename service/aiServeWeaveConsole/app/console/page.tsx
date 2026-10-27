import { redirect } from "next/navigation";
import { readSession } from "@/lib/server/session";
import { OverviewView } from "@/app/console/overview-view";

/** OverviewPage authenticates before exposing session facts to the view.
 * OverviewPage 在向视图提供会话事实之前完成身份校验。 */
export default async function OverviewPage() {
  const session = await readSession();
  if (!session) redirect("/login");
  return <OverviewView user={session.user} expiresAt={session.expiresAt} />;
}

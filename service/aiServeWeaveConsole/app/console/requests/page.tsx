import { redirect } from "next/navigation";

import { RequestLogView } from "@/app/console/requests/request-log-view";
import { readSession } from "@/lib/server/session";

/**
 * The request search page. Every signed-in tenant user may read their own
 * tenant's authenticated front-door requests (STATUS.md's P09/C28).
 *
 * 请求检索页。任何已登录的租户用户都可以读取自己租户已通过鉴权的前门请求
 * (STATUS.md 的 P09/C28)。
 */
export default async function RequestsPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <RequestLogView />;
}

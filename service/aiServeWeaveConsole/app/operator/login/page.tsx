import { redirect } from "next/navigation";

import { OperatorLoginForm } from "@/app/operator/login/login-form";
import { operatorTarget } from "@/lib/console/operator-session";
import { readOperatorSession } from "@/lib/server/operator-session";

/**
 * The sign-in page. The `next` parameter is sanitized here, on the server,
 * before it is handed to the form: it arrives from a query string anybody can
 * write, and it is about to become the destination of a navigation performed
 * with a fresh session.
 *
 * 登录页。`next` 参数在这里、在服务端被净化之后才交给表单：它来自谁都能写的查询串，
 * 而它即将成为一次以全新会话执行的跳转的目的地。
 */
export default async function LoginPage({
  searchParams,
}: {
  searchParams: Promise<{ next?: string | string[] }>;
}) {
  const requested = (await searchParams).next;
  const target = operatorTarget(Array.isArray(requested) ? requested[0] : requested);

  const session = await readOperatorSession();
  if (session) {
    redirect(target);
  }

  return (
    <main className="flex flex-1 items-center justify-center p-6">
      <div className="w-full max-w-sm">
        <div className="mb-6 space-y-1">
          <h1 className="font-heading text-xl font-semibold">
            AIServeWeave 平台运维
          </h1>
          <p className="text-sm text-muted-foreground">
            使用独立的平台运维账号登录；租户账号不授予运维权限。
          </p>
        </div>
        <OperatorLoginForm next={target} />
      </div>
    </main>
  );
}

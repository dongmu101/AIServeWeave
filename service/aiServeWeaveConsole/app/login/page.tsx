import { Message } from "@/components/i18n/message";
import { LanguageSwitcher } from "@/components/i18n/language-switcher";
import { redirect } from "next/navigation";

import { LoginForm } from "@/app/login/login-form";
import { SiteFooter } from "@/components/site/site-footer";
import { internalPath } from "@/lib/console/internal-path";
import { serverConfig } from "@/lib/server/config";
import { readSession } from "@/lib/server/session";

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
  const target = internalPath(Array.isArray(requested) ? requested[0] : requested);

  const session = await readSession();
  if (session) {
    redirect(target);
  }

  return (
    <>
    <main className="flex flex-1 items-center justify-center bg-muted/30 p-4 sm:p-6">
      <div className="w-full max-w-md rounded-2xl border bg-card p-6 shadow-sm sm:p-8">
        <div className="mb-6 flex justify-end"><LanguageSwitcher /></div>
        <div className="mb-8 space-y-2">
          <h1 className="font-heading text-2xl font-semibold tracking-tight">
            <Message id="common.consoleTitle" /></h1>
          <p className="text-sm text-muted-foreground">
            <Message id="auth.signInWithYourEmailAndPassword" /></p>
        </div>
        <LoginForm next={target} signupEnabled={serverConfig().signupEnabled} />
      </div>
    </main>
    <SiteFooter />
    </>
  );
}

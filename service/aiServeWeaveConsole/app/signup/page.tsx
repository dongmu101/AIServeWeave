import Link from "next/link";
import { notFound, redirect } from "next/navigation";

import { SignupForm } from "@/app/signup/signup-form";
import { DEFAULT_LANDING } from "@/lib/console/internal-path";
import { serverConfig } from "@/lib/server/config";
import { readSession } from "@/lib/server/session";

/**
 * The self-service sign-up page. It exists only when the deployment sets
 * AISW_CONSOLE_SIGNUP_ENABLED; otherwise it is a 404 like any unknown page.
 * A visitor who is already signed in is sent to the console instead.
 *
 * 自助注册页。只有部署设置了 AISW_CONSOLE_SIGNUP_ENABLED 时它才存在，否则与任何未知
 * 页面一样是 404。已登录的访客会被送往控制台。
 */
export default async function SignupPage() {
  if (!serverConfig().signupEnabled) {
    notFound();
  }
  const session = await readSession();
  if (session) {
    redirect(DEFAULT_LANDING);
  }

  return (
    <main className="flex flex-1 items-center justify-center p-6">
      <div className="w-full max-w-sm">
        <div className="mb-6 space-y-1">
          <h1 className="font-heading text-xl font-semibold">注册租户</h1>
          <p className="text-sm text-muted-foreground">
            创建一个新租户，你将成为它的 owner。注册后立即可用。
          </p>
        </div>
        <SignupForm />
        <p className="mt-4 text-xs text-muted-foreground">
          已有账号？
          <Link href="/login" className="underline underline-offset-4">
            登录
          </Link>
        </p>
      </div>
    </main>
  );
}

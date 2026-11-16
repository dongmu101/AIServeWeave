import { redirect } from "next/navigation";

import { LoginForm } from "@/app/login/login-form";
import { BrandMark } from "@/components/studio/brand";
import { internalPath } from "@/lib/studio/internal-path";
import { readSession } from "@/lib/server/session";

/**
 * The sign-in page. `next` is sanitised on the server before it becomes a
 * navigation target.
 *
 * 登录页。`next` 在成为跳转目标之前先在服务端净化。
 */
export default async function LoginPage({ searchParams }: { searchParams: Promise<{ next?: string | string[] }> }) {
  const requested = (await searchParams).next;
  const target = internalPath(Array.isArray(requested) ? requested[0] : requested);
  if (await readSession()) redirect(target);

  return (
    <main className="flex flex-1 items-center justify-center bg-muted/30 p-4 sm:p-6">
      <div className="w-full max-w-sm rounded-2xl border bg-card p-6 shadow-sm sm:p-8">
        <div className="mb-8 flex flex-col items-center gap-3 text-center">
          <BrandMark className="size-10" />
          <div className="space-y-1">
            <h1 className="font-heading text-xl font-semibold tracking-tight">登录 Studio</h1>
            <p className="text-sm text-muted-foreground">使用控制台的同一账号登录</p>
          </div>
        </div>
        <LoginForm next={target} />
      </div>
    </main>
  );
}

"use client";

import * as React from "react";
import { useRouter } from "next/navigation";

import { FormError } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { Input } from "@/components/ui/input";
import { operatorSignIn } from "@/lib/console/operator-client";
import { describe } from "@/lib/console/errors";

/**
 * OperatorLoginForm posts credentials to the Console's own session route, which is the
 * only endpoint that ever sees a password and the only one that receives a
 * control plane token. Nothing about the attempt is kept on this side: the
 * password lives in component state for the duration of one submission and is
 * cleared when it settles, whichever way it settled.
 *
 * OperatorLoginForm 把凭据提交给 Console 自己的 session 路由——那是唯一会看到密码的端点，也是
 * 唯一会收到控制面令牌的端点。这一侧不保留关于这次尝试的任何东西：密码在组件状态中
 * 只存活于一次提交期间，无论结果如何，尘埃落定即清除。
 */
export function OperatorLoginForm({ next }: { next: string }) {
  const router = useRouter();
  const [email, setEmail] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }
    setPending(true);
    setError(null);
    try {
      await operatorSignIn(email.trim(), password);
      setPassword("");
      // replace, not push: a signed-in person pressing Back should not land on
      // a sign-in form that will only redirect them forward again.
      //
      // 用 replace 而不是 push：已登录的人按返回键，不该落到一个只会把他们再送回去的
      // 登录表单上。
      router.replace(next);
      router.refresh();
    } catch (failure) {
      setPassword("");
      setError(describe(failure));
      setPending(false);
    }
  }

  return (
    <form onSubmit={submit} className="grid gap-4" noValidate>
      <div className="grid gap-2">
        <label htmlFor="email" className="text-sm font-medium">
          邮箱
        </label>
        <Input
          id="email"
          name="email"
          type="email"
          autoComplete="username"
          required
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          disabled={pending}
        />
      </div>
      <div className="grid gap-2">
        <label htmlFor="password" className="text-sm font-medium">
          密码
        </label>
        <Input
          id="password"
          name="password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          disabled={pending}
        />
      </div>
      <FormError message={error} />
      <SubmitButton pending={pending} size="lg">
        登录
      </SubmitButton>
      <p className="text-xs text-muted-foreground">
        忘记密码或尚未获得账号，请联系部署管理员。
      </p>
    </form>
  );
}

"use client";

import { Message } from "@/components/i18n/message";


import * as React from "react";
import { useRouter } from "next/navigation";

import { FormError } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { Input } from "@/components/ui/input";
import { signUp } from "@/lib/console/api-client";
import { describe } from "@/lib/console/errors";
import { DEFAULT_LANDING } from "@/lib/console/internal-path";
import {
  MAX_TENANT_NAME_LENGTH,
  MIN_SIGNUP_PASSWORD_LENGTH,
  localSignupProblem,
} from "@/lib/console/signup";

/**
 * SignupForm posts a new tenant and its owner to the Console's own sign-up
 * route, which signs the owner in on success. As on the sign-in form, the
 * passwords live in component state for one submission and are cleared when
 * it settles, whichever way it settled.
 *
 * SignupForm 把新租户及其 owner 提交给 Console 自己的注册路由，成功后该路由会为 owner
 * 登录。与登录表单一样，密码只在一次提交期间存活于组件状态中，无论结果如何，尘埃落定即
 * 清除。
 */
export function SignupForm() {
  const router = useRouter();
  const [tenantName, setTenantName] = React.useState("");
  const [email, setEmail] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [confirmPassword, setConfirmPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }
    const problem = localSignupProblem({
      tenantName,
      email,
      password,
      confirmPassword,
    });
    if (problem) {
      setError(problem);
      return;
    }
    setPending(true);
    setError(null);
    try {
      await signUp({
        tenantName: tenantName.trim(),
        email: email.trim(),
        password,
      });
      setPassword("");
      setConfirmPassword("");
      router.replace(DEFAULT_LANDING);
      router.refresh();
    } catch (failure) {
      setPassword("");
      setConfirmPassword("");
      setError(describe(failure));
      setPending(false);
    }
  }

  return (
    <form onSubmit={submit} className="grid gap-4" noValidate>
      <div className="grid gap-2">
        <label htmlFor="tenant-name" className="text-sm font-medium">
          <Message id="auth.tenantName" /></label>
        <Input
          id="tenant-name"
          name="tenant-name"
          autoComplete="organization"
          maxLength={MAX_TENANT_NAME_LENGTH}
          required
          value={tenantName}
          onChange={(event) => setTenantName(event.target.value)}
          disabled={pending}
        />
      </div>
      <div className="grid gap-2">
        <label htmlFor="email" className="text-sm font-medium">
          <Message id="tenant.email" /></label>
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
          <Message id="auth.password" /></label>
        <Input
          id="password"
          name="password"
          type="password"
          autoComplete="new-password"
          aria-describedby="password-hint"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          disabled={pending}
        />
        <p id="password-hint" className="text-xs text-muted-foreground"><Message id="auth.atLeastCharacters" values={{ MIN_SIGNUP_PASSWORD_LENGTH: MIN_SIGNUP_PASSWORD_LENGTH }} /></p>
      </div>
      <div className="grid gap-2">
        <label htmlFor="confirm-password" className="text-sm font-medium">
          <Message id="auth.confirmPassword" /></label>
        <Input
          id="confirm-password"
          name="confirm-password"
          type="password"
          autoComplete="new-password"
          value={confirmPassword}
          onChange={(event) => setConfirmPassword(event.target.value)}
          disabled={pending}
        />
      </div>
      <FormError message={error} />
      <SubmitButton pending={pending} size="lg">
        <Message id="auth.register" /></SubmitButton>
    </form>
  );
}

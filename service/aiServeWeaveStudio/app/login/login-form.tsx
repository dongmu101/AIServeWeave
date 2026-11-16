"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { Loader2Icon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { errorMessage } from "@/lib/studio/contract";

export function LoginForm({ next }: { next: string }) {
  const router = useRouter();
  const [email, setEmail] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState("");

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    setPending(true);
    setError("");
    try {
      const response = await fetch("/api/session", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: email.trim(), password }),
      });
      setPassword("");
      if (!response.ok) {
        const body = (await response.json().catch(() => ({}))) as { error?: string };
        setError(errorMessage(body.error ?? ""));
        setPending(false);
        return;
      }
      router.replace(next);
      router.refresh();
    } catch {
      setPassword("");
      setError("网络连接失败，请检查网络后重试。");
      setPending(false);
    }
  }

  return (
    <form onSubmit={submit} className="grid gap-4" noValidate>
      <div className="grid gap-2">
        <label htmlFor="email" className="text-sm font-medium">邮箱</label>
        <Input id="email" type="email" autoComplete="username" required value={email}
          onChange={(event) => setEmail(event.target.value)} disabled={pending} className="h-10 px-3" />
      </div>
      <div className="grid gap-2">
        <label htmlFor="password" className="text-sm font-medium">密码</label>
        <Input id="password" type="password" autoComplete="current-password" value={password}
          onChange={(event) => setPassword(event.target.value)} disabled={pending} className="h-10 px-3" />
      </div>
      {error ? <p role="alert" className="text-sm text-destructive">{error}</p> : null}
      <Button type="submit" size="lg" className="h-10 w-full" disabled={pending || email.trim() === ""}>
        {pending ? <Loader2Icon className="animate-spin" /> : null}
        登录
      </Button>
    </form>
  );
}

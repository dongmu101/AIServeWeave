"use client";

import type * as React from "react";
import { Loader2Icon } from "lucide-react";

import { Button } from "@/components/ui/button";

/**
 * SubmitButton is the only submit control Console forms use, because
 * double-submission is a correctness problem here rather than a cosmetic one:
 * two clicks on "create key" mint two credentials, and the second one is a
 * live secret nobody meant to exist. Disabling while a request is in flight is
 * the guard, and putting it in one component means no form can forget it.
 *
 * SubmitButton 是 Console 表单唯一使用的提交控件，因为重复提交在这里是正确性问题而不是
 * 观感问题：在「创建 key」上点两下就会铸出两个凭据，而第二个是一份没人打算让它存在的
 * 活密钥。请求在途期间禁用就是这道防线，把它收进一个组件，就没有哪个表单会忘记它。
 */
export function SubmitButton({
  pending,
  children,
  ...props
}: React.ComponentProps<typeof Button> & { pending: boolean }) {
  return (
    <Button type="submit" disabled={pending} aria-busy={pending} {...props}>
      {pending ? <Loader2Icon className="animate-spin" /> : null}
      {children}
    </Button>
  );
}

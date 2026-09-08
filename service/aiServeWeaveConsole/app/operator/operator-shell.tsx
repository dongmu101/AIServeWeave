"use client";

import * as React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { FormError } from "@/components/console/states";
import { operatorSignOut } from "@/lib/console/operator-client";
import { describe } from "@/lib/console/errors";
import type { OperatorIdentity } from "@/lib/console/operator-session";

const navigation = [
  { href: "/operator/fleet", label: "机群与审批" },
  { href: "/operator/models", label: "模型" },
  { href: "/operator/workflows", label: "发布状态" },
  { href: "/operator/audit", label: "运维审计" },
];

/** OperatorShell renders only platform identity and navigation. / OperatorShell 仅渲染平台身份与导航。 */
export function OperatorShell({ operator, children }: { operator: OperatorIdentity; children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  async function leave() {
    setPending(true);
    setError(null);
    try {
      await operatorSignOut();
      router.replace("/operator/login");
      router.refresh();
    } catch (failure) { setError(describe(failure)); setPending(false); }
  }
  return <div className="flex min-h-full flex-1 flex-col">
    <header className="border-b"><div className="mx-auto flex max-w-6xl flex-wrap items-center gap-3 px-4 py-3">
      <span className="font-heading font-semibold">AIServeWeave 平台运维</span>
      <nav aria-label="平台运维导航" className="flex flex-wrap gap-1">{navigation.map(item => <Button key={item.href} variant={pathname === item.href ? "secondary" : "ghost"} size="sm" render={<Link href={item.href} />} aria-current={pathname === item.href ? "page" : undefined}>{item.label}</Button>)}</nav>
      <Link href="/console" className="text-sm underline">租户控制台</Link>
      <span className="ml-auto max-w-56 break-all text-sm">{operator.name || operator.email}</span>
      <Button variant="outline" size="sm" onClick={leave} disabled={pending} aria-busy={pending}>退出运维</Button>
      <FormError message={error} />
    </div></header>
    <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6">{children}</main>
  </div>;
}

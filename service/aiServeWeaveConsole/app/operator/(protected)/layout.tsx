import { headers } from "next/headers";
import { operatorTarget } from "@/lib/console/operator-session";
import { redirect } from "next/navigation";
import { readOperatorSession } from "@/lib/server/operator-session";
import { OperatorShell } from "@/app/operator/operator-shell";

/** OperatorLayout requires a platform session independently of tenant login. / OperatorLayout 独立于租户登录校验平台会话。 */
export default async function OperatorLayout({ children }: { children: React.ReactNode }) {
  const session = await readOperatorSession();
  if (!session) {
    const target = operatorTarget((await headers()).get("x-aisw-operator-return"));
    redirect(`/operator/login?next=${encodeURIComponent(target)}`);
  }
  return <OperatorShell operator={session.operator}>{children}</OperatorShell>;
}

import { redirect } from "next/navigation";

import { OperatorsView } from "@/app/operator/(protected)/operators/operators-view";
import { readOperatorSession } from "@/lib/server/operator-session";

/** OperatorsPage renders platform account lifecycle management. / OperatorsPage 渲染平台账户生命周期管理。 */
export default async function OperatorsPage() {
  const session = await readOperatorSession();
  if (!session) redirect("/operator/login");
  return <OperatorsView currentOperatorId={session.operator.id} />;
}

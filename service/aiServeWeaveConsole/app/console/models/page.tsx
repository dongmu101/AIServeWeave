import { redirect } from "next/navigation";

/** LegacyPage moves the former inventory route to platform authentication. / LegacyPage 将旧清单路由迁移到平台认证入口。 */
export default function LegacyPage() { redirect("/operator/models"); }

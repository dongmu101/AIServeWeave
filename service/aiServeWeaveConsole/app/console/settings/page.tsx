import { redirect } from "next/navigation";

import { SettingsView } from "@/app/console/settings/settings-view";
import { readSession } from "@/lib/server/session";

/**
 * The settings page. Whether a Gateway Key is already configured comes from
 * the sealed session rather than a client-side fetch: the same server render
 * that decides the nav and the role already has the fact this page's first
 * paint needs, and fetching it again in the browser would show a flash of
 * "not configured" before the real answer arrives.
 *
 * 设置页。是否已经配置了 Gateway Key，来自密封的会话，而不是客户端再发一次请求：
 * 决定导航与角色的同一次服务端渲染，早就已经掌握了这个页面首次绘制所需要的事实，
 * 在浏览器里再取一次，只会先闪一下「未配置」再等到真正的答案。
 */
export default async function SettingsPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return (
    <SettingsView
      role={session.user.role}
      hasGatewayKey={Boolean(session.gatewayApiKey)}
    />
  );
}

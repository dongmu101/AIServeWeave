import { redirect } from "next/navigation";

import { parseModels, studioErrorCode, type StudioModel } from "@/lib/studio/contract";
import type { SessionPayload } from "@/lib/studio/session-payload";
import { readSession } from "@/lib/server/session";
import { callGateway } from "@/lib/server/upstream";

/**
 * Server-side reads of the Studio API for page rendering.
 * 页面渲染所用的 Studio API 服务端读取。
 */

/** requireSession returns the session or sends the visitor to sign in.
 * requireSession 返回会话，否则把访客送去登录。 */
export async function requireSession(returnTo: string): Promise<SessionPayload> {
  const session = await readSession();
  if (!session) redirect(`/login?next=${encodeURIComponent(returnTo)}`);
  return session;
}

export type ModelsLoad = { kind: "ok"; models: StudioModel[] } | { kind: "error"; code: string };

/** loadModels reads the user's models. A session the Gateway refuses is sent
 * through /api/session/expired, which clears the dead cookie before sign-in
 * so the sign-in page does not bounce straight back here.
 *
 * loadModels 读取用户可用的模型。Gateway 拒绝的会话会经 /api/session/expired 处理：
 * 先清除失效的 Cookie 再去登录，登录页才不会立刻把人弹回这里。 */
export async function loadModels(session: SessionPayload, returnTo: string): Promise<ModelsLoad> {
  const result = await callGateway({ method: "GET", path: "/studio/v1/models", token: session.token });
  if (result.kind !== "response") return { kind: "error", code: "upstream" };
  if (result.status === 401) redirect(`/api/session/expired?next=${encodeURIComponent(returnTo)}`);
  if (result.status !== 200) return { kind: "error", code: studioErrorCode(result.text) || "upstream" };
  try {
    return { kind: "ok", models: parseModels(JSON.parse(result.text)) };
  } catch {
    return { kind: "error", code: "upstream_contract" };
  }
}

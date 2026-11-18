import { handoffOriginAllowed } from "@/lib/studio/handoff-origin";
import { openTicket } from "@/lib/studio/handoff-ticket";
import { handoffConfig } from "@/lib/server/config";
import { readBoundedText } from "@/lib/server/responses";
import { readSession, writeSession } from "@/lib/server/session";
import { callControlPlane } from "@/lib/server/upstream";

/**
 * POST receives a sign-in from the Console: a form post carrying a sealed
 * ticket with a session the control plane issued for this person. A valid
 * ticket from an allowed origin becomes the Studio session and the browser
 * lands on the home page; anything else lands on sign-in, with no detail.
 * A different session already here is revoked first, best effort.
 *
 * POST 接收来自 Console 的登录：一次表单提交，携带一张密封票据，内含控制面为此人签发的
 * 会话。来自允许来源的有效票据成为 Studio 会话，浏览器落到首页；其余一律落到登录页，不给
 * 细节。此处已有的另一个会话会先被尽力吊销。
 */
export async function POST(request: Request): Promise<Response> {
  const config = handoffConfig();
  if (
    !config ||
    !handoffOriginAllowed({
      origin: request.headers.get("origin"),
      secFetchSite: request.headers.get("sec-fetch-site"),
      consoleOrigins: config.consoleOrigins,
    })
  ) {
    return seeOther("/login");
  }
  const raw = await readBoundedText(request);
  const value = raw === null ? null : new URLSearchParams(raw).get("ticket");
  const ticket = value ? openTicket(value, config.key) : null;
  if (!ticket) return seeOther("/login");

  const previous = await readSession();
  if (previous && previous.token !== ticket.token) {
    await callControlPlane({ method: "DELETE", path: "/admin/v1/auth/session", token: previous.token });
  }
  await writeSession({ token: ticket.token, expiresAt: ticket.expiresAt, user: ticket.user });
  return seeOther("/");
}

/** seeOther answers with a relative 303, so the browser follows it with GET
 * on this same host whatever address the server sits behind.
 *
 * seeOther 以相对地址的 303 回答，浏览器无论本服务处在什么地址之后，都会在同一主机上
 * 用 GET 跟随。 */
function seeOther(location: string): Response {
  return new Response(null, { status: 303, headers: { Location: location, "Cache-Control": "no-store" } });
}

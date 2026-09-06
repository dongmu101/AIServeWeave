import { resolveOperatorUpstream } from "@/lib/console/upstream-routes";
import { callControlPlane } from "@/lib/server/control-plane";
import { operatorAccess } from "@/lib/server/operator";
import { errorResponse, rawJsonResponse } from "@/lib/server/responses";
import { serverConfig } from "@/lib/server/config";
import { readSession } from "@/lib/server/session";

/**
 * This is the browser's entry point to the fleet inventory, and it is separate
 * from the Admin API entry point on purpose.
 *
 * The two forward with different credentials to different path prefixes, and
 * they are open to different people: the Admin API acts as the signed-in
 * tenant user, while this one spends a deployment secret that has no user
 * behind it. Keeping them apart means the operator token is unreachable from
 * the route that handles tenant traffic, whatever a later change does to that
 * route's allowlist.
 *
 * Access needs both halves — see lib/server/operator for why the Console is
 * the one deciding, and why that is a gap rather than a design.
 *
 * 这是浏览器进入机群清单的入口，它刻意与 Admin API 的入口分开。
 *
 * 两者用不同的凭据转发到不同的路径前缀，且面向不同的人开放：Admin API 以已登录的租户
 * 用户身份行事，而这一个花的是一个背后没有用户的部署密钥。把它们分开，意味着无论将来
 * 对处理租户流量那条路由的白名单做什么改动，运维 token 从那条路由都够不到。
 *
 * 访问需要两半同时成立——为什么由 Console 来做这个决定、以及为什么那是一处缺口而不是
 * 一种设计，见 lib/server/operator。
 */

interface RouteContext {
  params: Promise<{ path: string[] }>;
}

/** GET forwards a fleet read. There is no other method: the inventory is
 * read-only, and node management is a feature with a design of its own.
 *
 * GET 转发一次机群读取。没有其他方法：清单是只读的，而节点管理是另一项有自己设计的
 * 功能。 */
export async function GET(request: Request, context: RouteContext) {
  const session = await readSession();
  if (!session) {
    return errorResponse(401, "unauthorized");
  }

  const access = operatorAccess(session.user);
  if (!access.allowed) {
    // Not found rather than forbidden, and the same answer whether this
    // deployment holds no token or this person is not an operator. A tenant
    // user probing it learns only that this Console has no such page —
    // "forbidden" would confirm that a fleet is reachable from here.
    //
    // 用「不存在」而不是「被禁止」，且无论是本部署没有 token 还是此人不是运维，答案
    // 都相同。探测它的租户用户只能得知本 Console 没有这个页面——而「被禁止」会证实
    // 从这里能够到某个机群。
    return errorResponse(404, "not_found");
  }

  const url = new URL(request.url);
  const { path } = await context.params;
  const upstream = resolveOperatorUpstream("GET", path, url.searchParams);
  if (!upstream) {
    return errorResponse(404, "not_found");
  }

  const result = await callControlPlane({
    method: "GET",
    path: upstream.path,
    search: upstream.search,
    token: serverConfig().operatorToken,
  });
  if (result.kind !== "response") {
    return errorResponse(result.kind === "timeout" ? 504 : 502, "upstream");
  }
  if (result.status >= 400) {
    return errorResponse(result.status === 404 ? 404 : 502, "upstream_error");
  }
  return rawJsonResponse(result.status, result.text);
}

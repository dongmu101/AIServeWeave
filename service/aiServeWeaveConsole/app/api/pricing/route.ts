import { folkbenchPricing, parsePublicPlanPrices, type PublicPlanPrices } from "@/lib/console/folkbench";
import { createTtlCache } from "@/lib/console/ttl-cache";
import { callControlPlane } from "@/lib/server/control-plane";
import { legalEntity } from "@/lib/server/legal";
import { errorResponse } from "@/lib/server/responses";

/** HOSTNAME is the shape of a host name a site domain may be read from.
 *
 * HOSTNAME 是可以作为站点域名读取的主机名形状。 */
const HOSTNAME = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/;

/** siteDomain is the host the request reached, without its port. The reverse
 * proxy passes Host through unchanged (see AGENTS.md), so this is the public
 * domain the price list was read from.
 *
 * siteDomain 是请求抵达的主机名，不含端口。反向代理原样透传 Host（见 AGENTS.md），因此
 * 它就是读取价格表所用的公开域名。 */
function siteDomain(request: Request): string {
  const host = (request.headers.get("host") ?? "").trim().toLowerCase().replace(/:\d+$/, "");
  if (HOSTNAME.test(host)) return host;
  return new URL(request.url).hostname;
}

/** fetchPlanPrices reads the public plan prices. 404 means payment billing is
 * off; any other failure must not look like an empty list. No session.
 *
 * fetchPlanPrices 读取公开的方案售价。404 表示未开启支付计费；其他失败不能看起来像空
 * 列表。不需要会话。 */
async function fetchPlanPrices(): Promise<PublicPlanPrices | "disabled" | "failed"> {
  const result = await callControlPlane({ method: "GET", path: "/billing/v1/plan-prices" });
  if (result.kind !== "response") return "failed";
  if (result.status === 404) return "disabled";
  if (result.status !== 200) return "failed";
  try {
    return parsePublicPlanPrices(JSON.parse(result.text));
  } catch {
    return "failed";
  }
}

/** The list is the same for every caller, so each Console process reuses it
 * for 30 seconds; a price change shows up within that window. A failure is
 * not kept.
 *
 * 列表对每个调用方都一样，因此每个 Console 进程复用它 30 秒；价格变更在这段时间内生效。
 * 失败不缓存。 */
const loadPlanPrices = createTtlCache(fetchPlanPrices, { ttlMs: 30_000, keep: value => value !== "failed" });

/**
 * GET answers the public model price list in Folkbench's format (see
 * lib/console/folkbench.ts): one group per stored price plan. It needs no
 * session. Billing turned off is 404, matching the control plane; a list that
 * cannot be read is 503, never an empty list a reader could take for "no
 * models".
 *
 * GET 以 Folkbench 格式答复公开的模型价格表（见 lib/console/folkbench.ts）：每个已保存
 * 的价格方案一个分组。它不需要会话。未开启计费时返回 404，与控制面一致；读取失败时返回
 * 503，绝不返回一个会被读者当作「没有模型」的空列表。
 */
export async function GET(request: Request): Promise<Response> {
  const prices = await loadPlanPrices();
  if (prices === "disabled") return errorResponse(404, "not_found");
  if (prices === "failed") return errorResponse(503, "unavailable");
  const body = folkbenchPricing(prices, {
    name: legalEntity().serviceName,
    domain: siteDomain(request),
    fallbackUpdatedAt: new Date().toISOString(),
  });
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: {
      "Content-Type": "application/json; charset=utf-8",
      // The same for every caller, like the control plane's answer.
      // 对每个调用方都一样，与控制面的答复相同。
      "Cache-Control": "public, max-age=60",
      "Access-Control-Allow-Origin": "*",
    },
  });
}

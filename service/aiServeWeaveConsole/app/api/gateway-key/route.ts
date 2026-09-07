import { canManageGatewayKey } from "@/lib/console/permissions";
import { isTrustedWrite } from "@/lib/console/request-origin";
import { errorResponse, jsonResponse, readBoundedText } from "@/lib/server/responses";
import { readSession, writeSession } from "@/lib/server/session";

/**
 * This is Console's own settings endpoint, not a forward to anything — the
 * one Gateway API Key it manages is sealed into this session's own cookie
 * (SessionPayload.gatewayApiKey), and no control plane call exists for it.
 * That means the role check below is not UI guidance restating a check made
 * elsewhere, the way it is everywhere else in this codebase — see
 * canManageGatewayKey's own doc comment — it is the only check.
 *
 * 这是 Console 自己的设置端点，不是对任何东西的转发——它管理的这一把 Gateway API
 * Key，密封在这个会话自己的 cookie 里（SessionPayload.gatewayApiKey），背后没有
 * 任何控制面调用存在。这意味着下面的角色检查，不像本代码库里其他任何地方那样只是
 * 复述别处已经做过的检查——见 canManageGatewayKey 自己的文档注释——它是唯一的
 * 那一道检查。
 */

/** MAX_KEY_BYTES bounds the pasted key. common/apikey's own plaintext form is
 * well under this; the bound exists to refuse an obviously wrong paste (a
 * whole curl command, say) before it is sealed into a cookie.
 *
 * MAX_KEY_BYTES 限制粘贴进来的 key 的大小。common/apikey 自己的明文形式远小于这个
 * 上限；设置它是为了在一次明显错误的粘贴（比如整段 curl 命令）被密封进 cookie 之前
 * 就拒绝它。 */
const MAX_KEY_BYTES = 512;

/** GET reports only whether a key is configured, never the key itself — the
 * same "shown once, never read back" shape the control plane's own API Key
 * creation already uses.
 *
 * GET 只报告是否已配置了一把 key，绝不报告 key 本身——与控制面自己的 API Key
 * 创建早已采用的「只展示一次，无法再读回」是同一种形状。 */
export async function GET() {
  const session = await readSession();
  if (!session) {
    return errorResponse(401, "unauthorized");
  }
  return jsonResponse(200, { configured: Boolean(session.gatewayApiKey) });
}

/** POST sets this tenant's Gateway API Key for the current session.
 *
 * POST 为当前会话设置本租户的 Gateway API Key。 */
export async function POST(request: Request) {
  const session = await readSession();
  if (!session) {
    return errorResponse(401, "unauthorized");
  }
  if (!canManageGatewayKey(session.user.role)) {
    return errorResponse(403, "forbidden");
  }
  if (
    !isTrustedWrite({
      method: request.method,
      origin: request.headers.get("origin"),
      host: request.headers.get("host"),
      secFetchSite: request.headers.get("sec-fetch-site"),
    })
  ) {
    return errorResponse(403, "forbidden_origin");
  }

  const raw = await readBoundedText(request);
  if (raw === null) {
    return errorResponse(413, "body_too_large");
  }
  let apiKey: string;
  try {
    const body = JSON.parse(raw) as unknown;
    const value = (body as Record<string, unknown> | null)?.apiKey;
    if (typeof value !== "string") {
      throw new Error("apiKey must be a string");
    }
    apiKey = value.trim();
  } catch {
    return errorResponse(400, "invalid");
  }
  if (apiKey === "" || apiKey.length > MAX_KEY_BYTES) {
    return errorResponse(400, "invalid");
  }

  await writeSession({ ...session, gatewayApiKey: apiKey });
  return jsonResponse(204, null);
}

/** DELETE clears this session's Gateway API Key.
 *
 * DELETE 清除本会话的 Gateway API Key。 */
export async function DELETE(request: Request) {
  const session = await readSession();
  if (!session) {
    return errorResponse(401, "unauthorized");
  }
  if (!canManageGatewayKey(session.user.role)) {
    return errorResponse(403, "forbidden");
  }
  if (
    !isTrustedWrite({
      method: request.method,
      origin: request.headers.get("origin"),
      host: request.headers.get("host"),
      secFetchSite: request.headers.get("sec-fetch-site"),
    })
  ) {
    return errorResponse(403, "forbidden_origin");
  }

  // gatewayApiKey is omitted entirely, not set to an empty string — an
  // absent key and a cleared key are the same state, per the field's own
  // doc comment.
  //
  // gatewayApiKey 被彻底省略，而不是设为空字符串——缺席的 key 与被清空的 key
  // 是同一种状态，这一点在该字段自己的文档注释里已经说明。
  await writeSession({
    token: session.token,
    expiresAt: session.expiresAt,
    user: session.user,
  });
  return jsonResponse(204, null);
}

import { issueCaptchaChallenge } from "@/lib/server/captcha";
import { serverConfig } from "@/lib/server/config";
import { errorResponse, jsonResponse } from "@/lib/server/responses";

/**
 * GET hands the sign-in and sign-up widget a freshly signed ALTCHA challenge.
 * It needs no session: it is what a visitor solves before they have one.
 * When the human check is off the route does not exist, matching the pages.
 *
 * GET 为登录与注册页的组件签发一个新的 ALTCHA 挑战。它不需要会话：访客正是在拥有会话
 * 之前求解它。人机验证关闭时本路由不存在，与页面一致。
 */
export async function GET(): Promise<Response> {
  const config = serverConfig();
  if (!config.captchaEnabled) {
    return errorResponse(404, "not_found");
  }
  return jsonResponse(200, await issueCaptchaChallenge(config.captchaSecrets));
}

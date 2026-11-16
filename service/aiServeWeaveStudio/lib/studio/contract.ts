/**
 * Parsers for the upstream JSON Studio reads. Each throws ContractError on a
 * shape it does not recognise, so a changed upstream is reported as a
 * contract failure rather than rendered as empty data.
 *
 * Studio 所读上游 JSON 的解析器。遇到无法识别的形状时抛出 ContractError，因此上游的
 * 变化会被报告为契约失败，而不是被渲染成空数据。
 */

export class ContractError extends Error {
  constructor() {
    super("upstream contract");
    this.name = "ContractError";
  }
}

function fail(): never {
  throw new ContractError();
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) fail();
  return value as Record<string, unknown>;
}

function text(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (typeof value !== "string") fail();
  return value;
}

function nonEmpty(source: Record<string, unknown>, key: string): string {
  const value = text(source, key);
  if (value === "") fail();
  return value;
}

export interface LoginResult {
  token: string;
  expiresAt: string;
  user: { id: string; tenantId: string; email: string; name: string; role: string };
}

/** parseLogin reads the control plane's POST /admin/v1/auth/login answer.
 * parseLogin 读取控制面 POST /admin/v1/auth/login 的应答。 */
export function parseLogin(value: unknown): LoginResult {
  const source = record(value);
  const expiresAt = nonEmpty(source, "expires_at");
  if (Number.isNaN(Date.parse(expiresAt))) fail();
  const user = record(source.user);
  return {
    token: nonEmpty(source, "token"),
    expiresAt,
    user: {
      id: nonEmpty(user, "id"),
      tenantId: nonEmpty(user, "tenant_id"),
      email: text(user, "email"),
      name: text(user, "name"),
      role: nonEmpty(user, "role"),
    },
  };
}

export interface StudioModel {
  id: string;
  name: string;
}

/** parseModels reads GET /studio/v1/models. Duplicate ids are dropped, first
 * one kept, so a picker never shows the same model twice.
 *
 * parseModels 读取 GET /studio/v1/models。重复的 id 只保留第一个，选择器因此不会把
 * 同一个模型显示两次。 */
export function parseModels(value: unknown): StudioModel[] {
  const data = record(value).data;
  if (data === null || data === undefined) return [];
  if (!Array.isArray(data)) fail();
  const seen = new Set<string>();
  const out: StudioModel[] = [];
  for (const item of data) {
    const source = record(item);
    const id = nonEmpty(source, "id");
    if (seen.has(id)) continue;
    seen.add(id);
    const name = typeof source.name === "string" && source.name !== "" ? source.name : id;
    out.push({ id, name });
  }
  return out;
}

/** studioErrorCode reads the stable code out of a Studio API error body, or
 * "" when there is none.
 *
 * studioErrorCode 从 Studio API 的错误体中读出稳定错误码，没有时返回 ""。 */
export function studioErrorCode(text: string): string {
  try {
    const error = (JSON.parse(text) as { error?: { code?: unknown } }).error;
    return typeof error?.code === "string" ? error.code : "";
  } catch {
    return "";
  }
}

/** errorMessage is the fixed Chinese wording for a Studio-facing error code.
 * Upstream message text is never shown: it may describe internals.
 *
 * errorMessage 是面向 Studio 用户的错误码的固定中文文案。上游的 message 原文从不展示：
 * 它可能描述内部细节。 */
export function errorMessage(code: string): string {
  switch (code) {
    case "invalid_credentials":
      return "邮箱或密码不正确。";
    case "unauthorized":
      return "登录已过期，请重新登录。";
    case "insufficient_balance":
      return "余额不足，请联系管理员充值。";
    case "account_subscription_expired":
      return "账号包月已到期，请联系管理员续费。";
    case "rate_limited":
      return "请求太频繁，请稍后重试。";
    case "forbidden_origin":
      return "请求来源无效，请刷新页面后重试。";
    case "verification_unavailable":
    case "upstream":
    case "upstream_contract":
      return "服务暂不可用，请稍后重试。";
    default:
      return "出了点问题，请稍后重试。";
  }
}

/** greetingName picks what the home page calls the user: their name, else
 * the part of their email before the @.
 *
 * greetingName 决定首页如何称呼用户：优先用姓名，否则用邮箱 @ 之前的部分。 */
export function greetingName(user: { name: string; email: string }): string {
  const name = user.name.trim();
  if (name !== "") return name;
  const at = user.email.indexOf("@");
  return at > 0 ? user.email.slice(0, at) : user.email;
}

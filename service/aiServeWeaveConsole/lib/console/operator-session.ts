import { createCipheriv, createDecipheriv, hkdfSync, randomBytes } from "node:crypto";

/** OPERATOR_COOKIE names the independent platform session. / OPERATOR_COOKIE 是独立平台会话的名称。 */
export const OPERATOR_COOKIE = "aisw_operator_session";

/** OperatorIdentity contains only displayed platform identity fields. / OperatorIdentity 只包含展示所需的平台身份字段。 */
export interface OperatorIdentity {
  id: string;
  email: string;
  name: string;
}

/** OperatorSession binds the platform identity to its upstream credential. / OperatorSession 将平台身份与上游凭据绑定。 */
export interface OperatorSession {
  token: string;
  expiresAt: string;
  operator: OperatorIdentity;
}

/** parseOperatorLogin validates the platform login response, without decoding JWT claims. / parseOperatorLogin 校验平台登录响应，不解码或采信 JWT 声明。 */
export function parseOperatorLogin(value: unknown, now: Date = new Date()): OperatorSession {
  const source = record(value);
  const operator = record(source.operator);
  if (operator.status !== "active") throw new Error("invalid platform session");
  return validate({ token: source.token, expiresAt: source.expires_at, operator }, now);
}

/** sealOperator encrypts with a purpose-derived key and authenticated version. / sealOperator 用按用途派生的密钥加密，并认证版本。 */
export function sealOperator(payload: OperatorSession, key: Buffer): string {
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", operatorKey(key), iv);
  cipher.setAAD(Buffer.from("aisw-operator-v1"));
  const body = Buffer.concat([cipher.update(JSON.stringify(payload), "utf8"), cipher.final()]);
  return `op1.${Buffer.concat([iv, cipher.getAuthTag(), body]).toString("base64url")}`;
}

/** openOperator rejects tampered, expired or foreign-purpose cookies. / openOperator 拒绝被篡改、过期或其他用途的 Cookie。 */
export function openOperator(value: string, key: Buffer, now: Date = new Date()): OperatorSession | null {
  if (!value.startsWith("op1.")) return null;
  try {
    const data = Buffer.from(value.slice(4), "base64url");
    if (data.length <= 28) return null;
    const cipher = createDecipheriv("aes-256-gcm", operatorKey(key), data.subarray(0, 12));
    cipher.setAAD(Buffer.from("aisw-operator-v1"));
    cipher.setAuthTag(data.subarray(12, 28));
    const plain = Buffer.concat([cipher.update(data.subarray(28)), cipher.final()]);
    return validate(JSON.parse(plain.toString("utf8")), now);
  } catch {
    return null;
  }
}

/** operatorKey separates platform cookies cryptographically from tenant cookies. / operatorKey 在密码学上隔离平台与租户 Cookie。 */
function operatorKey(key: Buffer): Buffer {
  return Buffer.from(hkdfSync("sha256", key, "aisw-console", "operator-session-v1", 32));
}

/** validate checks the complete payload and returns only permitted fields. / validate 检查完整载荷，只返回允许的字段。 */
function validate(value: unknown, now: Date): OperatorSession {
  const source = record(value);
  const operator = record(source.operator);
  if (typeof source.token !== "string" || !source.token || typeof source.expiresAt !== "string" ||
      !Number.isFinite(Date.parse(source.expiresAt)) || Date.parse(source.expiresAt) <= now.getTime() ||
      typeof operator.id !== "string" || !operator.id || typeof operator.email !== "string" || !operator.email ||
      typeof operator.name !== "string") throw new Error("invalid platform session");
  return { token: source.token, expiresAt: source.expiresAt, operator: { id: operator.id, email: operator.email, name: operator.name } };
}

/** record rejects non-object input. / record 拒绝非对象输入。 */
function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("invalid platform session");
  return value as Record<string, unknown>;
}

/** operatorTarget restricts post-login navigation to known platform pages. / operatorTarget 将登录后导航限制在已知平台页面。 */
export function operatorTarget(value?: string | null): string {
  if (!value || /[\\\r\n]/.test(value)) return "/operator/fleet";
  const path = value.split(/[?#]/)[0];
  if (!["/operator/fleet", "/operator/models", "/operator/workflows", "/operator/audit"].includes(path)) return "/operator/fleet";
  return value;
}

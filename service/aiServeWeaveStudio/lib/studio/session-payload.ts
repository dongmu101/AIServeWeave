import {
  createCipheriv,
  createDecipheriv,
  hkdfSync,
  randomBytes,
  timingSafeEqual,
} from "node:crypto";

/**
 * The Studio session cookie: a control plane session token sealed with
 * AES-256-GCM, so the browser can neither read the token nor edit the user
 * in it. It mirrors the Console's design but is a separate cookie with a
 * separately derived key, so a Console cookie never opens here and the other
 * way round.
 *
 * Studio 会话 Cookie：用 AES-256-GCM 密封的控制面会话令牌，浏览器既读不到令牌，也改
 * 不了其中的用户。设计沿用 Console，但 Cookie 名与派生密钥各自独立，因此 Console 的
 * Cookie 在这里打不开，反之亦然。
 */

export const SESSION_COOKIE = "aisw_studio_session";

const VERSION = "v1";
const IV_BYTES = 12;
const TAG_BYTES = 16;

export interface SessionUser {
  id: string;
  tenantId: string;
  email: string;
  name: string;
  role: string;
}

export interface SessionPayload {
  /** token is the control plane session token; it never reaches the browser.
   * token 是控制面会话令牌；它永远不会到达浏览器。 */
  token: string;
  expiresAt: string;
  user: SessionUser;
}

/** deriveSessionKey stretches the configured secret into the AES key. The
 * info string differs from the Console's, so one shared secret still yields
 * two unrelated keys.
 *
 * deriveSessionKey 把配置的密钥扩展为 AES 密钥。info 字符串与 Console 的不同，即使
 * 共用同一份密钥材料，也会得到两把互不相关的密钥。 */
export function deriveSessionKey(secret: string): Buffer {
  return Buffer.from(hkdfSync("sha256", secret, "aisw-studio", "studio-session-cookie-v1", 32));
}

export function seal(payload: SessionPayload, key: Buffer): string {
  const iv = randomBytes(IV_BYTES);
  const cipher = createCipheriv("aes-256-gcm", key, iv);
  const body = Buffer.concat([cipher.update(JSON.stringify(payload), "utf8"), cipher.final()]);
  return `${VERSION}.${Buffer.concat([iv, cipher.getAuthTag(), body]).toString("base64url")}`;
}

/** open returns null for every failure — wrong version, wrong key, tampering
 * or a malformed payload — so a caller cannot tell them apart.
 *
 * open 对每一种失败——版本不对、密钥不对、被篡改或载荷畸形——都返回 null，调用方无从
 * 区分。 */
export function open(value: string, key: Buffer): SessionPayload | null {
  const separator = value.indexOf(".");
  if (separator < 0) return null;
  const version = Buffer.from(value.slice(0, separator));
  const expected = Buffer.from(VERSION);
  if (version.length !== expected.length || !timingSafeEqual(version, expected)) return null;
  const sealed = Buffer.from(value.slice(separator + 1), "base64url");
  if (sealed.length <= IV_BYTES + TAG_BYTES) return null;
  try {
    const decipher = createDecipheriv("aes-256-gcm", key, sealed.subarray(0, IV_BYTES));
    decipher.setAuthTag(sealed.subarray(IV_BYTES, IV_BYTES + TAG_BYTES));
    const plain = Buffer.concat([decipher.update(sealed.subarray(IV_BYTES + TAG_BYTES)), decipher.final()]);
    return validate(JSON.parse(plain.toString("utf8")));
  } catch {
    return null;
  }
}

export function isExpired(payload: SessionPayload, now: Date): boolean {
  const expiry = Date.parse(payload.expiresAt);
  return Number.isNaN(expiry) || expiry <= now.getTime();
}

function validate(value: unknown): SessionPayload | null {
  if (typeof value !== "object" || value === null) return null;
  const source = value as Record<string, unknown>;
  const user = source.user;
  if (typeof user !== "object" || user === null) return null;
  const fields = user as Record<string, unknown>;
  const strings = [source.token, source.expiresAt, fields.id, fields.tenantId, fields.email, fields.name, fields.role];
  if (strings.some((field) => typeof field !== "string")) return null;
  return {
    token: source.token as string,
    expiresAt: source.expiresAt as string,
    user: {
      id: fields.id as string,
      tenantId: fields.tenantId as string,
      email: fields.email as string,
      name: fields.name as string,
      role: fields.role as string,
    },
  };
}

import { createCipheriv, createDecipheriv, hkdfSync, randomBytes } from "node:crypto";

/**
 * A Studio handoff ticket carries a freshly issued control plane session from
 * the Console server to the Studio server through the browser, which only
 * ever sees ciphertext. Both servers derive the same AES-256-GCM key from
 * AISW_STUDIO_HANDOFF_SECRET; the ticket lives TICKET_TTL_MS, long enough for
 * one form post. Console and Studio each keep an identical copy of this file
 * (lib/console/studio-handoff.ts, lib/studio/handoff-ticket.ts); change both.
 *
 * Studio 交接票据把一个新签发的控制面会话从 Console 服务端经浏览器带到 Studio 服务端，
 * 浏览器只见到密文。两个服务端从 AISW_STUDIO_HANDOFF_SECRET 派生同一把 AES-256-GCM 密钥；
 * 票据有效期为 TICKET_TTL_MS，够完成一次表单提交。Console 与 Studio 各保留一份相同的
 * 本文件（lib/console/studio-handoff.ts、lib/studio/handoff-ticket.ts），改动须同步。
 */

export const TICKET_TTL_MS = 60_000;

const VERSION = "h1";
const IV_BYTES = 12;
const TAG_BYTES = 16;

export interface HandoffUser {
  id: string;
  tenantId: string;
  email: string;
  name: string;
  role: string;
}

export interface HandoffTicket {
  token: string;
  /** expiresAt is the session token's own expiry, RFC 3339. / expiresAt 是会话令牌自身的过期时刻。 */
  expiresAt: string;
  /** notAfter is when the ticket stops being accepted, RFC 3339. / notAfter 是票据不再被接受的时刻。 */
  notAfter: string;
  user: HandoffUser;
}

/** handoffKey derives the ticket key; at least 32 characters of secret. / handoffKey 派生票据密钥，密钥材料至少 32 个字符。 */
export function handoffKey(secret: string): Buffer {
  if (secret.length < 32) throw new Error("AISW_STUDIO_HANDOFF_SECRET must be at least 32 characters");
  return Buffer.from(hkdfSync("sha256", secret, "aisw-studio-handoff", "studio-handoff-ticket-v1", 32));
}

export function sealTicket(ticket: HandoffTicket, key: Buffer): string {
  const iv = randomBytes(IV_BYTES);
  const cipher = createCipheriv("aes-256-gcm", key, iv);
  cipher.setAAD(Buffer.from(VERSION));
  const body = Buffer.concat([cipher.update(JSON.stringify(ticket), "utf8"), cipher.final()]);
  return `${VERSION}.${Buffer.concat([iv, cipher.getAuthTag(), body]).toString("base64url")}`;
}

/** openTicket returns null for every failure, an expired ticket included. / openTicket 对每一种失败（含过期）都返回 null。 */
export function openTicket(value: string, key: Buffer, now: Date = new Date()): HandoffTicket | null {
  const prefix = `${VERSION}.`;
  if (!value.startsWith(prefix)) return null;
  const sealed = Buffer.from(value.slice(prefix.length), "base64url");
  if (sealed.length <= IV_BYTES + TAG_BYTES) return null;
  let ticket: HandoffTicket | null;
  try {
    const decipher = createDecipheriv("aes-256-gcm", key, sealed.subarray(0, IV_BYTES));
    decipher.setAAD(Buffer.from(VERSION));
    decipher.setAuthTag(sealed.subarray(IV_BYTES, IV_BYTES + TAG_BYTES));
    const plain = Buffer.concat([decipher.update(sealed.subarray(IV_BYTES + TAG_BYTES)), decipher.final()]);
    ticket = validate(JSON.parse(plain.toString("utf8")));
  } catch {
    return null;
  }
  if (!ticket) return null;
  const notAfter = Date.parse(ticket.notAfter);
  const expiresAt = Date.parse(ticket.expiresAt);
  if (Number.isNaN(notAfter) || notAfter <= now.getTime() || notAfter - now.getTime() > TICKET_TTL_MS) return null;
  if (Number.isNaN(expiresAt) || expiresAt <= now.getTime()) return null;
  return ticket;
}

function validate(value: unknown): HandoffTicket | null {
  if (typeof value !== "object" || value === null) return null;
  const source = value as Record<string, unknown>;
  const user = source.user;
  if (typeof user !== "object" || user === null) return null;
  const u = user as Record<string, unknown>;
  const strings = [source.token, source.expiresAt, source.notAfter, u.id, u.tenantId, u.email, u.name, u.role];
  if (strings.some((field) => typeof field !== "string") || source.token === "") return null;
  return {
    token: source.token as string,
    expiresAt: source.expiresAt as string,
    notAfter: source.notAfter as string,
    user: { id: u.id as string, tenantId: u.tenantId as string, email: u.email as string, name: u.name as string, role: u.role as string },
  };
}

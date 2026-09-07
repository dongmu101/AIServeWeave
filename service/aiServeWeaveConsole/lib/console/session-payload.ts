import {
  createCipheriv,
  createDecipheriv,
  hkdfSync,
  randomBytes,
  timingSafeEqual,
} from "node:crypto";

/**
 * This module seals a Console session into one cookie value and opens it
 * again. It is the reason the browser cannot choose its own role.
 *
 * The control plane's JWT is signed with a secret this service does not hold,
 * so the Console cannot verify it and must not read a role out of it and
 * believe the result. Sealing our own payload with a key only the Console
 * server has turns the cookie into something we can verify: a tampered cookie
 * fails authentication here, and never reaches the point where a role decides
 * which buttons render. Authorization still happens in the control plane —
 * this only ensures the session the Console renders from is one it issued.
 *
 * 本模块把一个 Console 会话密封进单个 cookie 值，并能再次打开它。浏览器无法自选角色，
 * 靠的就是它。
 *
 * 控制面的 JWT 由一个本服务并不持有的密钥签名，因此 Console 无法验证它，也就不能从中
 * 读出角色并采信。用一把只有 Console 服务端拥有的密钥密封我们自己的载荷，就把 cookie
 * 变成了可验证的东西：被篡改的 cookie 在这里就认证失败，根本到不了「由角色决定渲染哪些
 * 按钮」的那一步。授权依然发生在控制面——这里只保证 Console 据以渲染的会话确实是它自己
 * 签发的。
 */

/**
 * SESSION_COOKIE is the name of the one cookie the Console sets. It lives in
 * this module, next to the format it names, so the proxy can look for it
 * without pulling in the server-only modules that open it.
 *
 * SESSION_COOKIE 是 Console 所设置的唯一一个 cookie 的名称。它就放在本模块中、紧挨着
 * 它所命名的格式，这样 proxy 只需查找它，而不必牵扯进那些负责打开它的服务端专用模块。
 */
export const SESSION_COOKIE = "aisw_console_session";

/** VERSION prefixes every sealed value so a future key or format change can be
 * recognized instead of decrypted as garbage.
 *
 * VERSION 是每个密封值的前缀，让将来的密钥或格式变更可以被识别，而不是被当成乱码解密。 */
const VERSION = "v1";

/** IV_BYTES is the GCM nonce length, and TAG_BYTES its authentication tag.
 *
 * IV_BYTES 是 GCM 的 nonce 长度，TAG_BYTES 是它的认证标签长度。 */
const IV_BYTES = 12;
const TAG_BYTES = 16;

/**
 * SessionUser is the identity the Console renders from: the fields a page or a
 * navigation actually needs, and nothing more. The password digest has no
 * field here for the same reason it has none in the Go response type.
 *
 * SessionUser 是 Console 据以渲染的身份：页面或导航真正需要的那些字段，别无其他。
 * 密码摘要在这里没有字段，理由与它在 Go 响应类型里没有字段相同。
 */
export interface SessionUser {
  id: string;
  tenantId: string;
  email: string;
  name: string;
  role: string;
}

/**
 * SessionPayload is what the cookie carries. The control plane token is in
 * here rather than in a cookie of its own: one sealed value means there is no
 * state in which the Console holds a token but not the identity that goes
 * with it.
 *
 * SessionPayload 是 cookie 所携带的内容。控制面令牌放在这里而不是单独一个 cookie：
 * 只有一个密封值，就不存在「Console 持有令牌却没有与之配套的身份」这种状态。
 */
export interface SessionPayload {
  token: string;
  /** expiresAt is the control plane token's expiry, in RFC 3339.
   *
   * expiresAt 是控制面令牌的过期时刻，RFC 3339 格式。 */
  expiresAt: string;
  user: SessionUser;
  /**
   * gatewayApiKey is the one declared exception to "Console only talks to
   * the control plane": a tenant's own Gateway data-plane API Key, pasted in
   * by an owner or admin on the settings page, sealed into this same cookie
   * rather than a persistent store this stateless service does not have.
   * It powers exactly two calls — canceling a run and reading its artifacts
   * — both of which the Gateway itself authenticates with an ordinary
   * tenant API Key, never with anything the control plane issues.
   *
   * It is optional because most sessions never configure it, and absent
   * rather than empty-string when unset, so a cleared key and a key nobody
   * has ever set are the same state.
   *
   * This key is not scoped down: it can do everything any tenant-created key
   * can, including running inference against the tenant's own quota. That is
   * a deliberate, recorded trade-off for the stage this project is at (no
   * production tenants yet) — see the ControlPlane README's 「Job 持久化契约」
   * — not an oversight to quietly fix later without discussion.
   *
   * gatewayApiKey 是「Console 只与控制面对话」这条规则唯一的明文例外：一把该租户自己的
   * Gateway 数据面 API Key，由 owner 或 admin 在设置页粘贴进来，密封进这同一个 cookie，
   * 而不是这个无状态服务并不具备的某种持久化存储。它只驱动两次调用——取消一次运行、
   * 读取它的产物——两者 Gateway 自己认的都是一把普通的租户 API Key，而不是控制面签发的
   * 任何东西。
   *
   * 它是可选的，因为大多数会话从不配置它；未设置时是缺席而不是空字符串，这样「被清空的
   * key」与「从未设置过的 key」是同一种状态。
   *
   * 这把 key 没有做范围收紧：它能做任何租户自建 key 能做的一切，包括拿租户自己的配额去
   * 跑推理。这是针对本项目当前阶段（尚无生产租户）刻意做出、且已记录在案的权衡——见
   * ControlPlane README 的「Job 持久化契约」——不是留着以后悄悄修、不再讨论的疏漏。
   */
  gatewayApiKey?: string;
}

/**
 * deriveSessionKey turns a configured secret into the 32-byte AES key. HKDF is
 * used rather than the secret's bytes directly so that a short or low-entropy
 * secret still produces a full-length key, and so the same secret could later
 * derive a second key for a different purpose without the two being equal.
 *
 * deriveSessionKey 把配置的密钥材料变成 32 字节的 AES 密钥。这里用 HKDF 而不是直接
 * 使用密钥原始字节：即使密钥较短或熵较低，也仍能得到全长的密钥；而且同一份密钥材料
 * 将来可以为别的用途派生出第二把密钥，两者不会相等。
 */
export function deriveSessionKey(secret: string): Buffer {
  return Buffer.from(
    hkdfSync("sha256", secret, "aisw-console", "session-cookie-v1", 32)
  );
}

/**
 * seal encrypts a payload into a cookie-safe string. AES-256-GCM gives both
 * confidentiality and integrity from one primitive: an attacker can neither
 * read the control plane token out of a stolen cookie nor edit the role in it.
 *
 * seal 把一个载荷加密成可放进 cookie 的字符串。AES-256-GCM 用一个原语同时提供机密性
 * 与完整性：攻击者既无法从窃得的 cookie 中读出控制面令牌，也无法改动其中的角色。
 */
export function seal(payload: SessionPayload, key: Buffer): string {
  const iv = randomBytes(IV_BYTES);
  const cipher = createCipheriv("aes-256-gcm", key, iv);
  const body = Buffer.concat([
    cipher.update(JSON.stringify(payload), "utf8"),
    cipher.final(),
  ]);
  const sealed = Buffer.concat([iv, cipher.getAuthTag(), body]);
  return `${VERSION}.${sealed.toString("base64url")}`;
}

/**
 * open decrypts a cookie value. It returns null for every failure — wrong
 * version, wrong key, truncated value, tampered ciphertext, or a payload whose
 * shape does not match — because the caller's response is the same in all of
 * them: treat the request as unauthenticated.
 *
 * open 解密一个 cookie 值。任何失败都返回 null —— 版本不对、密钥不对、值被截断、
 * 密文被篡改，或载荷形状不符 —— 因为调用方在所有这些情况下的处置相同：把该请求
 * 当作未认证。
 */
export function open(value: string, key: Buffer): SessionPayload | null {
  const separator = value.indexOf(".");
  if (separator < 0) {
    return null;
  }
  const version = Buffer.from(value.slice(0, separator));
  const expected = Buffer.from(VERSION);
  if (
    version.length !== expected.length ||
    !timingSafeEqual(version, expected)
  ) {
    return null;
  }

  const sealed = Buffer.from(value.slice(separator + 1), "base64url");
  if (sealed.length <= IV_BYTES + TAG_BYTES) {
    return null;
  }

  try {
    const decipher = createDecipheriv(
      "aes-256-gcm",
      key,
      sealed.subarray(0, IV_BYTES)
    );
    decipher.setAuthTag(sealed.subarray(IV_BYTES, IV_BYTES + TAG_BYTES));
    const plain = Buffer.concat([
      decipher.update(sealed.subarray(IV_BYTES + TAG_BYTES)),
      decipher.final(),
    ]);
    return validate(JSON.parse(plain.toString("utf8")));
  } catch {
    return null;
  }
}

/**
 * isExpired reports whether the control plane token in a payload has run out.
 * The Console stops using an expired session on its own rather than waiting
 * for the control plane to answer 401, so an expired tab does not first show
 * data it can no longer refresh.
 *
 * isExpired 报告载荷中的控制面令牌是否已过期。Console 自行停用过期会话，而不是等控制面
 * 回 401，这样一个过期的标签页不会先展示它已无法刷新的数据。
 */
export function isExpired(payload: SessionPayload, now: Date): boolean {
  const expiry = Date.parse(payload.expiresAt);
  return Number.isNaN(expiry) || expiry <= now.getTime();
}

/** validate rejects a decrypted document whose shape is not a payload.
 *
 * validate 拒绝形状不是载荷的已解密文档。 */
function validate(value: unknown): SessionPayload | null {
  if (typeof value !== "object" || value === null) {
    return null;
  }
  const source = value as Record<string, unknown>;
  const user = source.user;
  if (typeof user !== "object" || user === null) {
    return null;
  }
  const fields = user as Record<string, unknown>;
  const strings = [
    source.token,
    source.expiresAt,
    fields.id,
    fields.tenantId,
    fields.email,
    fields.name,
    fields.role,
  ];
  if (strings.some((field) => typeof field !== "string")) {
    return null;
  }
  // gatewayApiKey is optional, but its presence is strict: a wrong-typed
  // value fails the whole session the way a wrong-typed required field does,
  // rather than being silently dropped. A cookie this server sealed never
  // has one, so seeing one is a sign the format changed underneath this
  // reader, not something to paper over.
  //
  // gatewayApiKey 是可选的，但一旦出现，类型要求是严格的：一个类型不对的值，会像某个
  // 必填字段类型不对时一样让整个会话失败，而不是被悄悄丢弃。本服务密封的 cookie 里
  // 这个字段要么是字符串要么缺席；出现一个类型不对的值，说明格式在这个读取器之下已经
  // 变了，而不是可以糊弄过去的小事。
  if (
    source.gatewayApiKey !== undefined &&
    typeof source.gatewayApiKey !== "string"
  ) {
    return null;
  }
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
    ...(source.gatewayApiKey !== undefined
      ? { gatewayApiKey: source.gatewayApiKey as string }
      : {}),
  };
}

import { createChallenge, pbkdf2, verifySolution, type Challenge, type Payload } from "altcha/lib";

/**
 * This module is the server side of the human check on the sign-in and
 * sign-up pages. The check itself is ALTCHA's proof of work: the browser's
 * widget solves a challenge this module signs, and the routes verify the
 * solution before anything reaches the control plane. Nothing here calls a
 * third-party service.
 *
 * ALTCHA's signature proves a challenge came from this server and has not
 * expired; it does not prove a solution is fresh. Each verified challenge is
 * therefore remembered until it expires and refused a second time. The memory
 * is per process: with several Console replicas, one solution can be spent
 * once on each.
 *
 * 本模块是登录页与注册页人机验证的服务端部分。验证本身用的是 ALTCHA 的工作量证明：
 * 浏览器中的组件求解本模块签发的挑战，路由在任何请求到达控制面之前校验其解。这里不调用
 * 任何第三方服务。
 *
 * ALTCHA 的签名只能证明挑战出自本服务且未过期，不能证明解是新的。因此每个校验通过的
 * 挑战都会被记住直到过期，再次提交时拒绝。这份记录是进程级的：Console 有多个副本时，
 * 同一个解在每个副本上各能用一次。
 */

/** CHALLENGE_TTL_SECONDS is how long a challenge stays solvable and spendable.
 * CHALLENGE_TTL_SECONDS 是一个挑战可被求解与使用的时长。 */
export const CHALLENGE_TTL_SECONDS = 300;

/** CaptchaSecrets are the two HMAC keys ALTCHA signs a challenge and its
 * expected key with.
 *
 * CaptchaSecrets 是 ALTCHA 用来签名挑战及其预期密钥的两把 HMAC 密钥。 */
export interface CaptchaSecrets {
  signature: string;
  keySignature: string;
}

/** ReplayGuard remembers spent challenges. `spend` returns false when the
 * signature was already spent.
 *
 * ReplayGuard 记录已使用的挑战。签名已被使用过时 `spend` 返回 false。 */
export interface ReplayGuard {
  spend(signature: string, expiresAtSeconds: number, nowSeconds: number): boolean;
}

/** memoryReplayGuard keeps spent signatures in a Map until they expire.
 * Expired entries are swept on each spend, so the Map holds at most the
 * challenges spent within one TTL.
 *
 * memoryReplayGuard 把已使用的签名保存在 Map 中直到过期。每次 spend 时清理过期条目，
 * 因此 Map 最多只保存一个 TTL 内用过的挑战。 */
export function memoryReplayGuard(): ReplayGuard {
  const spent = new Map<string, number>();
  return {
    spend(signature, expiresAtSeconds, nowSeconds) {
      for (const [key, expiry] of spent) {
        if (expiry < nowSeconds) spent.delete(key);
      }
      if (spent.has(signature)) return false;
      spent.set(signature, expiresAtSeconds);
      return true;
    },
  };
}

const processGuard = memoryReplayGuard();

/** issueCaptchaChallenge signs a new challenge for the widget. The cost and
 * counter range are ALTCHA's documented defaults.
 *
 * issueCaptchaChallenge 为组件签发一个新挑战。cost 与 counter 范围取 ALTCHA 文档中的
 * 推荐值。 */
export function issueCaptchaChallenge(
  secrets: CaptchaSecrets,
  nowMs: number = Date.now()
): Promise<Challenge> {
  return createChallenge({
    algorithm: "PBKDF2/SHA-256",
    cost: 5_000,
    counter: 5_000 + Math.floor(Math.random() * 5_000),
    deriveKey: pbkdf2.deriveKey,
    expiresAt: Math.floor(nowMs / 1000) + CHALLENGE_TTL_SECONDS,
    hmacSignatureSecret: secrets.signature,
    hmacKeySignatureSecret: secrets.keySignature,
  });
}

/** verifyCaptcha reports whether payload — the widget's base64 form value —
 * carries a valid, unexpired, unspent solution. Any malformed input is
 * simply false.
 *
 * verifyCaptcha 报告 payload（组件提交的 base64 表单值）是否携带一个有效、未过期、
 * 未使用过的解。任何畸形输入一律返回 false。 */
export async function verifyCaptcha(
  payload: unknown,
  secrets: CaptchaSecrets,
  guard: ReplayGuard = processGuard,
  nowMs: number = Date.now()
): Promise<boolean> {
  const decoded = decodePayload(payload);
  if (!decoded) return false;
  const { challenge, solution } = decoded;
  const expiresAt = challenge.parameters.expiresAt;
  // A challenge without an expiry was not issued here and could be replayed
  // forever once the guard forgot it.
  // 没有过期时间的挑战不是本服务签发的，一旦记录被清理就能被无限重放。
  if (typeof expiresAt !== "number" || typeof challenge.signature !== "string") return false;
  try {
    const result = await verifySolution({
      challenge,
      solution,
      deriveKey: pbkdf2.deriveKey,
      hmacSignatureSecret: secrets.signature,
      hmacKeySignatureSecret: secrets.keySignature,
    });
    if (!result.verified) return false;
  } catch {
    return false;
  }
  return guard.spend(challenge.signature, expiresAt, Math.floor(nowMs / 1000));
}

function decodePayload(payload: unknown): Payload | null {
  if (typeof payload !== "string" || payload === "") return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(Buffer.from(payload, "base64").toString("utf8"));
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null) return null;
  const { challenge, solution } = parsed as Record<string, unknown>;
  if (typeof challenge !== "object" || challenge === null) return null;
  if (typeof solution !== "object" || solution === null) return null;
  const parameters = (challenge as Record<string, unknown>).parameters;
  if (typeof parameters !== "object" || parameters === null) return null;
  return parsed as Payload;
}

import { deriveSessionKey } from "@/lib/studio/session-payload";

/**
 * Studio's server configuration. Nothing here is NEXT_PUBLIC_ and no client
 * component may import this module: the upstream addresses and the session
 * key are server facts.
 *
 * Studio 的服务端配置。这里没有任何 NEXT_PUBLIC_ 变量，也不允许客户端组件引用本模块：
 * 上游地址与会话密钥是服务端事实。
 */

const DEFAULT_CONTROL_PLANE_URL = "http://127.0.0.1:8090";
const DEFAULT_GATEWAY_URL = "http://127.0.0.1:8080";
const DEFAULT_TIMEOUT_MS = 10_000;

export interface ServerConfig {
  /** controlPlaneUrl is used for sign-in and sign-out only.
   * controlPlaneUrl 只用于登录与登出。 */
  controlPlaneUrl: string;
  /** gatewayUrl serves everything else, through the Studio API.
   * gatewayUrl 经 Studio API 提供其余一切。 */
  gatewayUrl: string;
  sessionKey: Buffer;
  cookieSecure: boolean;
  timeoutMs: number;
}

let cached: ServerConfig | null = null;

/** serverConfig resolves the configuration on first use, so `next build`
 * needs no deployment secret.
 *
 * serverConfig 在首次使用时解析配置，因此 `next build` 不需要部署密钥。 */
export function serverConfig(): ServerConfig {
  if (cached) return cached;
  cached = {
    controlPlaneUrl: absoluteURL("AISW_STUDIO_CONTROL_PLANE_URL", DEFAULT_CONTROL_PLANE_URL),
    gatewayUrl: absoluteURL("AISW_STUDIO_GATEWAY_URL", DEFAULT_GATEWAY_URL),
    sessionKey: deriveSessionKey(sessionSecret()),
    cookieSecure: flag(process.env.AISW_STUDIO_COOKIE_SECURE, process.env.NODE_ENV === "production"),
    timeoutMs: positiveInt(process.env.AISW_STUDIO_UPSTREAM_TIMEOUT_MS, DEFAULT_TIMEOUT_MS),
  };
  return cached;
}

function absoluteURL(name: string, fallback: string): string {
  const raw = process.env[name]?.trim();
  if (!raw) return fallback;
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error(`${name} must be an absolute URL such as ${fallback}`);
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new Error(`${name} must use http or https`);
  }
  return parsed.origin;
}

/** sessionSecret is required in production; development falls back to an
 * ephemeral per-process secret, so sessions end when the dev server restarts.
 *
 * sessionSecret 在生产环境必填；开发环境退回进程级临时密钥，开发服务器重启后会话即失效。 */
function sessionSecret(): string {
  const configured = process.env.AISW_STUDIO_SESSION_SECRET?.trim();
  if (configured) {
    if (configured.length < 32) throw new Error("AISW_STUDIO_SESSION_SECRET must be at least 32 characters");
    return configured;
  }
  if (process.env.NODE_ENV === "production") throw new Error("AISW_STUDIO_SESSION_SECRET is required in production");
  console.warn("[studio] AISW_STUDIO_SESSION_SECRET is unset; using an ephemeral development secret.");
  return crypto.randomUUID() + crypto.randomUUID();
}

function flag(raw: string | undefined, fallback: boolean): boolean {
  const value = raw?.trim().toLowerCase();
  if (value === undefined || value === "") return fallback;
  return value === "1" || value === "true" || value === "yes";
}

function positiveInt(raw: string | undefined, fallback: number): number {
  const value = Number(raw?.trim());
  return Number.isInteger(value) && value > 0 ? value : fallback;
}

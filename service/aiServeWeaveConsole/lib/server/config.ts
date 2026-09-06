import { deriveSessionKey } from "@/lib/console/session-payload";

/**
 * This module reads the Console server's configuration. Nothing here is
 * prefixed `NEXT_PUBLIC_`, and nothing here may be imported by a client
 * component: the upstream address and the session key are server facts, and a
 * browser that could read either would make the forwarding entry point
 * pointless.
 *
 * 本模块读取 Console 服务端的配置。这里没有任何 `NEXT_PUBLIC_` 前缀的东西，也不允许
 * 被任何客户端组件引用：上游地址与会话密钥是服务端事实，浏览器若能读到其中任何一个，
 * 转发入口就失去了意义。
 */

/** DEFAULT_CONTROL_PLANE_URL matches the control plane's local default.
 *
 * DEFAULT_CONTROL_PLANE_URL 与控制面的本地默认地址一致。 */
const DEFAULT_CONTROL_PLANE_URL = "http://127.0.0.1:8090";

/** DEFAULT_TIMEOUT_MS bounds one call to the control plane.
 *
 * DEFAULT_TIMEOUT_MS 限定一次对控制面的调用。 */
const DEFAULT_TIMEOUT_MS = 10_000;

/**
 * ServerConfig is the resolved configuration of one request's server side.
 *
 * ServerConfig 是单次请求服务端一侧解析后的配置。
 */
export interface ServerConfig {
  controlPlaneUrl: string;
  sessionKey: Buffer;
  /** cookieSecure marks the session cookie HTTPS-only. It is on in production
   * and off in development, where the Console is served over plain HTTP.
   *
   * cookieSecure 把会话 cookie 标记为仅 HTTPS。生产开启；开发关闭，因为那时
   * Console 走的是明文 HTTP。 */
  cookieSecure: boolean;
  timeoutMs: number;
  /** operatorToken authenticates this Console to the control plane's fleet
   * endpoints. Empty means this deployment is not an operations console, and
   * the fleet pages do not exist in it.
   *
   * operatorToken 用于本 Console 向控制面的机群端点表明身份。为空表示本部署不是运维
   * 控制台，机群页面在其中根本不存在。 */
  operatorToken: string;
  /** operatorEmails are the people who may use that token, lowercased. An
   * empty list grants nobody — see lib/server/operator.
   *
   * operatorEmails 是可以使用那个 token 的人，已转小写。空列表不授予任何人
   * —— 见 lib/server/operator。 */
  operatorEmails: readonly string[];
}

/** cached holds the resolved configuration for the life of the process.
 *
 * cached 在进程生命期内保存已解析的配置。 */
let cached: ServerConfig | null = null;

/**
 * serverConfig resolves the configuration, once per process.
 *
 * It is a function rather than a module constant because a build must not
 * require the deployment's secrets: reading the environment at first use means
 * `next build` succeeds on a machine that has none, and a missing secret is
 * reported when a request actually needs it.
 *
 * serverConfig 解析配置，每个进程一次。
 *
 * 它是函数而不是模块常量，因为构建不该要求部署环境的密钥：在首次使用时才读取环境变量，
 * 意味着 `next build` 在一台没有这些密钥的机器上也能成功，而缺失的密钥会在某次请求真正
 * 需要它时被报出。
 */
export function serverConfig(): ServerConfig {
  if (cached) {
    return cached;
  }
  cached = {
    controlPlaneUrl: controlPlaneUrl(),
    sessionKey: deriveSessionKey(sessionSecret()),
    cookieSecure: flag(
      process.env.AISW_CONSOLE_COOKIE_SECURE,
      process.env.NODE_ENV === "production"
    ),
    timeoutMs: positiveInt(
      process.env.AISW_CONSOLE_UPSTREAM_TIMEOUT_MS,
      DEFAULT_TIMEOUT_MS
    ),
    operatorToken: process.env.AISW_CONSOLE_OPERATOR_TOKEN?.trim() ?? "",
    operatorEmails: emailList(process.env.AISW_CONSOLE_OPERATOR_EMAILS),
  };
  return cached;
}

/** controlPlaneUrl reads the upstream base address and refuses one that is not
 * an absolute HTTP URL — a relative or malformed value would otherwise be
 * discovered as a confusing failure inside a forwarded request.
 *
 * controlPlaneUrl 读取上游基地址，并拒绝不是绝对 HTTP URL 的值 —— 相对或畸形的值否则
 * 会在某次转发请求内部以令人费解的失败形式被发现。 */
function controlPlaneUrl(): string {
  const raw = process.env.AISW_CONSOLE_CONTROL_PLANE_URL?.trim();
  if (!raw) {
    return DEFAULT_CONTROL_PLANE_URL;
  }
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error(
      "AISW_CONSOLE_CONTROL_PLANE_URL must be an absolute URL such as http://127.0.0.1:8090"
    );
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new Error("AISW_CONSOLE_CONTROL_PLANE_URL must use http or https");
  }
  return parsed.origin;
}

/**
 * sessionSecret reads the key material the session cookie is sealed with.
 *
 * In production it is required: without it there is no way to tell a cookie
 * this server issued from one a visitor wrote. In development an ephemeral
 * per-process secret is generated instead, so a fresh clone runs without
 * setup — sessions then end when the dev server restarts, which is the visible
 * cost of not configuring one.
 *
 * sessionSecret 读取密封会话 cookie 所用的密钥材料。
 *
 * 生产环境必须提供：没有它就无从分辨一个 cookie 是本服务签发的还是访客自己写的。开发
 * 环境则改为生成一个进程级的临时密钥，让新克隆的仓库无需配置即可运行——代价是开发服务器
 * 重启后会话即失效，这就是不配置它的那份可见成本。
 */
function sessionSecret(): string {
  const configured = process.env.AISW_CONSOLE_SESSION_SECRET?.trim();
  if (configured) {
    if (configured.length < 32) {
      throw new Error(
        "AISW_CONSOLE_SESSION_SECRET must be at least 32 characters"
      );
    }
    return configured;
  }
  if (process.env.NODE_ENV === "production") {
    throw new Error("AISW_CONSOLE_SESSION_SECRET is required in production");
  }
  console.warn(
    "[console] AISW_CONSOLE_SESSION_SECRET is unset; using an ephemeral development secret. Sessions end when this process restarts."
  );
  return crypto.randomUUID() + crypto.randomUUID();
}

/**
 * emailList reads the operator allowlist. Values are lowercased and trimmed so
 * a list written with the casing of somebody's mail client still matches the
 * address the control plane normalized on sign-in.
 *
 * emailList 读取运维名单。取值会转小写并去空白，这样一份按某人邮件客户端的大小写写下的
 * 名单，依然能匹配上控制面在登录时归一化过的地址。
 */
function emailList(raw: string | undefined): readonly string[] {
  return (raw ?? "")
    .split(",")
    .map((entry) => entry.trim().toLowerCase())
    .filter((entry) => entry !== "");
}

/** flag reads a boolean environment variable.
 *
 * flag 读取一个布尔类型的环境变量。 */
function flag(raw: string | undefined, fallback: boolean): boolean {
  const value = raw?.trim().toLowerCase();
  if (value === undefined || value === "") {
    return fallback;
  }
  return value === "1" || value === "true" || value === "yes";
}

/** positiveInt reads a positive integer environment variable.
 *
 * positiveInt 读取一个正整数环境变量。 */
function positiveInt(raw: string | undefined, fallback: number): number {
  const value = Number(raw?.trim());
  return Number.isInteger(value) && value > 0 ? value : fallback;
}

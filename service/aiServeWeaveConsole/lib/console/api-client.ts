import { ApiError, isRetryable, kindForStatus } from "./errors.ts";
import type { ApiErrorKind } from "./errors.ts";

/**
 * This is the browser's only way to reach the control plane, and it reaches it
 * indirectly: every call goes to this Console's own server entry point, which
 * holds the session and decides what may be forwarded. No control plane host,
 * token or header appears anywhere in this module.
 *
 * 这是浏览器抵达控制面的唯一途径，而且是间接抵达：每次调用都发往本 Console 自己的
 * 服务端入口，由它持有会话并决定什么可以被转发。本模块中不出现任何控制面的主机、
 * 令牌或请求头。
 */

/**
 * The two server entry points. They are distinct constants, and a request has
 * to name which one it wants, because they forward with different credentials
 * to different audiences: the admin base spends the signed-in tenant session,
 * the operator base spends the independently authenticated platform session.
 *
 * Defaulting to the admin base is safe in the direction that matters: a call
 * that forgets to say `surface` is sent as the tenant, and the operator
 * allowlist answers 404 to a fleet path arriving on the tenant entry point.
 * The opposite default would silently spend the operator token.
 *
 * 两个服务端入口。它们是两个不同的常量，且一次请求必须点名要哪一个，因为它们用不同的
 * 凭据转发给不同的受众：admin 入口花的是已登录的租户会话，operator 入口使用独立认证的平台会话。
 *
 * 默认取 admin 入口，在要紧的那个方向上是安全的：忘了写 `surface` 的调用会以租户身份
 * 发出，而租户入口上的白名单会对机群路径回 404。反过来做默认值，则会悄悄花掉运维 token。
 */
const ADMIN_BASE = "/api/admin";
const OPERATOR_BASE = "/api/operator";

/** SESSION_ENDPOINT is where sign-in and sign-out happen.
 *
 * SESSION_ENDPOINT 是登录与退出发生的地方。 */
const SESSION_ENDPOINT = "/api/session";

/** MAX_READ_ATTEMPTS bounds a read's total tries. Three is enough to ride out a
 * dropped connection or one restarting replica, and small enough that a user
 * waiting on a failing page is not held for long.
 *
 * MAX_READ_ATTEMPTS 限定一次读取的总尝试次数。三次足以熬过一次连接中断或一个正在
 * 重启的副本，又小到不会让等待一个失败页面的用户被拖住太久。 */
const MAX_READ_ATTEMPTS = 3;

/** RETRY_DELAYS_MS is the wait before each retry.
 *
 * RETRY_DELAYS_MS 是每次重试前的等待。 */
const RETRY_DELAYS_MS = [200, 600];

/** REQUEST_TIMEOUT_MS bounds one attempt.
 *
 * REQUEST_TIMEOUT_MS 限定单次尝试的时长。 */
const REQUEST_TIMEOUT_MS = 15_000;

/**
 * RequestSpec describes one Admin API call. `parse` is required rather than
 * optional: a response that reaches the UI has been checked against the Go
 * contract, and making that the only way in leaves no path that skips it.
 *
 * RequestSpec 描述一次 Admin API 调用。`parse` 是必填而非可选：抵达界面的响应都已
 * 对照 Go 契约校验过，而把它设为唯一入口，就不留任何绕过它的路径。
 */
export interface RequestSpec<T> {
  method: "GET" | "POST" | "PUT" | "DELETE";
  /** surface picks the entry point, and with it the credential the call is
   * forwarded with. It defaults to the tenant's Admin API.
   *
   * surface 选择入口，也就选定了这次调用用什么凭据转发。默认是租户的 Admin API。 */
  surface?: "admin" | "operator";
  /** path is an Admin API path such as "/admin/v1/users".
   *
   * path 是形如 "/admin/v1/users" 的 Admin API 路径。 */
  path: string;
  query?: Record<string, string>;
  body?: unknown;
  parse: (value: unknown) => T;
  signal?: AbortSignal;
}

/**
 * RequestDeps lets a test drive this module without a network or a clock. In
 * the browser both defaults are used.
 *
 * RequestDeps 让测试无需网络与真实时钟即可驱动本模块。在浏览器里两者都取默认值。
 */
export interface RequestDeps {
  fetchImpl?: typeof fetch;
  sleep?: (ms: number, signal?: AbortSignal) => Promise<void>;
}

/**
 * request performs one Admin API call and returns the parsed body.
 *
 * A read is retried on a failure that says nothing about whether the request
 * was applied — a dropped connection, a timeout, a 5xx. A write never is: the
 * Console cannot tell a lost response from a lost request, and repeating a key
 * creation to find out would mint a second credential. A write whose result is
 * unknown is reported as failed, and the page re-reads the list.
 *
 * request 执行一次 Admin API 调用并返回解析后的响应体。
 *
 * 读请求会在「无从判断请求是否已生效」的失败上重试——连接中断、超时、5xx。写请求绝不
 * 重试：Console 分不清是响应丢了还是请求丢了，而为了搞清楚去重发一次创建 key，会铸出
 * 第二个凭据。结果未知的写请求一律报告为失败，由页面重新读取列表。
 */
export async function request<T>(
  spec: RequestSpec<T>,
  deps: RequestDeps = {}
): Promise<T> {
  const doFetch = deps.fetchImpl ?? globalThis.fetch;
  const wait = deps.sleep ?? sleep;
  const attempts = spec.method === "GET" ? MAX_READ_ATTEMPTS : 1;

  let failure: ApiError = new ApiError("server");
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      return await attemptRequest(spec, doFetch);
    } catch (error) {
      failure = error instanceof ApiError ? error : new ApiError("server");
      const last = attempt === attempts - 1;
      if (last || !isRetryable(failure.kind)) {
        throw failure;
      }
      await wait(RETRY_DELAYS_MS[attempt] ?? 600, spec.signal);
    }
  }
  throw failure;
}

/**
 * signIn exchanges credentials for a session cookie and returns the signed-in
 * user. The password is in the body of one request and is never stored,
 * logged, or put into a URL.
 *
 * signIn 用凭据换取一个会话 cookie，并返回已登录的用户。密码只出现在这一个请求的
 * 请求体里，绝不被存储、记录日志或放进 URL。
 */
export async function signIn(
  email: string,
  password: string,
  deps: RequestDeps = {}
): Promise<SessionUserView> {
  const doFetch = deps.fetchImpl ?? globalThis.fetch;
  const response = await send(
    doFetch,
    SESSION_ENDPOINT,
    { method: "POST", body: { email, password } },
    undefined
  );
  if (!response.ok) {
    // A rejected login means invalid credentials, not an expired session.
    // 登录被拒绝表示凭据不正确，不表示已有会话过期。
    throw new ApiError(
      response.status === 401 ? "invalid_credentials" : kindForStatus(response.status),
      response.status
    );
  }
  return parseSessionUser(await readJson(response));
}

/**
 * signOut asks the Console server to revoke the ControlPlane session before
 * it clears the sealed cookie.
 *
 * signOut 要求 Console 服务端先吊销 ControlPlane 会话，再清理密封 Cookie。
 */
export async function signOut(deps: RequestDeps = {}): Promise<void> {
  const doFetch = deps.fetchImpl ?? globalThis.fetch;
  const response = await send(
    doFetch,
    SESSION_ENDPOINT,
    { method: "DELETE" },
    undefined
  );
  if (!response.ok && response.status !== 401) {
    throw new ApiError(kindForStatus(response.status), response.status);
  }
}

/**
 * SessionUserView is the identity the browser is given: what the navigation
 * and the permission hints need, and nothing else. The role in it decides
 * which controls render — never whether an operation is allowed, which only
 * the control plane decides.
 *
 * SessionUserView 是交给浏览器的身份：导航与权限提示所需要的部分，别无其他。其中的
 * 角色决定渲染哪些控件——绝不决定某个操作是否被允许，那只由控制面决定。
 */
export interface SessionUserView {
  id: string;
  tenantId: string;
  email: string;
  name: string;
  role: string;
}

/**
 * parseSessionUser validates the session endpoint's own response shape.
 *
 * parseSessionUser 校验 session 端点自身的响应形状。
 */
export function parseSessionUser(value: unknown): SessionUserView {
  if (typeof value !== "object" || value === null) {
    throw new ApiError("contract");
  }
  const user = (value as Record<string, unknown>).user;
  if (typeof user !== "object" || user === null) {
    throw new ApiError("contract");
  }
  const source = user as Record<string, unknown>;
  const fields = ["id", "tenantId", "email", "name", "role"] as const;
  if (fields.some((field) => typeof source[field] !== "string")) {
    throw new ApiError("contract");
  }
  return {
    id: source.id as string,
    tenantId: source.tenantId as string,
    email: source.email as string,
    name: source.name as string,
    role: source.role as string,
  };
}

/** attemptRequest performs a single try and maps its outcome onto ApiError.
 *
 * attemptRequest 执行单次尝试，并把它的结果映射成 ApiError。 */
async function attemptRequest<T>(
  spec: RequestSpec<T>,
  doFetch: typeof fetch
): Promise<T> {
  const query = spec.query ? new URLSearchParams(spec.query).toString() : "";
  const base = spec.surface === "operator" ? OPERATOR_BASE : ADMIN_BASE;
  const url = `${base}${spec.path}${query === "" ? "" : `?${query}`}`;
  const response = await send(
    doFetch,
    url,
    { method: spec.method, body: spec.body },
    spec.signal
  );

  if (response.status === 204) {
    return spec.parse(null);
  }
  if (!response.ok) {
    throw new ApiError(kindForStatus(response.status), response.status);
  }
  return spec.parse(await readJson(response));
}

/** send issues one fetch, classifying transport failures.
 *
 * send 发出一次 fetch，并对传输层失败做分类。 */
async function send(
  doFetch: typeof fetch,
  url: string,
  init: { method: string; body?: unknown },
  signal: AbortSignal | undefined
): Promise<Response> {
  const timeout = AbortSignal.timeout(REQUEST_TIMEOUT_MS);
  const combined = signal ? AbortSignal.any([signal, timeout]) : timeout;
  try {
    return await doFetch(url, {
      method: init.method,
      // Same-origin only, and never cached: an authenticated response must not
      // sit in a shared cache where the next session could be served it.
      //
      // 只走同源，且一律不缓存：已认证的响应不能留在共享缓存里，让下一个会话被喂到它。
      credentials: "same-origin",
      cache: "no-store",
      headers:
        init.body === undefined
          ? { Accept: "application/json" }
          : { Accept: "application/json", "Content-Type": "application/json" },
      body: init.body === undefined ? undefined : JSON.stringify(init.body),
      signal: combined,
    });
  } catch (error) {
    throw new ApiError(abortKind(error, signal, timeout));
  }
}

/** abortKind tells a caller's cancellation from a timeout from a dead network.
 *
 * abortKind 区分调用方的取消、超时与网络不通。 */
function abortKind(
  error: unknown,
  signal: AbortSignal | undefined,
  timeout: AbortSignal
): ApiErrorKind {
  if (signal?.aborted) {
    return "canceled";
  }
  if (timeout.aborted) {
    return "timeout";
  }
  if (error instanceof ApiError) {
    return error.kind;
  }
  return "network";
}

/** readJson parses a response body, treating unreadable content as a contract
 * violation rather than a transport failure.
 *
 * readJson 解析响应体，把无法读取的内容视为契约违例而不是传输失败。 */
async function readJson(response: Response): Promise<unknown> {
  try {
    return await response.json();
  } catch {
    throw new ApiError("contract", response.status);
  }
}

/** sleep waits, and gives up early when the caller cancels.
 *
 * sleep 等待，并在调用方取消时提前放弃。 */
function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true }
    );
  });
}

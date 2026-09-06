/**
 * ApiErrorKind is the classification every Console request failure collapses
 * into. The set is closed on purpose: the UI decides what to show from the
 * kind alone, so a new upstream message can never become new copy on screen.
 *
 * ApiErrorKind 是 Console 每一次请求失败最终归入的分类。这个集合是刻意封闭的：
 * 界面只根据 kind 决定展示什么，因此上游新增的报错文本永远不会变成屏幕上的新文案。
 */
export type ApiErrorKind =
  | "invalid_credentials"
  | "unauthorized"
  | "forbidden"
  | "not_found"
  | "conflict"
  | "invalid"
  | "server"
  | "network"
  | "timeout"
  | "canceled"
  | "contract";

/**
 * MESSAGES is the fixed copy shown for each kind. Upstream error text is never
 * rendered: it is written for operators, may name internals, and the repository
 * treats leaking it as a defect rather than a wording problem.
 *
 * MESSAGES 是每个 kind 对应的固定文案。上游的错误文本一律不渲染：那是写给运维看的，
 * 可能点出内部实现，而本仓库把泄漏它当作缺陷而非措辞问题。
 */
const MESSAGES: Record<ApiErrorKind, string> = {
  invalid_credentials: "邮箱或密码不正确，请检查后重试。",
  unauthorized: "登录状态已失效，请重新登录。",
  forbidden: "当前角色没有执行该操作的权限。",
  not_found: "目标不存在，或不属于当前租户。",
  conflict: "已存在同名或同一标识的记录。",
  invalid: "提交的内容不符合要求，请检查后重试。",
  server: "服务端处理失败，请稍后重试。",
  network: "无法连接服务端，请检查网络后重试。",
  timeout: "请求超时，请稍后重试。",
  canceled: "请求已取消。",
  contract: "服务端返回的数据格式无法识别。",
};

/**
 * ApiError is the only error type the Console request layer throws. It carries
 * the kind and, when a response was actually received, its status code.
 *
 * ApiError 是 Console 请求层唯一抛出的错误类型。它携带 kind，以及在确实收到响应时
 * 携带该响应的状态码。
 */
export class ApiError extends Error {
  readonly kind: ApiErrorKind;
  /**
   * status is the HTTP status, or 0 when the failure happened before a
   * response existed — a dropped connection, a timeout, an abort.
   *
   * status 是 HTTP 状态码；在响应尚不存在时失败（连接中断、超时、取消）则为 0。
   */
  readonly status: number;

  constructor(kind: ApiErrorKind, status = 0) {
    super(MESSAGES[kind]);
    this.name = "ApiError";
    this.kind = kind;
    this.status = status;
  }
}

/**
 * kindForStatus maps an HTTP status onto a kind. Anything unrecognized becomes
 * "server": an unexpected status is a server-side problem the user cannot act
 * on, and guessing a friendlier kind would tell them something untrue.
 *
 * kindForStatus 把 HTTP 状态码映射成 kind。无法识别的一律归为 "server"：意外的状态码
 * 是用户无法处置的服务端问题，猜一个更友好的 kind 只会告诉他们不实的信息。
 */
export function kindForStatus(status: number): ApiErrorKind {
  switch (status) {
    case 400:
      return "invalid";
    case 401:
      return "unauthorized";
    case 403:
      return "forbidden";
    case 404:
      return "not_found";
    case 409:
      return "conflict";
    default:
      return "server";
  }
}

/**
 * isRetryable reports whether a read may be attempted again. A write is never
 * retried on this signal alone — see requestJson in api-client.
 *
 * isRetryable 报告一次读取是否可以再试。仅凭这个信号绝不重试写请求
 * —— 见 api-client 中的 requestJson。
 */
export function isRetryable(kind: ApiErrorKind): boolean {
  return kind === "network" || kind === "timeout" || kind === "server";
}

/**
 * describe returns the message to show for any thrown value. A value that is
 * not an ApiError gets the generic server copy rather than its own text: a
 * stack trace or a bundler error string is not something to put on screen.
 *
 * describe 返回任意被抛出的值所对应的展示文案。非 ApiError 的值得到的是通用的服务端
 * 文案，而不是它自己的文本：调用栈或打包器的错误字符串不该出现在屏幕上。
 */
export function describe(error: unknown): string {
  return error instanceof ApiError ? error.message : MESSAGES.server;
}

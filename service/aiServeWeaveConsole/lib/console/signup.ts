import { MessageError } from "../i18n/problem.ts";
import { coreTranslator } from "../i18n/core.ts";
import { DEFAULT_LOCALE, type Locale } from "../i18n/locale.ts";
import type { ApiErrorKind } from "./errors.ts";

/**
 * This module holds the pure parts of self-service sign-up: the request shape
 * the Console's /api/signup route accepts, and how each answer on either side
 * of it becomes a status or an error kind. The control plane decides whether
 * a sign-up is valid; the checks here exist so a person sees a specific reason
 * before a round trip, not to replace that decision.
 *
 * 本模块承载自助注册中的纯逻辑部分：Console 的 /api/signup 路由接受的请求形状，以及
 * 它两侧的每种回答如何变成状态码或错误分类。注册是否有效由控制面决定；这里的检查是为了
 * 让人在一次往返之前就看到具体原因，而不是取代那个决定。
 */

/**
 * MIN_SIGNUP_PASSWORD_LENGTH mirrors the control plane's
 * logic.MinSignupPasswordLength, counted in characters (code points) the way
 * Go's utf8.RuneCountInString counts them. If the two drift apart, the
 * control plane's answer wins and the form shows the generic invalid message.
 *
 * MIN_SIGNUP_PASSWORD_LENGTH 与控制面的 logic.MinSignupPasswordLength 保持一致，
 * 按字符（码点）计数，与 Go 的 utf8.RuneCountInString 相同。两者若不一致，以控制面
 * 的回答为准，表单显示通用的「不符合要求」文案。
 */
export const MIN_SIGNUP_PASSWORD_LENGTH = 8;

/**
 * MAX_TENANT_NAME_LENGTH mirrors the control plane's tenant name column.
 *
 * MAX_TENANT_NAME_LENGTH 与控制面租户名列宽一致。
 */
export const MAX_TENANT_NAME_LENGTH = 128;

/**
 * SignupCredentials is the body the Console's sign-up route accepts.
 *
 * SignupCredentials 是 Console 注册路由接受的请求体。
 */
export interface SignupCredentials {
  tenantName: string;
  email: string;
  password: string;
}

/**
 * parseSignupBody validates the sign-up body's shape: three strings, none of
 * the identifying ones empty. Anything else is null.
 *
 * parseSignupBody 校验注册请求体的形状：三个字符串，其中用于标识的两个不能为空。
 * 其余情况一律返回 null。
 */
export function parseSignupBody(raw: string): SignupCredentials | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return null;
  }
  const { tenantName, email, password } = parsed as Record<string, unknown>;
  if (
    typeof tenantName !== "string" ||
    typeof email !== "string" ||
    typeof password !== "string"
  ) {
    return null;
  }
  if (tenantName.trim() === "" || email.trim() === "") {
    return null;
  }
  return { tenantName, email, password };
}

/**
 * SignupForm is what the sign-up page collects, confirmation included.
 *
 * SignupForm 是注册页收集的内容，包括确认密码。
 */
export interface SignupForm extends SignupCredentials {
  confirmPassword: string;
}

/**
 * localSignupProblem returns the first problem the page can name without
 * asking the control plane, or null when the form is worth submitting.
 *
 * localSignupProblem 返回页面无需询问控制面就能指出的第一个问题；表单值得提交时
 * 返回 null。
 */
export function localSignupProblem(form: SignupForm, locale: Locale = DEFAULT_LOCALE): string | null {
  const problem = signupProblem(form);
  return problem ? coreTranslator(locale)(problem.key, problem.values) : null;
}

/** signupProblem retains a fixed reason so open validation feedback can change language.
 * signupProblem 保留固定原因，使已显示的校验提示可随语言切换。 */
export function signupProblem(form: SignupForm): MessageError | null {
  const name = form.tenantName.trim();
  if (name === "") {
    return new MessageError("common.enterATenantName");
  }
  if ([...name].length > MAX_TENANT_NAME_LENGTH) {
    return new MessageError("common.tenantNameMustNotExceedCharacters", { MAX_TENANT_NAME_LENGTH: MAX_TENANT_NAME_LENGTH });
  }
  if (form.email.trim() === "") {
    return new MessageError("tenant.enterAnEmailAddress");
  }
  if ([...form.password].length < MIN_SIGNUP_PASSWORD_LENGTH) {
    return new MessageError("common.passwordMustContainAtLeastCharacters", { MIN_SIGNUP_PASSWORD_LENGTH: MIN_SIGNUP_PASSWORD_LENGTH });
  }
  if (form.password !== form.confirmPassword) {
    return new MessageError("common.thePasswordsDoNotMatch");
  }
  return null;
}

/**
 * UpstreamSignupDisposition is what the Console route answers for one
 * control plane sign-up status. `proceed` means the tenant exists and the
 * route goes on to sign the new owner in.
 *
 * UpstreamSignupDisposition 是 Console 路由针对控制面某个注册状态码给出的回答。
 * `proceed` 表示租户已创建，路由接着为新 owner 登录。
 */
export type UpstreamSignupDisposition =
  | { proceed: true }
  | { proceed: false; status: number; code: string };

/**
 * upstreamSignupDisposition maps the control plane's sign-up status onto the
 * Console route's own answer. Upstream text is never passed through; only
 * the statuses a person can act on keep their meaning, and everything else is
 * an upstream failure.
 *
 * upstreamSignupDisposition 把控制面的注册状态码映射成 Console 路由自己的回答。上游
 * 文本绝不透传；只有人能据此行动的那几种状态保留原意，其余一律视为上游故障。
 */
export function upstreamSignupDisposition(
  status: number
): UpstreamSignupDisposition {
  switch (status) {
    case 201:
      return { proceed: true };
    case 400:
      return { proceed: false, status: 400, code: "invalid_request" };
    case 404:
      return { proceed: false, status: 404, code: "signup_disabled" };
    case 409:
      return { proceed: false, status: 409, code: "email_taken" };
    case 429:
      return { proceed: false, status: 429, code: "rate_limited" };
    default:
      return { proceed: false, status: 502, code: "upstream" };
  }
}

/**
 * signupErrorKind maps the Console route's status onto the kind the page
 * shows. It differs from kindForStatus only where sign-up gives a status a
 * more specific meaning.
 *
 * signupErrorKind 把 Console 路由的状态码映射成页面展示用的分类。它与 kindForStatus
 * 的区别只在于注册赋予了某些状态码更具体的含义。
 */
export function signupErrorKind(
  status: number,
  fallback: (status: number) => ApiErrorKind
): ApiErrorKind {
  switch (status) {
    case 404:
      return "signup_disabled";
    case 409:
      return "email_taken";
    case 429:
      return "rate_limited";
    default:
      return fallback(status);
  }
}

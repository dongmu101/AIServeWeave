/**
 * entity.ts resolves the facts the public legal pages state about the operator:
 * who runs the service, where to reach them, which law governs, and how long a
 * credit purchase stays refundable. They are deployment facts, so they come
 * from the environment; an unset one renders as a bracketed placeholder that a
 * reader can see is unfinished, rather than as an invented value.
 *
 * entity.ts 解析公开法律页面关于营运方的事实：谁营运本服务、如何联系、适用哪里的法律、
 * 点数充值在多少天内可退款。它们属于部署事实，所以来自环境变量；未设置的项目以方括号
 * 占位符呈现，读者能一眼看出尚未完成，而不是一个编造出来的值。
 */

/** DEFAULT_SERVICE_NAME is the product name used when none is configured.
 *
 * DEFAULT_SERVICE_NAME 是未配置时使用的产品名称。 */
export const DEFAULT_SERVICE_NAME = "AIServeWeave";

/** DEFAULT_REFUND_WINDOW_DAYS mirrors the control plane's
 * Billing.RefundWindowDays default.
 *
 * DEFAULT_REFUND_WINDOW_DAYS 与控制面 Billing.RefundWindowDays 的默认值一致。 */
export const DEFAULT_REFUND_WINDOW_DAYS = 30;

/** MAX_REFUND_WINDOW_DAYS mirrors the control plane's upper bound.
 *
 * MAX_REFUND_WINDOW_DAYS 与控制面的上限一致。 */
export const MAX_REFUND_WINDOW_DAYS = 3650;

/** MAX_FIELD_LENGTH bounds one configured text so a stray value cannot flood a page.
 *
 * MAX_FIELD_LENGTH 限制单个配置文本的长度，避免误配的值撑爆页面。 */
const MAX_FIELD_LENGTH = 300;

/** LEGAL_ENV names each environment variable this module reads.
 *
 * LEGAL_ENV 列出本模块读取的每个环境变量。 */
export const LEGAL_ENV = {
  serviceName: "AISW_CONSOLE_LEGAL_SERVICE_NAME",
  company: "AISW_CONSOLE_LEGAL_COMPANY",
  address: "AISW_CONSOLE_LEGAL_ADDRESS",
  email: "AISW_CONSOLE_LEGAL_EMAIL",
  jurisdiction: "AISW_CONSOLE_LEGAL_JURISDICTION",
  refundWindowDays: "AISW_CONSOLE_LEGAL_REFUND_WINDOW_DAYS",
} as const;

/** PLACEHOLDERS are shown in place of each unset fact.
 *
 * PLACEHOLDERS 是各项事实未设置时显示的占位符。 */
export const PLACEHOLDERS = {
  company: "[COMPANY LEGAL NAME]",
  address: "[REGISTERED ADDRESS]",
  email: "[SUPPORT EMAIL]",
  jurisdiction: "[GOVERNING LAW JURISDICTION]",
} as const;

/**
 * LegalEntity is what the legal pages, the footer and the landing page say
 * about the operator. `email` is null when no valid address is configured, so
 * callers never build a mailto link to a placeholder; `emailText` is what to
 * print. `missing` lists the variables that fell back to a placeholder.
 *
 * LegalEntity 是法律页面、页脚与首页关于营运方的陈述。未配置有效地址时 `email` 为
 * null，调用方因此不会为占位符生成 mailto 链接；`emailText` 是要印出的文字。`missing`
 * 列出退回占位符的环境变量。
 */
export interface LegalEntity {
  serviceName: string;
  company: string;
  address: string;
  email: string | null;
  emailText: string;
  jurisdiction: string;
  /** refundWindowDays is 0 when tenants cannot ask for credit refunds.
   *
   * refundWindowDays 为 0 表示租户不能申请点数退款。 */
  refundWindowDays: number;
  missing: string[];
}

type Env = Readonly<Record<string, string | undefined>>;

/** EMAIL_PATTERN is a deliberately loose shape check: one @, no spaces, a dot in the domain.
 *
 * EMAIL_PATTERN 是刻意宽松的格式检查：一个 @、没有空白、域名含一个点。 */
const EMAIL_PATTERN = /^[^\s@<>"]+@[^\s@<>"]+\.[^\s@<>"]+$/;

/**
 * parseLegalEntity reads the operator facts from env. It never throws: a page
 * that states the operator must still render when a value is malformed, and
 * the malformed variable is reported through `missing` instead.
 *
 * parseLegalEntity 从 env 读取营运方事实。它从不抛错：值畸形时，陈述营运方的页面
 * 仍须能渲染，畸形的变量改由 `missing` 报告。
 */
export function parseLegalEntity(env: Env): LegalEntity {
  const missing: string[] = [];
  const text = (name: string, placeholder: string): string => {
    const value = clean(env[name]);
    if (value === null) {
      missing.push(name);
      return placeholder;
    }
    return value;
  };

  const serviceName = clean(env[LEGAL_ENV.serviceName]) ?? DEFAULT_SERVICE_NAME;
  const company = text(LEGAL_ENV.company, PLACEHOLDERS.company);
  const address = text(LEGAL_ENV.address, PLACEHOLDERS.address);
  const jurisdiction = text(LEGAL_ENV.jurisdiction, PLACEHOLDERS.jurisdiction);

  const rawEmail = clean(env[LEGAL_ENV.email]);
  const email = rawEmail !== null && EMAIL_PATTERN.test(rawEmail) ? rawEmail : null;
  if (email === null) missing.push(LEGAL_ENV.email);

  return {
    serviceName,
    company,
    address,
    email,
    emailText: email ?? PLACEHOLDERS.email,
    jurisdiction,
    refundWindowDays: refundWindow(env[LEGAL_ENV.refundWindowDays], missing),
    missing,
  };
}

/** clean trims a value, collapses inner whitespace and bounds its length; empty is null.
 *
 * clean 去除首尾空白、合并内部空白并限制长度；空值返回 null。 */
function clean(raw: string | undefined): string | null {
  const value = raw?.replace(/\s+/g, " ").trim();
  if (!value) return null;
  return value.length > MAX_FIELD_LENGTH ? value.slice(0, MAX_FIELD_LENGTH) : value;
}

/** refundWindow reads the window in days. Unset is the control plane default;
 * a value that is not an integer within the control plane's range is reported
 * and also falls back to the default.
 *
 * refundWindow 读取以天为单位的退款期限。未设置时采用控制面默认值；不是控制面范围内
 * 整数的值会被报告，同样退回默认值。 */
function refundWindow(raw: string | undefined, missing: string[]): number {
  const value = raw?.trim();
  if (!value) return DEFAULT_REFUND_WINDOW_DAYS;
  const days = Number(value);
  if (/^\d+$/.test(value) && days <= MAX_REFUND_WINDOW_DAYS) return days;
  missing.push(LEGAL_ENV.refundWindowDays);
  return DEFAULT_REFUND_WINDOW_DAYS;
}

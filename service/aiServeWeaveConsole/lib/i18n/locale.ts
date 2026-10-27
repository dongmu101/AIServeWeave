/** LOCALES is the closed set of display locales. / LOCALES 是封闭的界面语言集合。 */
export const LOCALES = ["zh-CN", "zh-TW", "en-US", "ja-JP", "ko-KR", "de-DE", "fr-FR", "es-ES"] as const;
/** Locale is a supported display preference. / Locale 是受支持的显示偏好。 */
export type Locale = typeof LOCALES[number];
/** DEFAULT_LOCALE is used when no preference matches. / DEFAULT_LOCALE 是无匹配偏好时的语言。 */
export const DEFAULT_LOCALE: Locale = "zh-CN";
/** LOCALE_COOKIE identifies the Console's preference, independently of auth. / LOCALE_COOKIE 独立标识控制台的语言偏好。 */
export const LOCALE_COOKIE = "aisw_console_locale";
/** LANGUAGE_NAMES are self-names so the picker is readable in any language. / LANGUAGE_NAMES 使用各语言自称，便于识别。 */
export const LANGUAGE_NAMES: Record<Locale, string> = { "zh-CN": "简体中文", "zh-TW": "繁體中文", "en-US": "English", "ja-JP": "日本語", "ko-KR": "한국어", "de-DE": "Deutsch", "fr-FR": "Français", "es-ES": "Español" };

/** parseLocale accepts canonical cookie values only. / parseLocale 只接受规范的 Cookie 值。 */
export function parseLocale(value: string): Locale | null {
  return (LOCALES as readonly string[]).includes(value) ? value as Locale : null;
}

function matchTag(tag: string): Locale | null {
  if (!/^[a-z]{2,3}(?:-[a-z0-9]{2,8})*$/i.test(tag)) return null;
  const parts = tag.toLowerCase().split("-");
  if (parts[0] === "zh") {
    if (parts.includes("hant")) return "zh-TW";
    if (parts.includes("hans")) return "zh-CN";
    return parts.some(p => ["tw", "hk", "mo"].includes(p)) ? "zh-TW" : "zh-CN";
  }
  return ({ en: "en-US", ja: "ja-JP", ko: "ko-KR", de: "de-DE", fr: "fr-FR", es: "es-ES" } as Record<string, Locale>)[parts[0]] ?? null;
}

/** resolveLocale negotiates a bounded header after validating a saved choice.
 * resolveLocale 先校验已保存选择，再在有界的浏览器语言头中匹配。 */
export function resolveLocale(saved: string | null, acceptLanguage: string): Locale {
  const preferred = saved === null ? null : parseLocale(saved);
  if (preferred) return preferred;
  let best: Locale = DEFAULT_LOCALE;
  let weight = 0;
  for (const item of acceptLanguage.slice(0, 4096).split(",").slice(0, 64)) {
    const [tag, ...params] = item.trim().split(";").map(p => p.trim());
    if (params.length > 1 || (params.length === 1 && !/^q=(?:0(?:\.\d{0,3})?|1(?:\.0{0,3})?)$/i.test(params[0]))) continue;
    const q = params.length === 0 ? 1 : Number(params[0].slice(2));
    const locale = matchTag(tag);
    if (locale && q > weight) { best = locale; weight = q; }
  }
  return best;
}

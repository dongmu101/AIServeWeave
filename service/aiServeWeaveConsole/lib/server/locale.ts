import "server-only";
import { cache } from "react";
import { cookies, headers } from "next/headers";
import { LOCALE_COOKIE, resolveLocale, type Locale } from "../i18n/locale";

/** readLocale negotiates display language once within the current request.
 * readLocale 在当前请求作用域内识别一次显示语言。 */
export const readLocale: () => Promise<Locale> = cache(async () => {
  const [jar, requestHeaders] = await Promise.all([cookies(), headers()]);
  return resolveLocale(jar.get(LOCALE_COOKIE)?.value ?? null, requestHeaders.get("accept-language") ?? "");
});

"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { LOCALE_COOKIE, type Locale } from "@/lib/i18n/locale";
import { loadMessages, type Messages } from "@/lib/i18n/messages";
import { createLocaleSelection } from "@/lib/i18n/selection";
import { createTranslator, type Translator } from "@/lib/i18n/translate";

type Context = { locale: Locale; messages: Messages; t: Translator; setLocale(locale: Locale): Promise<void>; changing: boolean };
const I18nContext = React.createContext<Context | null>(null);

/** I18nProvider keeps display preferences independent of sessions and form state.
 * I18nProvider 独立保存显示偏好，不改动会话或表单状态。 */
export function I18nProvider({ initialLocale, initialMessages, children }: {
  initialLocale: Locale; initialMessages: Messages; children: React.ReactNode;
}) {
  const router = useRouter();
  const [display, setDisplay] = React.useState({ locale: initialLocale, messages: initialMessages });
  const [changing, setChanging] = React.useState(false);
  const operation = React.useRef(0);
  const [selection] = React.useState(() => createLocaleSelection(initialLocale, loadMessages, next => {
    const secure = window.location.protocol === "https:" ? "; Secure" : "";
    document.cookie = `${LOCALE_COOKIE}=${next.locale}; Path=/; Max-Age=31536000; SameSite=Lax${secure}`;
    document.documentElement.lang = next.locale;
    const t = createTranslator(next.locale, next.messages);
    document.title = t("common.consoleTitle");
    document.querySelector('meta[name="description"]')?.setAttribute("content", t("common.consoleDescription"));
    setDisplay(next);
    router.refresh();
  }));
  const setLocale = React.useCallback(async (locale: Locale) => {
    const current = ++operation.current;
    setChanging(true);
    try { await selection.select(locale); }
    finally { if (current === operation.current) setChanging(false); }
  }, [selection]);
  const t = React.useMemo(() => createTranslator(display.locale, display.messages), [display]);
  const value = React.useMemo(() => ({ locale: display.locale, messages: display.messages, t, setLocale, changing }), [display.locale, display.messages, t, setLocale, changing]);
  return <I18nContext value={value}>{children}</I18nContext>;
}

/** useI18n reads the nearest request-initialized language context.
 * useI18n 读取最近的、由请求初始化的语言上下文。 */
export function useI18n(): Context {
  const value = React.useContext(I18nContext);
  if (!value) throw new Error("I18nProvider is required");
  return value;
}

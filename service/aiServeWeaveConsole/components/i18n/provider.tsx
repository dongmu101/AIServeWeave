"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { LOCALE_COOKIE, LOCALES, parseLocale, type Locale } from "@/lib/i18n/locale";
import { catalogue, type Messages } from "@/lib/i18n/messages";
import { createLocaleSelection } from "@/lib/i18n/selection";
import { createTranslator, type Translator } from "@/lib/i18n/translate";

type Context = { locale: Locale; messages: Messages; t: Translator; setLocale(locale: Locale): Promise<void>; changing: boolean };
const I18nContext = React.createContext<Context | null>(null);

// Start loading before the first render. On the server every catalogue is
// warm by the time a request arrives, so server rendering never waits and
// emits complete HTML. In the browser the page's own language starts
// downloading as soon as this module runs, ahead of hydration.
// 在首次渲染之前开始加载。服务端在请求到来时所有词典都已就绪，服务端渲染从不等待，输出完整
// 的 HTML。浏览器中，本模块一运行就开始下载页面自身语言的词典，早于水合。
if (typeof document === "undefined") {
  for (const locale of LOCALES) void catalogue(locale).catch(() => {});
} else {
  const locale = parseLocale(document.documentElement.lang);
  if (locale) void catalogue(locale).catch(() => {});
}

/** I18nProvider keeps display preferences independent of sessions and form state.
 * The catalogue is not a prop: passing it would inline the whole catalogue
 * (about 250 KB) into every HTML page. The browser loads it as a static,
 * long-cached chunk instead, while the server still renders translated HTML.
 *
 * I18nProvider 独立保存显示偏好，不改动会话或表单状态。词典不作为 prop 传入：那样会把整份
 * 词典（约 250 KB）内联进每个 HTML 页面。浏览器改为以可长期缓存的静态 chunk 加载它，服务端
 * 仍渲染出已翻译的 HTML。 */
export function I18nProvider({ initialLocale, children }: {
  initialLocale: Locale; children: React.ReactNode;
}) {
  const initialMessages = React.use(catalogue(initialLocale));
  const router = useRouter();
  const [display, setDisplay] = React.useState({ locale: initialLocale, messages: initialMessages });
  const [changing, setChanging] = React.useState(false);
  const operation = React.useRef(0);
  const [selection] = React.useState(() => createLocaleSelection(initialLocale, catalogue, next => {
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

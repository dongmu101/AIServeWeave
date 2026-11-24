import type source from "./messages/zh-CN/index.ts";
import type { Locale } from "./locale.ts";

/** MessageKey is the stable source-catalogue key set. / MessageKey 是源词典的稳定键集合。 */
export type MessageKey = keyof typeof source;
/** MessageEntry contains a whole phrase or its plural forms. / MessageEntry 表示完整句子或其复数形式。 */
export type MessageEntry = string | { readonly one?: string; readonly other: string };
/** Messages has exactly the source catalogue's keys. / Messages 包含与源词典一致的键。 */
export type Messages = Readonly<Record<MessageKey, MessageEntry>>;
const loaders: Record<Locale, () => Promise<Messages>> = {
  "zh-CN": () => import("./messages/zh-CN/index.ts").then(m => m.default),
  "zh-TW": () => import("./messages/zh-TW/index.ts").then(m => m.default),
  "en-US": () => import("./messages/en-US/index.ts").then(m => m.default),
  "ja-JP": () => import("./messages/ja-JP/index.ts").then(m => m.default),
  "ko-KR": () => import("./messages/ko-KR/index.ts").then(m => m.default),
  "de-DE": () => import("./messages/de-DE/index.ts").then(m => m.default),
  "fr-FR": () => import("./messages/fr-FR/index.ts").then(m => m.default),
  "es-ES": () => import("./messages/es-ES/index.ts").then(m => m.default),
};
/** loadMessages loads one allowlisted static catalogue. / loadMessages 加载白名单中的一份静态词典。 */
export async function loadMessages(locale: Locale): Promise<Messages> { return loaders[locale](); }

/**
 * createCatalogue returns a loader that hands out one shared promise per
 * locale, so React's use() sees the same thenable on every render and reads
 * it synchronously once it settled. A failed load is forgotten, so a later
 * call can retry.
 *
 * createCatalogue 返回一个加载函数，为每种语言交出同一个 promise，React 的 use() 每次渲染
 * 看到的都是同一个 thenable，兑现后即可同步读取。加载失败的 promise 会被丢弃，之后的调用
 * 可以重试。
 */
export function createCatalogue(load: (locale: Locale) => Promise<Messages>): (locale: Locale) => Promise<Messages> {
  const pending = new Map<Locale, Promise<Messages>>();
  return locale => {
    const known = pending.get(locale);
    if (known) return known;
    const created = load(locale);
    created.catch(() => { if (pending.get(locale) === created) pending.delete(locale); });
    pending.set(locale, created);
    return created;
  };
}

/** catalogue is the shared per-locale loader the I18nProvider reads: the
 * browser loads a hashed static chunk instead of receiving the catalogue
 * inline in every HTML page.
 * catalogue 是 I18nProvider 读取的共享加载函数：浏览器加载带哈希的静态 chunk，而不是在每个
 * HTML 页面里内联接收词典。 */
export const catalogue = createCatalogue(loadMessages);

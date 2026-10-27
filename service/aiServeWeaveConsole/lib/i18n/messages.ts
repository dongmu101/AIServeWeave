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

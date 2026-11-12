import type { Locale } from "../i18n/locale.ts";
import { enUS } from "./content/en-US.ts";
import { zhCN } from "./content/zh-CN.ts";
import { zhTW } from "./content/zh-TW.ts";
import type { LegalEntity } from "./entity.ts";
import type { LegalDocument, LegalKind, LegalSet } from "./types.ts";

/**
 * CONTENT is every language the legal text is written in. Other display
 * languages are shown the English text, which is authoritative anyway: a
 * machine-quality translation of a contract is worse than none.
 *
 * CONTENT 是法律正文实际撰写的语言。其他界面语言显示英文正文——英文本就是具权威性的版本，
 * 而一份机翻质量的合约译文比没有译文更糟。
 */
const CONTENT: Partial<Record<Locale, (entity: LegalEntity) => LegalSet>> = {
  "en-US": enUS,
  "zh-CN": zhCN,
  "zh-TW": zhTW,
};

/** CONTENT_LOCALES are the languages with their own legal text. / CONTENT_LOCALES 是有自己法律正文的语言。 */
export const CONTENT_LOCALES = Object.keys(CONTENT) as Locale[];

/** ResolvedLegalDocument is a document plus the language it is actually written in.
 *
 * ResolvedLegalDocument 是一份文件以及它实际撰写所用的语言。 */
export interface ResolvedLegalDocument {
  document: LegalDocument;
  contentLocale: Locale;
  /** fallback is true when the display language has no text of its own.
   *
   * fallback 为 true 表示该界面语言没有自己的正文。 */
  fallback: boolean;
}

/** legalDocument returns kind in locale, or in English when locale has no text.
 *
 * legalDocument 返回 locale 语言的 kind 文件；该语言没有正文时返回英文。 */
export function legalDocument(kind: LegalKind, locale: Locale, entity: LegalEntity): ResolvedLegalDocument {
  const own = CONTENT[locale];
  const contentLocale: Locale = own ? locale : "en-US";
  const build = own ?? enUS;
  return { document: build(entity)[kind], contentLocale, fallback: !own };
}

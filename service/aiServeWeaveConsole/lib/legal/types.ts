/**
 * types.ts is the shape every legal document takes, in every language.
 *
 * types.ts 定义每份法律文件在每种语言下的结构。
 */

/** LEGAL_KINDS are the public legal documents. / LEGAL_KINDS 是公开的法律文件。 */
export const LEGAL_KINDS = ["terms", "privacy", "refund"] as const;

/** LegalKind names one legal document. / LegalKind 指名一份法律文件。 */
export type LegalKind = typeof LEGAL_KINDS[number];

/** LegalBlock is a paragraph (string) or a bulleted list (string array).
 *
 * LegalBlock 是一段文字（字符串）或一个项目列表（字符串数组）。 */
export type LegalBlock = string | readonly string[];

/** LegalSection is one numbered heading and its blocks. / LegalSection 是一个编号标题及其内容块。 */
export interface LegalSection {
  heading: string;
  blocks: readonly LegalBlock[];
}

/** LegalDocument is a whole document in one language. / LegalDocument 是一种语言的完整文件。 */
export interface LegalDocument {
  title: string;
  intro: readonly LegalBlock[];
  sections: readonly LegalSection[];
}

/** LegalSet is all documents in one language. / LegalSet 是一种语言的全部文件。 */
export type LegalSet = Readonly<Record<LegalKind, LegalDocument>>;

/** LEGAL_UPDATED is the date the built-in text was last revised; change it with the text.
 *
 * LEGAL_UPDATED 是内置正文最后修订的日期，修改正文时一并更新。 */
export const LEGAL_UPDATED = "2026-11-12";

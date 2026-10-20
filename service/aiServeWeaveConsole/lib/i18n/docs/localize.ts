import type { ApiDocSummary, ApiDocDetail } from "../../console/api-docs.ts";
import type { Locale } from "../locale.ts";
import type { Messages } from "../messages.ts";
import { DOC_SOURCES, type DocSource } from "./source.ts";

const META = ["slug", "category", "categoryOrder", "sortOrder", "title", "method", "path", "summary"] as const;
function canonical(value: unknown): string {
  return JSON.stringify(value, function (_key, item: unknown) {
    return item && typeof item === "object" && !Array.isArray(item)
      ? Object.fromEntries(Object.entries(item).sort(([a], [b]) => a.localeCompare(b))) : item;
  });
}
function comparable(doc: ApiDocSummary, detail: boolean) {
  const meta = Object.fromEntries(META.map(key => [key, doc[key]]));
  return detail ? { ...meta, content: (doc as ApiDocDetail).content } : meta;
}
function project<T extends ApiDocSummary>(doc: T, source: DocSource, messages: Messages, detail: boolean): T {
  const next = structuredClone(doc);
  for (const field of source.fields) {
    if (!detail && field.path[0] === "content") continue;
    const value = messages[field.key];
    if (typeof value !== "string") throw new Error(`Missing literal documentation message: ${field.key}`);
    let parent: Record<string | number, unknown> = next as unknown as Record<string | number, unknown>;
    for (const part of field.path.slice(0, -1)) parent = parent[part] as Record<string | number, unknown>;
    parent[field.path.at(-1)!] = value;
  }
  return next;
}
/** localizeApiDocSummary translates metadata only when its original version matches.
 * localizeApiDocSummary 仅在元数据原文版本匹配时翻译元数据。 */
export function localizeApiDocSummary<T extends ApiDocSummary>(doc: T, locale: Locale, messages: Messages): T {
  if (locale === "zh-CN") return doc;
  const source = DOC_SOURCES.find(item => item.doc.slug === doc.slug);
  if (!source || canonical(comparable(doc, false)) !== canonical(comparable(source.doc, false))) return doc;
  return project(doc, source, messages, false);
}
/** localizeApiDoc returns a translated display projection of an exact built-in original.
 * localizeApiDoc 返回完整内置原文的翻译展示投影。 */
export function localizeApiDoc<T extends ApiDocDetail>(doc: T, locale: Locale, messages: Messages): T {
  if (locale === "zh-CN") return doc;
  const source = DOC_SOURCES.find(item => item.doc.slug === doc.slug);
  if (!source || canonical(comparable(doc, true)) !== canonical(comparable(source.doc, true))) return doc;
  return project(doc, source, messages, true);
}

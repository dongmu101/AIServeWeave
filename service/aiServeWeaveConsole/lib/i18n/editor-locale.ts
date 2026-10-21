import type {Locale} from "./locale.ts";

const bundles = {"zh-CN":"zh", "zh-TW":"zh-TW", "en-US":"en", "ja-JP":"ja", "ko-KR":"ko", "de-DE":"de", "fr-FR":"fr", "es-ES":"es"} as const satisfies Record<Locale,string>;

/** editorLocale maps a display preference to an exact shipped editor bundle.
 * editorLocale 将界面偏好映射到编辑器实际提供的规范语言包。 */
export function editorLocale(locale: Locale): string { return bundles[locale]; }

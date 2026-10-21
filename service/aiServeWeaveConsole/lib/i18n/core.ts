import zh from "./messages/zh-CN/core.ts";
import tw from "./messages/zh-TW/core.ts";
import en from "./messages/en-US/core.ts";
import ja from "./messages/ja-JP/core.ts";
import ko from "./messages/ko-KR/core.ts";
import de from "./messages/de-DE/core.ts";
import fr from "./messages/fr-FR/core.ts";
import es from "./messages/es-ES/core.ts";
import { createTranslator, type Translator } from "./translate.ts";
import { DEFAULT_LOCALE, type Locale } from "./locale.ts";

const catalogues = { "zh-CN": zh, "zh-TW": tw, "en-US": en, "ja-JP": ja, "ko-KR": ko, "de-DE": de, "fr-FR": fr, "es-ES": es };

/** coreTranslator binds the small synchronous validator/display catalogue.
 * coreTranslator 绑定供纯校验和显示辅助函数使用的小型同步词典。 */
export function coreTranslator(locale: Locale = DEFAULT_LOCALE): Translator {
  return createTranslator(locale, catalogues[locale]);
}

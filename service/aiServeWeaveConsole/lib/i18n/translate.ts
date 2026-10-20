import type { Locale } from "./locale.ts";
import type { MessageKey, Messages } from "./messages.ts";

/** MessageValues supplies named phrase parameters. / MessageValues 提供句子的具名参数。 */
export type MessageValues = Readonly<Record<string, string | number>>;
/** Translator renders whole phrases for one immutable locale. / Translator 为一个固定语言渲染完整句子。 */
export type Translator = (key: MessageKey, values?: MessageValues) => string;

/** createTranslator binds a catalogue without global active-language state.
 * createTranslator 绑定词典，不使用全局的当前语言状态。 */
export function createTranslator(locale: Locale, messages: Messages, options: { strict?: boolean } = {}): Translator {
  const strict = options.strict ?? process.env.NODE_ENV !== "production";
  const plural = new Intl.PluralRules(locale);
  const unavailable = String(messages["common.unavailable"]);
  return (key, values = {}) => {
    if (!Object.hasOwn(messages, key)) {
      if (strict) throw new Error(`Unknown translation key: ${key}`);
      return unavailable;
    }
    const entry = messages[key];
    let phrase: string;
    if (typeof entry === "string") phrase = entry;
    else {
      if (typeof values.count !== "number" || !Number.isFinite(values.count)) {
        if (strict) throw new Error(`Missing numeric count for: ${key}`);
        return unavailable;
      }
      phrase = plural.select(values.count) === "one" ? entry.one ?? entry.other : entry.other;
    }
    let missing = false;
    const rendered = phrase.replace(/(?<!\{)\{([A-Za-z]\w*)\}(?!\})/g, (_, name: string) => {
      if (!Object.hasOwn(values, name)) {
        if (strict) throw new Error(`Missing translation parameter ${name} for: ${key}`);
        missing = true;
        return "";
      }
      return String(values[name]);
    });
    return missing ? unavailable : rendered;
  };
}

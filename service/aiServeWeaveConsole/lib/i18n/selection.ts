import { parseLocale, type Locale } from "./locale.ts";
import type { loadMessages, Messages } from "./messages.ts";

/** createLocaleSelection commits only a successfully loaded, current selection.
 * createLocaleSelection 仅提交成功加载且仍为最新选择的语言。 */
export function createLocaleSelection(
  initial: Locale,
  load: typeof loadMessages,
  commit: (next: { locale: Locale; messages: Messages }) => void,
): { select(locale: Locale): Promise<void> } {
  let active = initial;
  let generation = 0;
  return {
    async select(locale) {
      if (!parseLocale(locale)) throw new Error("Unsupported locale");
      const request = ++generation;
      if (locale === active) return;
      try {
        const messages = await load(locale);
        if (request !== generation) return;
        commit({ locale, messages });
        active = locale;
      } catch (failure) {
        if (request === generation) throw failure;
      }
    },
  };
}

"use client";

import { describe } from "@/lib/console/errors";
import { MessageError } from "@/lib/i18n/problem";
import { useI18n } from "./provider";

/** ErrorMessage translates a retained cause whenever the display language changes.
 * ErrorMessage 在显示语言变化时重新翻译保留的错误原因。 */
export function ErrorMessage({ error, describeError = describe }: {
  error: unknown; describeError?: (error: unknown, locale: ReturnType<typeof useI18n>["locale"]) => string;
}) {
  const { locale, t } = useI18n();
  if (error instanceof MessageError) {
    return t(error.key, { ...(error.failure === undefined ? {} : { value: describe(error.failure, locale) }), ...error.values });
  }
  return describeError(error, locale);
}

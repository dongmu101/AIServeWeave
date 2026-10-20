"use client";

import * as React from "react";
import { LanguagesIcon } from "lucide-react";
import { LANGUAGE_NAMES, LOCALES, parseLocale } from "@/lib/i18n/locale";
import { useI18n } from "./provider";

/** LanguageSwitcher uses self-names and leaves pending forms mounted.
 * LanguageSwitcher 用语言自称显示选项，并保留正在编辑的表单。 */
export function LanguageSwitcher() {
  const { locale, t, setLocale, changing } = useI18n();
  const [failure, setFailure] = React.useState(false);
  const generation = React.useRef(0);
  return <div className="flex max-w-full flex-col gap-1">
    <label className="inline-flex items-center gap-1.5 text-sm">
      <LanguagesIcon aria-hidden="true" className="size-4 shrink-0" />
      <span className="sr-only">{t("common.language")}</span>
      <select data-language-switcher value={locale} aria-busy={changing}
        className="h-8 max-w-36 rounded-md border border-input bg-background px-2 text-sm"
        onChange={async event => {
          const next = parseLocale(event.target.value);
          if (!next) return;
          const current = ++generation.current;
          setFailure(false);
          try { await setLocale(next); }
          catch { if (current === generation.current) setFailure(true); }
        }}>
        {LOCALES.map(value => <option key={value} value={value}>{LANGUAGE_NAMES[value]}</option>)}
      </select>
    </label>
    {failure ? <span role="alert" className="max-w-48 text-xs text-destructive">{t("common.languageLoadFailed")}</span> : null}
  </div>;
}

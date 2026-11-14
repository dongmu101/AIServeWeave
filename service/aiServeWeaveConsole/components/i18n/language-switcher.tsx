"use client";

import * as React from "react";
import { LanguagesIcon } from "lucide-react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { LANGUAGE_NAMES, LOCALES, parseLocale } from "@/lib/i18n/locale";
import { useI18n } from "./provider";

/** LanguageSwitcher uses self-names and leaves pending forms mounted; it is a themed popup rather than a native select so the highlight follows the theme palette instead of the OS accent color.
 * LanguageSwitcher 用语言自称显示选项，并保留正在编辑的表单；使用主题化弹层而非原生 select，使高亮跟随主题色而非系统强调色。 */
export function LanguageSwitcher() {
  const { locale, t, setLocale, changing } = useI18n();
  const [failure, setFailure] = React.useState(false);
  const generation = React.useRef(0);
  return <div className="flex max-w-full flex-col gap-1">
    <Select value={locale} onValueChange={async value => {
      const next = typeof value === "string" ? parseLocale(value) : null;
      if (!next || next === locale) return;
      const current = ++generation.current;
      setFailure(false);
      try { await setLocale(next); }
      catch { if (current === generation.current) setFailure(true); }
    }}>
      <SelectTrigger data-language-switcher data-value={locale} aria-label={t("common.language")} aria-busy={changing} className="max-w-40">
        <LanguagesIcon aria-hidden="true" className="text-muted-foreground" />
        <SelectValue>{LANGUAGE_NAMES[locale]}</SelectValue>
      </SelectTrigger>
      <SelectContent align="end" alignItemWithTrigger={false}>
        {LOCALES.map(value => <SelectItem key={value} value={value} lang={value}>{LANGUAGE_NAMES[value]}</SelectItem>)}
      </SelectContent>
    </Select>
    {failure ? <span role="alert" className="max-w-48 text-xs text-destructive">{t("common.languageLoadFailed")}</span> : null}
  </div>;
}

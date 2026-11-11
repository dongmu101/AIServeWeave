"use client";

import * as React from "react";
import { useI18n } from "@/components/i18n/provider";
import { currencyOptions } from "@/lib/console/currencies";

/** currencyName returns the currency's name in locale, or "" when the
 * runtime does not know it.
 *
 * currencyName 返回该币种在 locale 下的名称；运行时不认识时返回 ""。 */
function currencyName(names: Intl.DisplayNames | null, code: string): string {
  try {
    const name = names?.of(code) ?? "";
    return name === code ? "" : name;
  } catch {
    return "";
  }
}

/** CurrencySelect picks a price's currency from the provider's list, each
 * option its code and, where the browser knows it, its local name.
 *
 * CurrencySelect 从服务商清单中选择标价币种；每个选项显示代码，浏览器认识时附上本地名称。 */
export function CurrencySelect({ id, value, onChange, disabled, required, className }: {
  id: string; value: string; onChange: (code: string) => void; disabled?: boolean; required?: boolean; className?: string;
}) {
  const { locale } = useI18n();
  const names = React.useMemo(() => {
    try {
      return new Intl.DisplayNames([locale], { type: "currency" });
    } catch {
      return null;
    }
  }, [locale]);
  return <select id={id} className={className} value={value} required={required} disabled={disabled} onChange={event => onChange(event.target.value)}>
    {value === "" ? <option value="">—</option> : null}
    {currencyOptions(value).map(code => {
      const name = currencyName(names, code);
      return <option key={code} value={code}>{name ? `${code} · ${name}` : code}</option>;
    })}
  </select>;
}

import type { Locale } from "./locale.ts";

/** formatNumber formats a value without changing its unit. / formatNumber 格式化数值但不改变单位。 */
export function formatNumber(value: number, locale: Locale, options?: Intl.NumberFormatOptions): string {
  return new Intl.NumberFormat(locale, options).format(value);
}
/** formatPercent formats a ratio where one means 100%. / formatPercent 格式化比率，1 表示 100%。 */
export function formatPercent(ratio: number, locale: Locale): string {
  return formatNumber(ratio, locale, { style: "percent", maximumFractionDigits: 2 });
}
/** formatBytes uses IEC units and locale-specific numbers. / formatBytes 使用 IEC 单位和对应语言的数字格式。 */
export function formatBytes(bytes: number, locale: Locale): string {
  if (!Number.isFinite(bytes)) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  const index = bytes > 0 ? Math.max(0, Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)))) : 0;
  return `${formatNumber(bytes / 1024 ** index, locale, { maximumFractionDigits: 2 })} ${units[index]}`;
}
/** formatDuration formats milliseconds as seconds. / formatDuration 将毫秒按秒显示。 */
export function formatDuration(milliseconds: number, locale: Locale): string {
  return new Intl.NumberFormat(locale, { style: "unit", unit: "second", maximumFractionDigits: 2 }).format(milliseconds / 1000);
}
/** formatRelativeTime formats an already calculated relative amount. / formatRelativeTime 格式化已计算的相对时长。 */
export function formatRelativeTime(value: number, unit: Intl.RelativeTimeFormatUnit, locale: Locale): string {
  return new Intl.RelativeTimeFormat(locale, { numeric: "auto" }).format(value, unit);
}

"use client";

import { useI18n } from "@/components/i18n/provider";
import type { Translator } from "@/lib/i18n/translate";
import type { CliUsage, CliUsageWindow } from "@/lib/console/fleet";
import { formatDateTime } from "@/lib/console/format";

const WINDOW_NAMES: Record<string, Parameters<Translator>[0]> = {
  five_hour: "tenant.cliQuotaFiveHour", seven_day: "tenant.cliQuotaSevenDay",
  seven_day_sonnet: "tenant.cliQuotaSevenDaySonnet", seven_day_opus: "tenant.cliQuotaSevenDayOpus",
  primary: "tenant.cliQuotaPrimary", secondary: "tenant.cliQuotaSecondary",
};
const STATUS_TEXT: Record<string, Parameters<Translator>[0]> = {
  pending: "tenant.cliQuotaPending", unauthorized: "tenant.cliQuotaUnauthorized",
  unavailable: "tenant.cliQuotaUnavailable", error: "tenant.cliQuotaError",
};

/** CliQuota shows the subscription quota of the CLI a service runs: used and
 * remaining percentages per window, when each resets, and when it was checked.
 * A service without a reported quota shows a dash, never a zero.
 * CliQuota 展示服务所运行 CLI 的订阅额度：每个窗口的已用与剩余比例、重置时间与查询时间。
 * 没有上报额度的服务显示破折号，绝不显示零。 */
export function CliQuota({ usage, now }: { usage: CliUsage | null; now: number }) {
  const { locale, t } = useI18n();
  if (!usage) return <span className="text-xs text-muted-foreground">—</span>;
  if (usage.status !== "ready" || usage.windows.length === 0) {
    const key = STATUS_TEXT[usage.status] ?? "tenant.cliQuotaError";
    return <span className="text-xs text-muted-foreground">{t(key)}</span>;
  }
  const percent = (value: number) => `${value.toLocaleString(locale, { maximumFractionDigits: 1 })}%`;
  const render = (window: CliUsageWindow) => {
    const expired = window.resetsAt !== null && Date.parse(window.resetsAt) <= now;
    const key = WINDOW_NAMES[window.name];
    return <li key={`${window.bucket}/${window.name}`} className="grid gap-0.5">
      <span className="font-medium">{key ? t(key) : window.name}{window.bucket ? ` · ${window.bucket}` : ""}</span>
      {window.durationMinutes > 0 ? <span className="text-muted-foreground">{t("tenant.cliQuotaMinutesWindow", { minutes: window.durationMinutes.toLocaleString(locale) })}</span> : null}
      {expired ? <span className="text-muted-foreground">{t("tenant.cliQuotaWaitingNextCheck")}</span> :
        <span>{t("tenant.cliQuotaUsedRemaining", { used: percent(window.usedPercent), remaining: percent(Math.max(0, 100 - window.usedPercent)) })}</span>}
      {window.resetsAt ? <span className="text-muted-foreground">{t("tenant.cliQuotaResets", { time: formatDateTime(window.resetsAt, undefined, locale) })}</span> : null}
    </li>;
  };
  return <div className="grid min-w-40 gap-2 whitespace-normal text-xs">
    <ul className="grid gap-2">{usage.windows.map(render)}</ul>
    {usage.checkedAt ? <span className="text-muted-foreground">{t("tenant.cliQuotaChecked", { time: formatDateTime(usage.checkedAt, undefined, locale) })}</span> : null}
  </div>;
}

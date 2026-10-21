"use client";

import { useSyncExternalStore } from "react";
import { useI18n } from "./provider";

const subscribe = () => () => {};
const clientSnapshot = () => true;
const serverSnapshot = () => false;

/** LocalDateTime renders in the viewer's time zone after a deterministic first paint.
 * LocalDateTime 在确定的首屏占位后，按浏览者的时区渲染时间。 */
export function LocalDateTime({ value }: { value: string }) {
  const { locale } = useI18n();
  const client = useSyncExternalStore(subscribe, clientSnapshot, serverSnapshot);
  return client ? new Date(value).toLocaleString(locale) : "—";
}

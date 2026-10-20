"use client";

import * as React from "react";
import type { MessageKey } from "@/lib/i18n/messages";
import type { MessageValues } from "@/lib/i18n/translate";
import { useI18n } from "./provider";

/** Message renders named React slots as text or elements without HTML injection.
 * Message 将具名插槽渲染为文本或元素，不注入 HTML。 */
export function Message({ id, values = {} }: { id: MessageKey; values?: MessageValues | Readonly<Record<string, React.ReactNode>> }) {
  const { t } = useI18n();
  const plain: Record<string, string | number> = {};
  const nodes = new Map<string, React.ReactNode>();
  let index = 0;
  for (const [name, value] of Object.entries(values)) {
    if (typeof value === "string" || typeof value === "number") plain[name] = value;
    else {
      const marker = `\uE000${index++}\uE001`;
      plain[name] = marker;
      nodes.set(marker, value);
    }
  }
  return <>{t(id, plain).split(/(\uE000\d+\uE001)/g).map((part, position) =>
    <React.Fragment key={position}>{nodes.has(part) ? nodes.get(part) : part}</React.Fragment>)}</>;
}

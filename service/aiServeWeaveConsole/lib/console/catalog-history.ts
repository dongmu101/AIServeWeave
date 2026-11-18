/**
 * Catalog review history is read from the platform audit log: the control
 * plane records every approval, rejection and automatic application there,
 * and keeps no separate table of decisions.
 *
 * 价目表审核记录读自平台审计日志：控制面把每次批准、拒绝与自动生效都记在那里，没有
 * 单独的决定表。
 */

/** CATALOG_HISTORY_ACTIONS are the audit actions of catalog decisions. / CATALOG_HISTORY_ACTIONS 是价目表决定的审计动作。 */
export const CATALOG_HISTORY_ACTIONS = ["billing.catalog.approve", "billing.catalog.reject", "billing.catalog.auto_apply"] as const;

export type CatalogHistoryAction = (typeof CATALOG_HISTORY_ACTIONS)[number];

/** CatalogHistoryDetail is what an audit detail says about one decision. / CatalogHistoryDetail 是审计详情中关于一次决定的内容。 */
export interface CatalogHistoryDetail {
  /** kind is "change" or "remove"; "" when the detail does not say. / kind 为 "change" 或 "remove"；详情未写时为 ""。 */
  kind: string;
  /** Prices are in micro-credits per million tokens; null when absent or "-". / 单价以微点 / 百万 token 计；缺失或为 "-" 时为 null。 */
  inputBefore: number | null;
  inputAfter: number | null;
  outputBefore: number | null;
  outputAfter: number | null;
  /** maxChange is the auto-applied change as written, e.g. "12.50%". / maxChange 是自动生效的变动幅度原文，例如 "12.50%"。 */
  maxChange: string;
}

function price(value: string | undefined): number | null {
  if (value === undefined || !/^-?\d+$/.test(value)) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : null;
}

/**
 * parseCatalogHistoryDetail reads the space-separated key=value detail the
 * control plane writes (logic/pricecatalog.go). Unknown or malformed parts are
 * ignored, so a changed format degrades to blanks rather than failing.
 *
 * parseCatalogHistoryDetail 读取控制面写入的以空格分隔的 key=value 详情
 * （logic/pricecatalog.go）。未知或格式不对的部分被忽略，格式变化时只会显示为空，而不是报错。
 */
export function parseCatalogHistoryDetail(detail: string): CatalogHistoryDetail {
  const fields = new Map<string, string>();
  for (const part of detail.split(" ")) {
    const at = part.indexOf("=");
    if (at > 0) fields.set(part.slice(0, at), part.slice(at + 1));
  }
  const pair = (key: string): [number | null, number | null] => {
    const [before, after] = (fields.get(key) ?? "").split("→");
    return [price(before), price(after)];
  };
  const [inputBefore, inputAfter] = pair("input");
  const [outputBefore, outputAfter] = pair("output");
  return { kind: fields.get("kind") ?? "", inputBefore, inputAfter, outputBefore, outputAfter, maxChange: fields.get("max_change") ?? "" };
}

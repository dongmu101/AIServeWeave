import { ApiError } from "./errors.ts";
import { integer, list, page, record, required, text, timestamp } from "./billing.ts";
import type { Page } from "./contract.ts";

/**
 * This module mirrors GET /admin/v1/usage/records
 * (internal/types.UsageRecordListResponse): one row per request, newest
 * first, with its meter breakdown and, when billing is on, how it was
 * priced. See .scratch/token-pricing/spec.md「单笔计价」.
 *
 * 本模块镜像 GET /admin/v1/usage/records（internal/types.UsageRecordListResponse）：
 * 每个请求一行，新的在前，附带计量项细分，开启计费时另附计价过程。见
 * .scratch/token-pricing/spec.md「单笔计价」。
 */

/** USAGE_RECORDS_PAGE_SIZE is how many records one page asks for; the server
 * caps a page at 200.
 *
 * USAGE_RECORDS_PAGE_SIZE 是每页请求的记录数；服务端每页最多 200 条。 */
export const USAGE_RECORDS_PAGE_SIZE = 50;

/** UsageMeterLine is one meter of a record: `quantity` is the meter's count,
 * `billed` that less its children's (what its own price applies to), and the
 * price and cost are null with billing off.
 *
 * UsageMeterLine 是记录中的一个计量项：`quantity` 是该计量项的数量，`billed` 是减去
 * 子项之后的部分（适用其自身单价的部分）；计费关闭时单价与费用为 null。 */
export interface UsageMeterLine {
  meter: string;
  quantity: number;
  billed: number;
  priceMicroPerM: number | null;
  costMicro: number | null;
}

/** UsageRecord is one request. The pricing fields are null with billing off;
 * `priceSource` is "" for a record written before pricing existed.
 *
 * UsageRecord 是一个请求。计费关闭时计价字段为 null；计价出现之前写入的记录
 * `priceSource` 为 ""。 */
export interface UsageRecord {
  requestId: string;
  createdAt: string;
  model: string;
  endpoint: string;
  keyId: string;
  userId: string;
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  meters: UsageMeterLine[];
  adjusted: boolean;
  costMicro: number | null;
  baseCostMicro: number | null;
  ratePpm: number | null;
  priceSource: string;
  charged: boolean | null;
  tier: number;
  ownNode: boolean;
  unpriced: boolean;
}

function fail(): never {
  throw new ApiError("contract");
}

/** optionalText reads an `omitempty` string field; absent is "".
 * optionalText 读取一个 `omitempty` 的字符串字段；缺席为 ""。 */
function optionalText(source: Record<string, unknown>, key: string): string {
  return source[key] === undefined || source[key] === null ? "" : text(source, key);
}

function optionalInteger(source: Record<string, unknown>, key: string): number | null {
  const value = source[key];
  return value === undefined || value === null ? null : integer(source, key);
}

function optionalFlag(source: Record<string, unknown>, key: string): boolean | null {
  const value = source[key];
  if (value === undefined || value === null) return null;
  if (typeof value !== "boolean") fail();
  return value;
}

function parseMeterLine(value: unknown): UsageMeterLine {
  const source = record(value);
  return {
    meter: required(source, "meter"),
    quantity: integer(source, "quantity"),
    billed: integer(source, "billed"),
    priceMicroPerM: optionalInteger(source, "price_micro_per_m"),
    costMicro: optionalInteger(source, "cost_micro"),
  };
}

/** parseUsageRecord validates one types.UsageRecordResponse.
 * parseUsageRecord 校验一个 types.UsageRecordResponse。 */
export function parseUsageRecord(value: unknown): UsageRecord {
  const source = record(value);
  return {
    requestId: required(source, "request_id"),
    createdAt: timestamp(source, "created_at"),
    model: required(source, "model"),
    endpoint: text(source, "endpoint"),
    keyId: optionalText(source, "key_id"),
    userId: optionalText(source, "user_id"),
    promptTokens: integer(source, "prompt_tokens"),
    completionTokens: integer(source, "completion_tokens"),
    totalTokens: integer(source, "total_tokens"),
    cacheReadTokens: optionalInteger(source, "cache_read_tokens") ?? 0,
    cacheWriteTokens: optionalInteger(source, "cache_write_tokens") ?? 0,
    meters: list(source.meters, parseMeterLine),
    adjusted: optionalFlag(source, "adjusted") ?? false,
    costMicro: optionalInteger(source, "cost_micro"),
    baseCostMicro: optionalInteger(source, "base_cost_micro"),
    ratePpm: optionalInteger(source, "rate_ppm"),
    priceSource: optionalText(source, "price_source"),
    charged: optionalFlag(source, "charged"),
    tier: optionalInteger(source, "tier") ?? 0,
    ownNode: optionalFlag(source, "own_node") ?? false,
    unpriced: optionalFlag(source, "unpriced") ?? false,
  };
}

/** parseUsageRecordPage validates one page of records.
 * parseUsageRecordPage 校验一页记录。 */
export function parseUsageRecordPage(value: unknown): Page<UsageRecord> {
  return page(value, parseUsageRecord);
}

/** RecordPricing is how a record's pricing reads for the tenant: `off` with
 * billing off, `pre_upgrade` for a record written before pricing, `legacy`
 * for a cost backfilled from the old per-model prices, `own_node` for a
 * request the tenant's own node served (priced, not charged), `unpriced`
 * when nothing priced it, and `priced` otherwise.
 *
 * RecordPricing 是一条记录的计价在租户看来的样子：计费关闭为 `off`；计价出现之前写入的
 * 记录为 `pre_upgrade`；从旧的按模型单价回填的费用为 `legacy`；由租户自有节点服务的
 * 请求为 `own_node`（照常计价但不扣费）；没有任何单价时为 `unpriced`；其余为 `priced`。 */
export type RecordPricing = "off" | "pre_upgrade" | "legacy" | "own_node" | "unpriced" | "priced";

/** recordPricing classifies a record for display.
 * recordPricing 为展示而给一条记录分类。 */
export function recordPricing(r: Pick<UsageRecord, "costMicro" | "priceSource" | "ownNode" | "unpriced">): RecordPricing {
  if (r.costMicro === null) return "off";
  if (r.priceSource === "") return "pre_upgrade";
  if (r.priceSource === "legacy") return "legacy";
  if (r.ownNode) return "own_node";
  if (r.priceSource === "none" || r.unpriced) return "unpriced";
  return "priced";
}

/** formatRate writes a rate in parts per million as a multiplier, such as
 * "1.2×"; at most six decimals, which is the rate's own precision.
 *
 * formatRate 把以百万分之一为单位的倍率写成乘数，例如「1.2×」；最多六位小数，即倍率
 * 自身的精度。 */
export function formatRate(ppm: number, locale: string): string {
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 6 }).format(ppm / 1_000_000)}×`;
}

/** KNOWN_METERS are the meters this release names, in display order.
 * KNOWN_METERS 是本版本有名称的计量项，按展示顺序排列。 */
export const KNOWN_METERS = [
  "input",
  "input.cache_read",
  "input.cache_write",
  "input.cache_write.1h",
  "input.cache_write.12h",
  "output",
  "output.reasoning",
] as const;
export type KnownMeter = (typeof KNOWN_METERS)[number];

/** isKnownMeter reports whether a meter has a name in the UI; any other is
 * shown as its raw name.
 *
 * isKnownMeter 报告计量项在界面上是否有名称；其他计量项显示原始名称。 */
export function isKnownMeter(meter: string): meter is KnownMeter {
  return (KNOWN_METERS as readonly string[]).includes(meter);
}

/** compareMeters orders meters: known ones in KNOWN_METERS order, under
 * their root, then the rest by name.
 *
 * compareMeters 为计量项排序：有名称的按 KNOWN_METERS 顺序，其余按名称排在后面。 */
export function compareMeters(a: string, b: string): number {
  const ia = (KNOWN_METERS as readonly string[]).indexOf(a);
  const ib = (KNOWN_METERS as readonly string[]).indexOf(b);
  if (ia >= 0 && ib >= 0) return ia - ib;
  if (ia >= 0) return -1;
  if (ib >= 0) return 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

/** meterDepth is how far below its root a meter is, for indentation.
 * meterDepth 是计量项位于其根之下的层数，用于缩进。 */
export function meterDepth(meter: string): number {
  return meter.split(".").length - 1;
}

/** sortedMeters returns a record's meter lines in display order.
 * sortedMeters 按展示顺序返回一条记录的计量项。 */
export function sortedMeters(lines: readonly UsageMeterLine[]): UsageMeterLine[] {
  return [...lines].sort((a, b) => compareMeters(a.meter, b.meter));
}

/** LEDGER_EDGE_MS widens the usage window a ledger entry links to: the entry
 * spans charge times, while records are filtered by request time, which
 * comes earlier by the push delay.
 *
 * LEDGER_EDGE_MS 放宽账目链接到的用量窗口：账目按扣费时间分段，而记录按请求时间筛选，
 * 后者因推送延迟而更早。 */
export const LEDGER_EDGE_MS = 10 * 60 * 1000;

/** ledgerUsageHref is the usage page's records tab for a sealed usage ledger
 * entry: from its first charge, less the push delay, to its last.
 *
 * ledgerUsageHref 是一条已封账用量账目在用量页「明细」中对应的链接：从第一笔扣费
 * （减去推送延迟）到最后一笔扣费。 */
export function ledgerUsageHref(periodStart: string, periodEnd: string): string {
  const since = new Date(Date.parse(periodStart) - LEDGER_EDGE_MS).toISOString();
  const until = new Date(Date.parse(periodEnd) + 1000).toISOString();
  const params = new URLSearchParams({ tab: "records", since, until });
  return `/console/usage?${params.toString()}`;
}

/** UsageLink is what the usage page reads from its query: the tab to open
 * and an optional custom window, both validated.
 *
 * UsageLink 是用量页从查询参数读取的内容：要打开的标签页，以及可选的自定义时间窗口，
 * 两者都经过校验。 */
export interface UsageLink {
  tab: "model" | "user" | "key" | "records";
  since: string;
  until: string;
}

const USAGE_TABS = ["model", "user", "key", "records"] as const;

/** readUsageLink validates the usage page's query; an unknown tab or a
 * malformed time is dropped rather than failing the page.
 *
 * readUsageLink 校验用量页的查询参数；未知的标签页或格式错误的时间会被忽略，而不是让
 * 页面失败。 */
export function readUsageLink(params: Readonly<Record<string, string | string[] | undefined>>): UsageLink {
  const one = (key: string) => {
    const value = params[key];
    return typeof value === "string" ? value : "";
  };
  const iso = (value: string) => {
    const at = Date.parse(value);
    return value !== "" && Number.isFinite(at) ? new Date(at).toISOString() : "";
  };
  const tab = one("tab");
  return {
    tab: (USAGE_TABS as readonly string[]).includes(tab) ? (tab as UsageLink["tab"]) : "model",
    since: iso(one("since")),
    until: iso(one("until")),
  };
}

/** isoToLocalInput turns an RFC 3339 instant into a `datetime-local` value
 * in the browser's zone, the inverse of the page's own conversion.
 *
 * isoToLocalInput 把 RFC 3339 时刻转换成浏览器时区下的 `datetime-local` 输入值，与页面
 * 自身的转换相反。 */
export function isoToLocalInput(iso: string): string {
  if (!iso) return "";
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}`;
}

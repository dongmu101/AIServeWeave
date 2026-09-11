import { ApiError } from "./errors.ts";

/**
 * This module mirrors GET /operator/v1/metrics/history
 * (internal/types.MetricsHistoryResponse), which the control plane builds by
 * grouping the metrics_history_points table into one series per
 * (metric, labels) pair (P08).
 *
 * 本模块镜像 GET /operator/v1/metrics/history
 * (internal/types.MetricsHistoryResponse)，控制面把 metrics_history_points
 * 表按 (metric, labels) 分组成每对一条序列来构造它(P08)。
 */

/** MetricsHistoryPoint is one bucket of a MetricsHistorySeries.
 *
 * MetricsHistoryPoint 是 MetricsHistorySeries 的一个 bucket。 */
export interface MetricsHistoryPoint {
  bucketAt: string;
  value: number;
}

/** MetricsHistorySeries is one (metric, labels) time series over the queried
 * window.
 *
 * MetricsHistorySeries 是查询窗口内一条 (metric, labels) 时间序列。 */
export interface MetricsHistorySeries {
  metric: string;
  labels: Record<string, string>;
  points: MetricsHistoryPoint[];
}

/** MetricsHistory is the parsed response of GET /operator/v1/metrics/history.
 *
 * MetricsHistory 是 GET /operator/v1/metrics/history 的解析结果。 */
export interface MetricsHistory {
  since: string;
  until: string;
  series: MetricsHistorySeries[];
}

function fail(): never {
  throw new ApiError("contract");
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    fail();
  }
  return value as Record<string, unknown>;
}

function required(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (typeof value !== "string" || value === "") {
    fail();
  }
  return value;
}

function num(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  if (typeof value !== "number" || !Number.isFinite(value)) {
    fail();
  }
  return value;
}

function stringMap(value: unknown): Record<string, string> {
  if (value === undefined || value === null) {
    return {};
  }
  const entries = record(value);
  for (const item of Object.values(entries)) {
    if (typeof item !== "string") {
      fail();
    }
  }
  return entries as Record<string, string>;
}

function list<T>(value: unknown, parse: (item: unknown) => T): T[] {
  if (value === undefined || value === null) {
    return [];
  }
  if (!Array.isArray(value)) {
    fail();
  }
  return value.map(parse);
}

function parsePoint(value: unknown): MetricsHistoryPoint {
  const source = record(value);
  return { bucketAt: required(source, "bucket_at"), value: num(source, "value") };
}

function parseSeries(value: unknown): MetricsHistorySeries {
  const source = record(value);
  return {
    metric: required(source, "metric"),
    labels: stringMap(source.labels),
    points: list(source.points, parsePoint),
  };
}

/** parseMetricsHistory validates a GET /operator/v1/metrics/history response.
 *
 * parseMetricsHistory 校验 GET /operator/v1/metrics/history 的响应。 */
export function parseMetricsHistory(value: unknown): MetricsHistory {
  const source = record(value);
  return {
    since: required(source, "since"),
    until: required(source, "until"),
    series: list(source.series, parseSeries),
  };
}

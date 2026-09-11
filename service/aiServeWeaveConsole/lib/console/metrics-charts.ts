import type { MetricsHistoryPoint, MetricsHistorySeries } from "./metrics.ts";

/**
 * deltaByBucket turns a cumulative counter series into per-bucket
 * increments, one shorter than the input since the first bucket has no
 * predecessor to subtract. A decrease (a process restart resetting the
 * counter) clamps to zero rather than going negative — a chart that dipped
 * below the axis for one bucket after every restart would be harder to read
 * than one that briefly under-reports that bucket's true volume.
 *
 * deltaByBucket 把一条累计计数器序列转成逐桶增量，比输入短一位——第一个桶没有
 * 前驱可供相减。一次下降(进程重启导致计数器归零)会被钳制为零，而不是变负——
 * 一张每次重启后都会在某个桶跌破坐标轴的图，比一张只是暂时低估了那个桶真实
 * 量的图更难读。
 */
export function deltaByBucket(series: MetricsHistorySeries): MetricsHistoryPoint[] {
  const out: MetricsHistoryPoint[] = [];
  for (let i = 1; i < series.points.length; i++) {
    const prev = series.points[i - 1]!;
    const curr = series.points[i]!;
    out.push({ bucketAt: curr.bucketAt, value: Math.max(0, curr.value - prev.value) });
  }
  return out;
}

/**
 * approxP95 groups histogram bucket series by bucketAt (they all share one
 * timestamp per call site in this codebase — one snapshot at a time) and, for
 * each timestamp, returns the smallest `le` boundary whose cumulative count
 * is at least 95% of the largest (== total) count at that timestamp. This is
 * an approximation, not linear interpolation within a bucket: it is precise
 * enough to show a trend on a chart, not to answer "what is the exact p95
 * latency".
 *
 * approxP95 按 bucketAt 对直方图 bucket 序列分组(本仓库的调用方式下每次只
 * 传一个时间戳的快照)，对每个时间戳返回累计计数达到该时刻最大(即总数)计数
 * 95% 所需的最小 `le` 边界。这是一个近似值，不做桶内的线性插值：足够在图上
 * 显示趋势，不足以回答"精确的 p95 延迟是多少"。
 */
export function approxP95(buckets: MetricsHistorySeries[]): MetricsHistoryPoint[] {
  if (buckets.length === 0) {
    return [];
  }
  const bucketAt = buckets[0]!.points[0]?.bucketAt ?? "";
  const total = Math.max(...buckets.map((b) => b.points[0]?.value ?? 0));
  if (total === 0) {
    return [{ bucketAt, value: 0 }];
  }
  const sorted = [...buckets].sort((a, b) => Number(a.labels.le) - Number(b.labels.le));
  for (const b of sorted) {
    const count = b.points[0]?.value ?? 0;
    if (count >= total * 0.95) {
      return [{ bucketAt, value: Number(b.labels.le) }];
    }
  }
  return [{ bucketAt, value: Number(sorted[sorted.length - 1]?.labels.le ?? 0) }];
}

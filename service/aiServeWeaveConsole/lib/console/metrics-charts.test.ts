import { test } from "node:test";
import assert from "node:assert/strict";
import { deltaByBucket, approxP95 } from "./metrics-charts.ts";
import type { MetricsHistorySeries } from "./metrics.ts";

test("deltaByBucket turns a cumulative counter into per-bucket increments", () => {
  const series: MetricsHistorySeries = {
    metric: "gateway_http_requests_total",
    labels: { endpoint: "chat", status: "200" },
    points: [
      { bucketAt: "2026-09-11T00:00:00Z", value: 10 },
      { bucketAt: "2026-09-11T00:05:00Z", value: 25 },
      { bucketAt: "2026-09-11T00:10:00Z", value: 25 },
    ],
  };
  const deltas = deltaByBucket(series);
  assert.deepEqual(
    deltas.map((d) => d.value),
    [15, 0]
  );
});

test("deltaByBucket clamps a counter reset to zero rather than going negative", () => {
  const series: MetricsHistorySeries = {
    metric: "m",
    labels: {},
    points: [
      { bucketAt: "t1", value: 20 },
      { bucketAt: "t2", value: 3 }, // process restarted, counter reset
    ],
  };
  assert.deepEqual(
    deltaByBucket(series).map((d) => d.value),
    [0]
  );
});

test("approxP95 picks the le boundary where the cumulative count first reaches 95%", () => {
  const buckets: MetricsHistorySeries[] = [
    { metric: "gateway_http_request_duration_seconds_bucket", labels: { endpoint: "chat", le: "0.1" }, points: [{ bucketAt: "t1", value: 50 }] },
    { metric: "gateway_http_request_duration_seconds_bucket", labels: { endpoint: "chat", le: "0.5" }, points: [{ bucketAt: "t1", value: 94 }] },
    { metric: "gateway_http_request_duration_seconds_bucket", labels: { endpoint: "chat", le: "1" }, points: [{ bucketAt: "t1", value: 100 }] },
  ];
  const p95 = approxP95(buckets);
  assert.equal(p95[0]?.value, 1); // 94/100=94% < 95%, so the next boundary is picked
});

test("approxP95 returns a single zero point for no traffic rather than throwing", () => {
  const buckets: MetricsHistorySeries[] = [
    { metric: "gateway_http_request_duration_seconds_bucket", labels: { endpoint: "chat", le: "1" }, points: [{ bucketAt: "t1", value: 0 }] },
  ];
  const p95 = approxP95(buckets);
  assert.equal(p95[0]?.value, 0);
});

test("approxP95 returns an empty list for no buckets", () => {
  assert.deepEqual(approxP95([]), []);
});

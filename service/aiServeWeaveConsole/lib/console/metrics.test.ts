import { test } from "node:test";
import assert from "node:assert/strict";
import { parseMetricsHistory } from "./metrics.ts";
import { ApiError } from "./errors.ts";

test("parses a metrics history response with one series", () => {
  const history = parseMetricsHistory({
    since: "2026-09-11T00:00:00Z",
    until: "2026-09-12T00:00:00Z",
    series: [
      {
        metric: "gateway_http_requests_total",
        labels: { endpoint: "chat", status: "200" },
        points: [{ bucket_at: "2026-09-11T00:05:00Z", value: 12 }],
      },
    ],
  });
  assert.equal(history.series[0]?.metric, "gateway_http_requests_total");
  assert.equal(history.series[0]?.labels.endpoint, "chat");
  assert.equal(history.series[0]?.points[0]?.value, 12);
});

test("a response with no series field parses as an empty list", () => {
  // Matches this module's list() convention (mirrored from lib/console/fleet.ts):
  // an absent array field is empty, not malformed — the same way a Go slice
  // with omitempty is absent from JSON when there is nothing to report.
  const history = parseMetricsHistory({ since: "2026-09-11T00:00:00Z", until: "2026-09-12T00:00:00Z" });
  assert.deepEqual(history.series, []);
});

test("rejects a response missing since", () => {
  assert.throws(() => parseMetricsHistory({ until: "2026-09-12T00:00:00Z", series: [] }), ApiError);
});

test("rejects a point missing bucket_at", () => {
  assert.throws(
    () =>
      parseMetricsHistory({
        since: "2026-09-11T00:00:00Z",
        until: "2026-09-12T00:00:00Z",
        series: [{ metric: "m", labels: {}, points: [{ value: 1 }] }],
      }),
    ApiError
  );
});

test("an empty series list parses cleanly", () => {
  const history = parseMetricsHistory({ since: "2026-09-11T00:00:00Z", until: "2026-09-12T00:00:00Z", series: [] });
  assert.deepEqual(history.series, []);
});

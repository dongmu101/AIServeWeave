package alertengine

import (
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

func t0(offset time.Duration) time.Time {
	return time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC).Add(offset)
}

func TestDerivedSeriesRequestRateSumsAcrossLabelsAndDiffsCounters(t *testing.T) {
	points := []model.MetricsHistoryPoint{
		{Metric: "gateway_http_requests_total", Labels: "endpoint=chat,status=200", BucketAt: t0(0), Value: 10},
		{Metric: "gateway_http_requests_total", Labels: "endpoint=chat,status=500", BucketAt: t0(0), Value: 1},
		{Metric: "gateway_http_requests_total", Labels: "endpoint=chat,status=200", BucketAt: t0(5 * time.Minute), Value: 25},
		{Metric: "gateway_http_requests_total", Labels: "endpoint=chat,status=500", BucketAt: t0(5 * time.Minute), Value: 3},
	}
	got, err := DerivedSeries(model.MetricRequestRate, points, []time.Time{t0(5 * time.Minute)})
	if err != nil {
		t.Fatalf("DerivedSeries() error = %v, want nil", err)
	}
	// (25+3) - (10+1) = 17
	if len(got) != 1 || got[0] != 17 {
		t.Fatalf("DerivedSeries(request_rate) = %v, want [17]", got)
	}
}

func TestDerivedSeriesRequestRateClampsACounterResetToZero(t *testing.T) {
	points := []model.MetricsHistoryPoint{
		{Metric: "gateway_http_requests_total", Labels: "status=200", BucketAt: t0(0), Value: 100},
		{Metric: "gateway_http_requests_total", Labels: "status=200", BucketAt: t0(5 * time.Minute), Value: 5}, // replica restarted
	}
	got, err := DerivedSeries(model.MetricRequestRate, points, []time.Time{t0(5 * time.Minute)})
	if err != nil {
		t.Fatalf("DerivedSeries() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("DerivedSeries(request_rate) after a counter reset = %v, want [0] (clamped, not negative)", got)
	}
}

func TestDerivedSeriesSuccessRateComputesRatioOfDeltas(t *testing.T) {
	points := []model.MetricsHistoryPoint{
		{Metric: "gateway_http_requests_total", Labels: "status=200", BucketAt: t0(0), Value: 0},
		{Metric: "gateway_http_requests_total", Labels: "status=500", BucketAt: t0(0), Value: 0},
		{Metric: "gateway_http_requests_total", Labels: "status=200", BucketAt: t0(5 * time.Minute), Value: 95},
		{Metric: "gateway_http_requests_total", Labels: "status=500", BucketAt: t0(5 * time.Minute), Value: 5},
	}
	got, err := DerivedSeries(model.MetricSuccessRate, points, []time.Time{t0(5 * time.Minute)})
	if err != nil {
		t.Fatalf("DerivedSeries() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != 0.95 {
		t.Fatalf("DerivedSeries(success_rate) = %v, want [0.95]", got)
	}
}

func TestDerivedSeriesSuccessRateWithNoTrafficIsTreatedAsFullySuccessful(t *testing.T) {
	points := []model.MetricsHistoryPoint{
		{Metric: "gateway_http_requests_total", Labels: "status=200", BucketAt: t0(0), Value: 10},
		{Metric: "gateway_http_requests_total", Labels: "status=200", BucketAt: t0(5 * time.Minute), Value: 10},
	}
	got, err := DerivedSeries(model.MetricSuccessRate, points, []time.Time{t0(5 * time.Minute)})
	if err != nil {
		t.Fatalf("DerivedSeries() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != 1.0 {
		t.Fatalf("DerivedSeries(success_rate) with zero delta traffic = %v, want [1.0] (no evidence of failure)", got)
	}
}

func TestDerivedSeriesLatencyP95FindsTheSmallestBucketBoundaryAtOrAbove95Percent(t *testing.T) {
	points := []model.MetricsHistoryPoint{
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=0.1", BucketAt: t0(0), Value: 0},
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=0.5", BucketAt: t0(0), Value: 0},
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=+Inf", BucketAt: t0(0), Value: 0},
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=0.1", BucketAt: t0(5 * time.Minute), Value: 80},
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=0.5", BucketAt: t0(5 * time.Minute), Value: 96},
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=+Inf", BucketAt: t0(5 * time.Minute), Value: 100},
	}
	got, err := DerivedSeries(model.MetricLatencyP95, points, []time.Time{t0(5 * time.Minute)})
	if err != nil {
		t.Fatalf("DerivedSeries() error = %v, want nil", err)
	}
	// deltas: le=0.1 -> 80, le=0.5 -> 96, le=+Inf -> 100; total 100, 95% = 95;
	// smallest le with cumulative count >= 95 is le=0.5 (count 96).
	if len(got) != 1 || got[0] != 0.5 {
		t.Fatalf("DerivedSeries(latency_p95) = %v, want [0.5]", got)
	}
}

func TestDerivedSeriesLatencyP95UsesMaxAcrossAllLeBucketsNotJustTheHighestLe(t *testing.T) {
	// Simulates a multi-replica staggered restart: the raw le=+Inf counter
	// resets between the two timestamps (net decrease, clamped to zero),
	// while le=0.1 and le=0.5 keep climbing normally. If total were taken
	// only from the highest-le (+Inf) bucket's clamped delta, it would be
	// 0 here and the function would wrongly report "no traffic" (p95=0)
	// even though le=0.1 and le=0.5 show real, large deltas. Taking the
	// max clamped delta across every le bucket (matching Console's
	// Math.max(...buckets.map(...))) recovers the correct total and
	// therefore the correct boundary.
	//
	// 模拟多副本交错重启的场景：两个时间戳之间 le=+Inf 的原始计数器发生了
	// 重置(净减少，被钳制为零)，而 le=0.1 与 le=0.5 仍在正常增长。如果
	// total 只取最高 le(+Inf)桶的钳制增量，这里会是 0，函数就会错误地
	// 报告"没有流量"(p95=0)，尽管 le=0.1 与 le=0.5 都显示出真实的大额
	// 增量。取全部 le 桶钳制增量里的最大值(对应 Console 的
	// Math.max(...buckets.map(...)))才能得到正确的 total，进而选对边界。
	points := []model.MetricsHistoryPoint{
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=0.1", BucketAt: t0(0), Value: 10},
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=0.5", BucketAt: t0(0), Value: 10},
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=+Inf", BucketAt: t0(0), Value: 500},
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=0.1", BucketAt: t0(5 * time.Minute), Value: 190},
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=0.5", BucketAt: t0(5 * time.Minute), Value: 195},
		{Metric: "gateway_http_request_duration_seconds_bucket", Labels: "le=+Inf", BucketAt: t0(5 * time.Minute), Value: 200}, // replica restarted, net decrease from 500
	}
	got, err := DerivedSeries(model.MetricLatencyP95, points, []time.Time{t0(5 * time.Minute)})
	if err != nil {
		t.Fatalf("DerivedSeries() error = %v, want nil", err)
	}
	// deltas: le=0.1 -> 180, le=0.5 -> 185, le=+Inf -> max(0, 200-500) = 0;
	// total = max(180, 185, 0) = 185, 95% = 175.75;
	// smallest le with cumulative delta >= 175.75 is le=0.1 (delta 180).
	if len(got) != 1 || got[0] != 0.1 {
		t.Fatalf("DerivedSeries(latency_p95) with a reset +Inf bucket = %v, want [0.1] (total must be the max across all le buckets, not just +Inf's)", got)
	}
}

func TestDerivedSeriesTokenUsageSumsAcrossDirectionAndDiffs(t *testing.T) {
	points := []model.MetricsHistoryPoint{
		{Metric: "gateway_tokens_total", Labels: "direction=prompt", BucketAt: t0(0), Value: 1000},
		{Metric: "gateway_tokens_total", Labels: "direction=completion", BucketAt: t0(0), Value: 200},
		{Metric: "gateway_tokens_total", Labels: "direction=prompt", BucketAt: t0(5 * time.Minute), Value: 1500},
		{Metric: "gateway_tokens_total", Labels: "direction=completion", BucketAt: t0(5 * time.Minute), Value: 350},
	}
	got, err := DerivedSeries(model.MetricTokenUsage, points, []time.Time{t0(5 * time.Minute)})
	if err != nil {
		t.Fatalf("DerivedSeries() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != 650 {
		t.Fatalf("DerivedSeries(token_usage) = %v, want [650]", got)
	}
}

func TestDerivedSeriesCapacityReadsTheGaugeDirectlyWithoutDiffing(t *testing.T) {
	points := []model.MetricsHistoryPoint{
		{Metric: "tunnel_server_slots_total", Labels: "", BucketAt: t0(5 * time.Minute), Value: 42},
	}
	got, err := DerivedSeries(model.MetricCapacity, points, []time.Time{t0(5 * time.Minute)})
	if err != nil {
		t.Fatalf("DerivedSeries() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != 42 {
		t.Fatalf("DerivedSeries(capacity) = %v, want [42] (a gauge, read as-is)", got)
	}
}

func TestDerivedSeriesRejectsAnUnknownMetric(t *testing.T) {
	if _, err := DerivedSeries("not_a_real_metric", nil, []time.Time{t0(0)}); err == nil {
		t.Fatal("DerivedSeries(unknown metric) error = nil, want an error")
	}
}

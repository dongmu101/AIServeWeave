// Package alertengine computes STATUS.md's P09/C29 derived alert metrics
// (request_rate, success_rate, latency_p95, token_usage, capacity) from
// metrics_history's raw Prometheus rollup rows, and evaluates alert_rules
// against them on a background loop (evaluator.go), delivering an optional
// webhook on each firing/resolved transition (webhook.go).
//
// The five derived metrics are NOT metrics_history.metric column values —
// P08's collector only ever wrote raw counter/histogram rollups
// (gateway_http_requests_total, gateway_http_request_duration_seconds_
// bucket/_sum/_count, gateway_tokens_total, tunnel_server_slots_total).
// success_rate and latency_p95 in particular require the same
// approximation algorithms already validated in Console's
// lib/console/metrics-charts.ts (deltaByBucket, approxP95), ported to Go
// here — there is no shared runtime between the two, so this is a
// deliberate re-implementation of the same semantics, not shared code.
//
// alertengine 包从 metrics_history 的原始 Prometheus 汇总行计算出
// STATUS.md P09/C29 的派生告警指标(request_rate、success_rate、
// latency_p95、token_usage、capacity)，并在后台循环里(evaluator.go)拿
// alert_rules 去评估它们，在每次 firing/resolved 状态转换时按需投递一次
// Webhook(webhook.go)。
//
// 这五个派生指标不是 metrics_history.metric 列的取值——P08 的采集器写入的
// 从来只是原始计数器/直方图汇总行(gateway_http_requests_total、
// gateway_http_request_duration_seconds_bucket/_sum/_count、
// gateway_tokens_total、tunnel_server_slots_total)。success_rate 与
// latency_p95 尤其需要 Console 前端 lib/console/metrics-charts.ts 里已经
// 验证过的近似算法(deltaByBucket、approxP95)，这里把它们移植成 Go——两者
// 之间没有共享的运行时，因此这是对同一套语义的刻意重新实现，不是共享代码。
package alertengine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

const (
	rawRequestsTotal  = "gateway_http_requests_total"
	rawDurationBucket = "gateway_http_request_duration_seconds_bucket"
	rawTokensTotal    = "gateway_tokens_total"
	rawCapacityGauge  = "tunnel_server_slots_total"
)

// DerivedSeries computes metric's value at each of bucketAts (already
// sorted ascending, one per 5-minute rollup bucket the caller wants a
// value for) from points — the raw metrics_history rows covering that
// window and, for counter-based metrics, the one bucket immediately before
// bucketAts[0] needed to compute the first delta. Returns one value per
// bucketAt, same order.
//
// DerivedSeries 从 points(覆盖该窗口的原始 metrics_history 行；对基于计数器
// 的指标，还需要 bucketAts[0] 之前紧邻的一个桶用于算出第一个差值)计算出
// metric 在 bucketAts(已按升序排列，调用方想要取值的每一个 5 分钟汇总桶)
// 每一个时刻的值。返回值与 bucketAts 一一对应、顺序相同。
func DerivedSeries(metric string, points []model.MetricsHistoryPoint, bucketAts []time.Time) ([]float64, error) {
	switch metric {
	case model.MetricRequestRate:
		return counterDelta(points, rawRequestsTotal, bucketAts), nil
	case model.MetricSuccessRate:
		return successRate(points, bucketAts), nil
	case model.MetricLatencyP95:
		return latencyP95(points, bucketAts), nil
	case model.MetricTokenUsage:
		return counterDelta(points, rawTokensTotal, bucketAts), nil
	case model.MetricCapacity:
		return gaugeValue(points, rawCapacityGauge, bucketAts), nil
	default:
		return nil, fmt.Errorf("alertengine: unknown metric %q", metric)
	}
}

// parseCanonicalLabels parses metrics_history.Labels's canonical
// "k1=v1,k2=v2" form (keys already sorted by the writer) into a map — the
// inverse of internal/metricshistory's unexported canonicalLabels, kept as
// its own small helper here since that function lives in a different
// package and is not exported.
//
// parseCanonicalLabels 解析 metrics_history.Labels 的规范 "k1=v1,k2=v2"
// 形式(写入方已经排好序的 key)为一个 map——是 internal/metricshistory 未
// 导出的 canonicalLabels 的逆操作，在这里单独实现成一个小助手，因为那个
// 函数在另一个包里且未导出。
func parseCanonicalLabels(labels string) map[string]string {
	out := map[string]string{}
	if labels == "" {
		return out
	}
	for _, pair := range strings.Split(labels, ",") {
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

// sumByBucket sums every point of the given raw metric name across all
// label combinations, grouped by BucketAt.
//
// sumByBucket 把给定原始指标名下的每一个点，跨全部标签组合按 BucketAt 分组
// 求和。
func sumByBucket(points []model.MetricsHistoryPoint, rawMetric string) map[time.Time]float64 {
	sums := map[time.Time]float64{}
	for _, p := range points {
		if p.Metric != rawMetric {
			continue
		}
		sums[p.BucketAt] += p.Value
	}
	return sums
}

// counterDelta computes, for each requested bucketAt, sumByBucket's value
// at that bucket minus its value at the immediately preceding 5-minute
// bucket, clamped to zero — the same semantics as Console's
// deltaByBucket, generalized from "the input series's own predecessor" to
// "look up bucketAt minus 5 minutes", since callers here ask for specific
// timestamps rather than walking a contiguous series.
//
// counterDelta 为每一个请求的 bucketAt 计算 sumByBucket 在该桶的值减去它
// 紧邻前一个 5 分钟桶的值，并钳制为非负——与 Console 的 deltaByBucket 语义
// 相同，只是把"输入序列自身的前一项"泛化成了"查找 bucketAt 减 5 分钟"，
// 因为这里的调用方要的是具体时间戳而不是遍历一条连续序列。
func counterDelta(points []model.MetricsHistoryPoint, rawMetric string, bucketAts []time.Time) []float64 {
	sums := sumByBucket(points, rawMetric)
	out := make([]float64, len(bucketAts))
	for i, at := range bucketAts {
		curr := sums[at]
		prev := sums[at.Add(-5*time.Minute)]
		out[i] = max(0, curr-prev)
	}
	return out
}

// successRate computes, for each bucketAt, the ratio of 2xx-status request
// deltas to total request deltas over the preceding 5 minutes. A bucket
// with zero delta traffic is treated as 1.0 (fully successful) — there is
// no evidence of failure in a window with no requests, and dividing by
// zero would otherwise need a separate "undefined" representation this
// design deliberately avoids.
//
// successRate 为每一个 bucketAt 计算过去 5 分钟内 2xx 状态请求增量与总请求
// 增量之比。一个增量流量为零的桶按 1.0(完全成功)处理——一个没有请求的
// 窗口谈不上有失败证据，否则除以零还需要一种本设计刻意避免的"未定义"表示。
func successRate(points []model.MetricsHistoryPoint, bucketAts []time.Time) []float64 {
	byBucketAndStatus := map[time.Time]map[string]float64{}
	for _, p := range points {
		if p.Metric != rawRequestsTotal {
			continue
		}
		status := parseCanonicalLabels(p.Labels)["status"]
		if byBucketAndStatus[p.BucketAt] == nil {
			byBucketAndStatus[p.BucketAt] = map[string]float64{}
		}
		byBucketAndStatus[p.BucketAt][status] += p.Value
	}
	totalAt := func(at time.Time) float64 {
		var sum float64
		for _, v := range byBucketAndStatus[at] {
			sum += v
		}
		return sum
	}
	successAt := func(at time.Time) float64 {
		var sum float64
		for status, v := range byBucketAndStatus[at] {
			if strings.HasPrefix(status, "2") {
				sum += v
			}
		}
		return sum
	}

	out := make([]float64, len(bucketAts))
	for i, at := range bucketAts {
		totalDelta := max(0, totalAt(at)-totalAt(at.Add(-5*time.Minute)))
		successDelta := max(0, successAt(at)-successAt(at.Add(-5*time.Minute)))
		if totalDelta == 0 {
			out[i] = 1
			continue
		}
		out[i] = successDelta / totalDelta
	}
	return out
}

// latencyP95 computes, for each bucketAt, an approximate P95 latency by
// finding the smallest histogram `le` boundary whose delta cumulative
// count over the preceding 5 minutes is at least 95% of the total delta
// count — the same non-interpolating approximation Console's approxP95
// already uses (precise enough for threshold comparison, not for an exact
// percentile).
//
// latencyP95 为每一个 bucketAt 计算近似 P95 延迟：找到过去 5 分钟内增量
// 累计计数达到增量总计数 95% 所需的最小直方图 `le` 边界——与 Console 的
// approxP95 已经使用的、不做插值的近似算法相同(足够用于阈值比较，不足以
// 给出精确分位数)。
func latencyP95(points []model.MetricsHistoryPoint, bucketAts []time.Time) []float64 {
	// byLeAndBucket[le][bucketAt] = cumulative count
	byLeAndBucket := map[string]map[time.Time]float64{}
	for _, p := range points {
		if p.Metric != rawDurationBucket {
			continue
		}
		le := parseCanonicalLabels(p.Labels)["le"]
		if byLeAndBucket[le] == nil {
			byLeAndBucket[le] = map[time.Time]float64{}
		}
		byLeAndBucket[le][p.BucketAt] += p.Value
	}
	les := make([]string, 0, len(byLeAndBucket))
	for le := range byLeAndBucket {
		les = append(les, le)
	}
	sort.Slice(les, func(i, j int) bool { return leValue(les[i]) < leValue(les[j]) })

	out := make([]float64, len(bucketAts))
	for i, at := range bucketAts {
		prev := at.Add(-5 * time.Minute)
		var total float64
		if len(les) > 0 {
			last := les[len(les)-1]
			total = max(0, byLeAndBucket[last][at]-byLeAndBucket[last][prev])
		}
		if total == 0 {
			out[i] = 0
			continue
		}
		threshold := total * 0.95
		found := leValue(les[len(les)-1])
		for _, le := range les {
			delta := max(0, byLeAndBucket[le][at]-byLeAndBucket[le][prev])
			if delta >= threshold {
				found = leValue(le)
				break
			}
		}
		out[i] = found
	}
	return out
}

// leValue parses a histogram bucket's `le` label into a float64 for
// sorting and comparison, treating the Prometheus sentinel "+Inf" as a
// very large finite number so it always sorts last.
//
// leValue 把直方图桶的 `le` 标签解析成 float64 用于排序与比较，把
// Prometheus 的哨兵值 "+Inf" 当作一个足够大的有限数处理，使其始终排在
// 最后。
func leValue(le string) float64 {
	if le == "+Inf" {
		return 1e18
	}
	v, _ := strconv.ParseFloat(le, 64)
	return v
}

// gaugeValue reads a gauge metric's raw per-bucket value with no diffing,
// summed across whatever label combinations exist at that bucket.
//
// gaugeValue 读取一个量表指标的原始逐桶值，不做差分，按该桶存在的全部标签
// 组合求和。
func gaugeValue(points []model.MetricsHistoryPoint, rawMetric string, bucketAts []time.Time) []float64 {
	sums := sumByBucket(points, rawMetric)
	out := make([]float64, len(bucketAts))
	for i, at := range bucketAts {
		out[i] = sums[at]
	}
	return out
}

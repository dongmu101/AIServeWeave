package httpapi

import (
	"testing"

	"AIServeWeave/common/metrics"
)

// httpapiMetrics is every instrument this package records, restated so the
// catalogue test iterates a list rather than whatever happened to be
// recorded.
//
// httpapiMetrics 是本包记录的全部仪器，在此重述，好让目录测试遍历一份清单，
// 而不是遍历「碰巧被记录到的那些」。
var httpapiMetrics = []string{
	MetricRequestsTotal,
	MetricRequestDurationSeconds,
	MetricInflightRequests,
	MetricTTFTSeconds,
	MetricTokensTotal,
	MetricOutputTokensPerSecond,
	MetricRateLimitedTotal,
	MetricLimiterUnavailableTotal,
	MetricRequestLogDroppedTotal,
	MetricRequestLogPushFailedTotal,
	MetricWorkflowJobsTotal,
	MetricWorkflowJobDurationSeconds,
	MetricWorkflowJobOOMTotal,
	MetricArtifactTransferBytesTotal,
	MetricArtifactTransferDurationSeconds,
	MetricArtifactTransfersTotal,
	MetricResponsePersistDroppedTotal,
	MetricResponsePersistFailedTotal,
}

func TestHTTPAPIDescriptionsCoverEveryMetric(t *testing.T) {
	descs := Descriptions()

	for _, name := range httpapiMetrics {
		if _, ok := descs[name]; !ok {
			t.Errorf("metric %s has no description", name)
		}
	}
	known := make(map[string]bool, len(httpapiMetrics))
	for _, name := range httpapiMetrics {
		known[name] = true
	}
	for name, desc := range descs {
		if !known[name] {
			t.Errorf("description %s names no metric this package records", name)
		}
		if desc.Help == "" {
			t.Errorf("metric %s has an empty help string", name)
		}
		if desc.Kind == metrics.KindHistogram && len(desc.Buckets) == 0 {
			t.Errorf("histogram %s has no buckets", name)
		}
	}
}

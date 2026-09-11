package handler

import (
	"net/http"
	"strings"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// metricsHistory answers GET /operator/v1/metrics/history?since=&until=,
// grouping stored points by (metric, labels) into one time series each —
// the shape Console's ECharts panels want, not the flat row-per-bucket shape
// the store returns.
//
// metricsHistory 回答 GET /operator/v1/metrics/history?since=&until=，把存储
// 返回的"每个 bucket 一行"的扁平数据，按 (metric, labels) 分组成 Console
// ECharts 面板需要的每条序列一组的形状。
func metricsHistory(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		rawSince, rawUntil := query.Get("since"), query.Get("until")
		since, sinceOK := timeParam(rawSince)
		until, untilOK := timeParam(rawUntil)
		if rawSince == "" || rawUntil == "" || !sinceOK || !untilOK {
			writeError(w, http.StatusBadRequest, "since and until are required RFC 3339 timestamps")
			return
		}

		points, err := ctx.Logic.ListMetricsHistory(r.Context(), since, until)
		if err != nil {
			respondErr(w, err)
			return
		}

		type seriesKey struct{ metric, labels string }
		grouped := map[seriesKey]*types.MetricsHistorySeries{}
		var order []seriesKey
		for _, p := range points {
			k := seriesKey{metric: p.Metric, labels: p.Labels}
			s, ok := grouped[k]
			if !ok {
				s = &types.MetricsHistorySeries{Metric: p.Metric, Labels: parseCanonicalLabels(p.Labels)}
				grouped[k] = s
				order = append(order, k)
			}
			s.Points = append(s.Points, types.MetricsHistoryPoint{BucketAt: p.BucketAt.UTC(), Value: p.Value})
		}
		resp := types.MetricsHistoryResponse{Since: since.UTC(), Until: until.UTC()}
		for _, k := range order {
			resp.Series = append(resp.Series, *grouped[k])
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// parseCanonicalLabels is the inverse of metricshistory.canonicalLabels
// (internal/metricshistory/collector.go).
//
// parseCanonicalLabels 是 metricshistory.canonicalLabels
// (internal/metricshistory/collector.go)的逆操作。
func parseCanonicalLabels(s string) map[string]string {
	labels := map[string]string{}
	if s == "" {
		return labels
	}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			labels[k] = v
		}
	}
	return labels
}

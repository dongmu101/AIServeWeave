package logic

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// HistoryMetricNames is the closed list of metric names Console C27 charts.
// It mirrors internal/metricshistory's collector allowlist by name, not by
// import, so a rename on either side is a visible mismatch instead of a
// silent one.
//
// HistoryMetricNames 是 Console C27 图表用到的封闭指标名单，按名字而非依赖
// 与 internal/metricshistory 采集器的允许名单保持一致——任何一侧改名都会变成
// 显眼的不一致，而不是悄悄失联。
var HistoryMetricNames = []string{
	"gateway_http_requests_total",
	"gateway_http_request_duration_seconds_bucket",
	"gateway_http_request_duration_seconds_sum",
	"gateway_http_request_duration_seconds_count",
	"gateway_tokens_total",
	"tunnel_server_slots_total",
}

// ListMetricsHistory returns every rolled-up point across the metrics
// Console C27 charts, in [since, until).
//
// ListMetricsHistory 返回 Console C27 图表用到的全部指标在 [since, until) 内
// 的汇总数据点。
func (s *Service) ListMetricsHistory(ctx context.Context, since, until time.Time) ([]model.MetricsHistoryPoint, error) {
	if !until.After(since) {
		return nil, ErrInvalidInput
	}
	return s.store.ListRollup(ctx, HistoryMetricNames, since, until)
}

// Package metrics is aiServeWeaveControlPlane's common/metrics catalogue —
// this service's first use of common/metrics, confirmed empty before this
// package (see the P08 design doc and this repo's own README known-gaps
// list).
//
// metrics 包是控制面在 common/metrics 上的指标目录——本服务首次使用
// common/metrics(此前确认为空，见 P08 设计文档与本仓库自己 README 的已知缺口
// 列表)。
package metrics

import "AIServeWeave/common/metrics"

const (
	MetricHTTPRequestsTotal        = "controlplane_http_requests_total"
	MetricHTTPRequestDurationSecs  = "controlplane_http_request_duration_seconds"
	MetricHTTPInflightRequests     = "controlplane_http_inflight_requests"
	MetricOutboxLagGeneration      = "controlplane_revocation_outbox_lag"
	MetricFleetCallsTotal          = "controlplane_fleet_calls_total"
	MetricRegistryClientCallsTotal = "controlplane_registry_client_calls_total"
)

// Descriptions returns this service's metric catalogue.
//
// Descriptions 返回本服务的指标目录。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricHTTPRequestsTotal:        {Kind: metrics.KindCounter, Help: "HTTP requests by route template and status. / 按路由模板与状态码分类的 HTTP 请求数。"},
		MetricHTTPRequestDurationSecs:  {Kind: metrics.KindHistogram, Help: "HTTP request duration. / HTTP 请求耗时。", Buckets: metrics.SecondsBuckets()},
		MetricHTTPInflightRequests:     {Kind: metrics.KindGauge, Help: "In-flight HTTP requests by route template. / 按路由模板分类的在途 HTTP 请求数。"},
		MetricOutboxLagGeneration:      {Kind: metrics.KindGauge, Help: "generation - delivered_generation for the revocation outbox. / 吊销 outbox 的 generation 与 delivered_generation 之差。"},
		MetricFleetCallsTotal:          {Kind: metrics.KindCounter, Help: "Fleet aggregation calls to Gateway replicas by result. / 控制面向 Gateway 副本发起的机群聚合调用，按结果分类。"},
		MetricRegistryClientCallsTotal: {Kind: metrics.KindCounter, Help: "Calls to the Registry TokenAdmin client by result. / 控制面对 Registry TokenAdmin 客户端的调用，按结果分类。"},
	}
}

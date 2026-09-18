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
	// MetricModelPullRouterCallsTotal counts calls to modelpullrouter.Router
	// (STATUS.md's P2 model distribution, the control plane forwarding
	// layer) by result, the same success/error split
	// MetricRegistryClientCallsTotal uses rather than the router's own
	// richer per-replica taxonomy — reproducing that here would risk a
	// label value outside what this package's own cardinality test
	// asserts, the same reasoning recordFleetCall's doc comment gives.
	MetricModelPullRouterCallsTotal = "controlplane_model_pull_router_calls_total"
	// MetricArtifactStorageBytes is the total bytes job_artifacts records as
	// actually copied into object storage (storage_key set), periodically
	// recomputed from that table rather than accumulated at write time
	// (STATUS.md's A06) — job_artifacts is the durable, authoritative source
	// for this figure, unlike a Gateway-held running counter, which would
	// reset to zero on every replica restart while the storage bytes it was
	// meant to describe kept existing.
	//
	// MetricArtifactStorageBytes 是 job_artifacts 记录的、已实际复制进对象
	// 存储（storage_key 非空）的总字节数，定期从该表重新计算，而不是在写入
	// 时累加（STATUS.md 的 A06）——job_artifacts 是这个数字的持久、权威来源，
	// 不同于一个由 Gateway 维护的运行时计数器：后者会在每次副本重启时归零，
	// 而它本该描述的存储字节依然存在。
	MetricArtifactStorageBytes = "controlplane_artifact_storage_bytes"
)

// Descriptions returns this service's metric catalogue.
//
// Descriptions 返回本服务的指标目录。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricHTTPRequestsTotal:         {Kind: metrics.KindCounter, Help: "HTTP requests by route template and status. / 按路由模板与状态码分类的 HTTP 请求数。"},
		MetricHTTPRequestDurationSecs:   {Kind: metrics.KindHistogram, Help: "HTTP request duration. / HTTP 请求耗时。", Buckets: metrics.SecondsBuckets()},
		MetricHTTPInflightRequests:      {Kind: metrics.KindGauge, Help: "In-flight HTTP requests by route template. / 按路由模板分类的在途 HTTP 请求数。"},
		MetricOutboxLagGeneration:       {Kind: metrics.KindGauge, Help: "generation - delivered_generation for the revocation outbox. / 吊销 outbox 的 generation 与 delivered_generation 之差。"},
		MetricFleetCallsTotal:           {Kind: metrics.KindCounter, Help: "Fleet aggregation calls to Gateway replicas by result. / 控制面向 Gateway 副本发起的机群聚合调用，按结果分类。"},
		MetricRegistryClientCallsTotal:  {Kind: metrics.KindCounter, Help: "Calls to the Registry TokenAdmin client by result. / 控制面对 Registry TokenAdmin 客户端的调用，按结果分类。"},
		MetricModelPullRouterCallsTotal: {Kind: metrics.KindCounter, Help: "Calls to forward a model-pull trigger or status read to Gateway replicas, by result. / 向 Gateway 副本转发模型拉取触发或状态读取的调用，按结果分类。"},
		MetricArtifactStorageBytes:      {Kind: metrics.KindGauge, Help: "Total bytes of job artifacts actually copied into object storage. / 已实际复制进对象存储的产物总字节数。"},
	}
}

package registryserver

import (
	"sync"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
)

// Metric name constants — mirrors tunnelserver/metrics.go's convention: one
// exported constant per name so a rename shows up as a compile error at every
// call site instead of a silently orphaned series.
//
// 指标名常量——照抄 tunnelserver/metrics.go 的约定：每个名字一个导出常量，改名
// 会在每个调用点变成编译错误，而不是留下一条悄悄失联的序列。
const (
	MetricRegisterTotal            = "registry_register_total"
	MetricCertRenewalTotal         = "registry_cert_renewal_total"
	MetricGatewayReplicasConnected = "registry_gateway_replicas_connected"
	MetricTokenOpsTotal            = "registry_token_ops_total"
	MetricNodeStateChangesTotal    = "registry_node_state_changes_total"
	MetricListNodeStatesTotal      = "registry_list_node_states_total"
)

// Closed result/operation/action vocabularies. No RPC method may pass a
// string here that is not one of these constants — that is what keeps every
// label value bounded regardless of how many distinct gRPC error messages
// exist.
//
// 封闭的 result/operation/action 取值集合。任何 RPC 方法都不能在这里传入不属于
// 这些常量的字符串——这正是不论底层有多少种 gRPC 错误消息，标签值始终有界的
// 保证方式。
const (
	ResultSuccess         = "success"
	ResultReconnect       = "reconnect"
	ResultConflict        = "conflict"
	ResultPendingApproval = "pending_approval"
	ResultInvalid         = "invalid"
	ResultUnauthorized    = "unauthorized"
	ResultNotFound        = "not_found"
	ResultInternal        = "internal"
)

const (
	TokenOpMint   = "mint"
	TokenOpRevoke = "revoke"
)

const (
	NodeActionApprove          = "approve"
	NodeActionDisable          = "disable"
	NodeActionEnable           = "enable"
	NodeActionMaintenanceSet   = "maintenance_set"
	NodeActionMaintenanceClear = "maintenance_clear"
)

// Descriptions returns this package's metric catalogue.
//
// Descriptions 返回本包的指标目录。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricRegisterTotal:            {Kind: metrics.KindCounter, Help: "Node identity registration attempts by outcome. / 按结果分类的节点身份注册尝试次数。"},
		MetricCertRenewalTotal:         {Kind: metrics.KindCounter, Help: "Certificate renewal attempts by outcome. / 按结果分类的证书续期尝试次数。"},
		MetricGatewayReplicasConnected: {Kind: metrics.KindGauge, Help: "Gateway replicas currently joined to this Registry. / 当前加入本 Registry 的 Gateway 副本数。"},
		MetricTokenOpsTotal:            {Kind: metrics.KindCounter, Help: "TokenAdmin token mint/revoke calls by outcome. / TokenAdmin 铸造/撤销 token 调用，按结果分类。"},
		MetricNodeStateChangesTotal:    {Kind: metrics.KindCounter, Help: "Node approve/disable/enable/maintenance calls by outcome. / 节点审批/禁用/启用/维护调用，按结果分类。"},
		MetricListNodeStatesTotal:      {Kind: metrics.KindCounter, Help: "ListNodeStates calls by outcome. / ListNodeStates 调用次数，按结果分类。"},
	}
}

// recorder is registryserver's typed wrapper over runtime.Metrics, mirroring
// tunnelserver/metrics.go's recorder — the caller passes only a closed-set
// result string, never a raw error, so a label value cannot become free text
// by accident.
//
// recorder 是 registryserver 对 runtime.Metrics 的类型化包装，照抄
// tunnelserver/metrics.go 的 recorder——调用方只能传入一个封闭集合里的 result
// 字符串，从不传原始 error，标签值因此不会意外变成自由文本。
type recorder struct {
	sink runtime.Metrics

	mu        sync.Mutex
	connected int
}

func newRecorder(sink runtime.Metrics) *recorder {
	if sink == nil {
		sink = discardMetrics{}
	}
	return &recorder{sink: sink}
}

func (r *recorder) Register(result string) {
	r.sink.Counter(MetricRegisterTotal, map[string]string{"result": result}).Add(1)
}

func (r *recorder) CertRenewal(result string) {
	r.sink.Counter(MetricCertRenewalTotal, map[string]string{"result": result}).Add(1)
}

func (r *recorder) GatewayJoined() {
	r.mu.Lock()
	r.connected++
	n := r.connected
	r.mu.Unlock()
	r.sink.Gauge(MetricGatewayReplicasConnected, nil).Set(float64(n))
}

func (r *recorder) GatewayLeft() {
	r.mu.Lock()
	if r.connected > 0 {
		r.connected--
	}
	n := r.connected
	r.mu.Unlock()
	r.sink.Gauge(MetricGatewayReplicasConnected, nil).Set(float64(n))
}

func (r *recorder) TokenOp(op, result string) {
	r.sink.Counter(MetricTokenOpsTotal, map[string]string{"operation": op, "result": result}).Add(1)
}

func (r *recorder) NodeStateChange(action, result string) {
	r.sink.Counter(MetricNodeStateChangesTotal, map[string]string{"action": action, "result": result}).Add(1)
}

func (r *recorder) ListNodeStates(result string) {
	r.sink.Counter(MetricListNodeStatesTotal, map[string]string{"result": result}).Add(1)
}

// discardMetrics is the zero-cost sink used when Config.Metrics is nil,
// mirroring tunnelserver's discardMetrics.
//
// discardMetrics 是 Config.Metrics 为 nil 时使用的零开销汇点，照抄
// tunnelserver 的 discardMetrics。
type discardMetrics struct{}

func (discardMetrics) Counter(string, map[string]string) runtime.Counter { return discardInstrument{} }
func (discardMetrics) Gauge(string, map[string]string) runtime.Gauge     { return discardInstrument{} }
func (discardMetrics) Histogram(string, map[string]string) runtime.Histogram {
	return discardInstrument{}
}

type discardInstrument struct{}

func (discardInstrument) Add(float64)     {}
func (discardInstrument) Set(float64)     {}
func (discardInstrument) Observe(float64) {}

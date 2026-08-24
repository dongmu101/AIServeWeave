package tunnel

import (
	"errors"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
)

// This file is the whole of the tunnel's observability surface: the metric
// names README.md's 可观测性 section lists, the closed label vocabularies they
// are allowed to carry, and the typed recorder every other file in the package
// goes through.
//
// Two rules hold everywhere and are what the cardinality test enforces:
//
//   - No value that came off the wire ever becomes a label. Prompts, workflow
//     templates, model names, request ids, endpoints and the free-form reason
//     string on a Cancel frame are all unbounded, and some of them are user
//     content. Labels are drawn from the enumerations below, from the
//     configured node and runtime ids, and from nothing else.
//   - Every metric that describes one link carries replica_id, because a
//     symptom seen on one tunnel and a symptom seen on the whole node call for
//     different action. The three node-scoped metrics that legitimately have
//     no replica are listed in NodeScopedMetrics.

// Metric names, one per instrument in README.md's 可观测性 section.
const (
	// MetricConnectionState is the per-tunnel state gauge, valued by
	// State.Metric.
	MetricConnectionState = "tunnel_connection_state"
	// MetricConnectedReplicas is how many tunnels can currently take a
	// request. Zero, and only zero, means the node is offline.
	MetricConnectedReplicas = "tunnel_connected_replicas"
	// MetricRosterVersion is the roster version in force on this node.
	MetricRosterVersion = "tunnel_roster_version"
	// MetricReconnectsTotal counts reconnect attempts by ReconnectReason.
	MetricReconnectsTotal = "tunnel_reconnects_total"
	// MetricHeartbeatRTTSeconds observes the round trip of acknowledged
	// Control heartbeats.
	MetricHeartbeatRTTSeconds = "tunnel_control_heartbeat_rtt_seconds"
	// MetricSlotsTotal is the slot occupancy gauge, by class and by
	// SlotStateIdle/SlotStateBusy.
	MetricSlotsTotal = "tunnel_slots_total"
	// MetricSlotAcquireFailuresTotal counts slots that could not be brought
	// into service: the stream would not open, or Ready would not send.
	MetricSlotAcquireFailuresTotal = "tunnel_slot_acquire_failures_total"
	// MetricLimiterRejectionsTotal counts requests the per-instance hard
	// quota refused after the slot soft quota had already let them in. It is
	// the calibration signal for node_total.
	MetricLimiterRejectionsTotal = "tunnel_limiter_rejections_total"
	// MetricRequestsTotal counts finished requests by operation and Result.
	MetricRequestsTotal = "tunnel_requests_total"
	// MetricRequestDurationSeconds observes whole-request latency.
	MetricRequestDurationSeconds = "tunnel_request_duration_seconds"
	// MetricStreamFirstEventSeconds observes the tunnel-side TTFT: from the
	// arrival of RequestHeaders to the first response frame of a
	// progressive operation — a token stream, a workflow event stream or an
	// artifact body. Its difference with the Gateway's end-to-end TTFT is what
	// separates "slow tunnel" from "slow model".
	MetricStreamFirstEventSeconds = "tunnel_stream_first_event_seconds"
	// MetricFrameBytes observes serialized data-plane frame sizes by
	// direction.
	MetricFrameBytes = "tunnel_frame_bytes"
	// MetricCancelTotal counts cancelled requests by CancelReason.
	MetricCancelTotal = "tunnel_cancel_total"
)

// Label keys. The set is closed: a metric carries some subset of these and
// never a key invented at a call site.
const (
	LabelNodeID    = "node_id"
	LabelReplicaID = "replica_id"
	LabelRuntimeID = "runtime_id"
	LabelClass     = "class"
	LabelState     = "state"
	LabelOperation = "operation"
	LabelResult    = "result"
	LabelReason    = "reason"
	LabelDirection = "direction"
)

// NodeScopedMetrics are the three instruments that carry node_id but no
// replica_id, because they describe the node as a whole rather than one link:
// the count of usable links, the roster version the node has applied, and the
// per-instance concurrency gate, which is one node-wide quota shared by every
// replica. Attributing a limiter rejection to whichever replica happened to
// lose the race would be a fiction.
var NodeScopedMetrics = []string{
	MetricConnectedReplicas,
	MetricRosterVersion,
	MetricLimiterRejectionsTotal,
}

// Result is the outcome label on MetricRequestsTotal. It is an alias for
// tunnelwire.Result, where the classification now lives so that both ends of
// the tunnel label one request identically.
//
// Result 是 MetricRequestsTotal 的结果标签。它是 tunnelwire.Result 的别名——分类
// 逻辑已移到那里，好让隧道两端对同一个请求打出相同的标签。
type Result = tunnelwire.Result

// The six result values, re-exported so this package's callers and tests keep
// naming them through the tunnel package they already import.
//
// 六个结果取值，在此转出，好让本包的调用方与测试继续通过它们本就导入的 tunnel 包
// 来称呼它们。
const (
	ResultSuccess       = tunnelwire.ResultSuccess
	ResultClientError   = tunnelwire.ResultClientError
	ResultUpstreamError = tunnelwire.ResultUpstreamError
	ResultTimeout       = tunnelwire.ResultTimeout
	ResultCancelled     = tunnelwire.ResultCancelled
	ResultBackpressure  = tunnelwire.ResultBackpressure
)

// ReconnectReason is the reason label on MetricReconnectsTotal. It is a
// deliberately coarse classification of the error that ended a connection:
// the error's message may quote an endpoint or a server response, so only
// this enumeration reaches the label.
type ReconnectReason string

// The reconnect reasons.
const (
	// ReconnectTransport covers a dial failure, a broken stream and a replica
	// that stopped answering heartbeats.
	ReconnectTransport ReconnectReason = "transport_error"
	// ReconnectProtocol covers a replica that spoke the protocol wrongly.
	ReconnectProtocol ReconnectReason = "protocol_error"
	// ReconnectTimeout covers a handshake or an operation that ran out of
	// time.
	ReconnectTimeout ReconnectReason = "timeout"
	// ReconnectUnauthorized covers a rejected certificate. It is fatal, and
	// is counted once before the tunnel gives up.
	ReconnectUnauthorized ReconnectReason = "unauthorized"
	// ReconnectOther is everything else, and should stay near zero.
	ReconnectOther ReconnectReason = "other"
)

// CancelReason is the reason label on MetricCancelTotal. The Cancel frame
// carries a free-form reason string from the Gateway; it is logged at debug
// level and never used as a label, because nothing bounds what a replica may
// put in it.
type CancelReason string

// The cancel reasons.
const (
	// CancelByGateway is a Cancel frame naming the running request.
	CancelByGateway CancelReason = "gateway_cancel"
	// CancelSlotClosed is the slot itself going away underneath a request:
	// rotation, a protocol fault, a reconnect.
	CancelSlotClosed CancelReason = "slot_closed"
)

// Slot state label values on MetricSlotsTotal.
const (
	SlotStateIdle = "idle"
	SlotStateBusy = "busy"
)

// Frame direction label values on MetricFrameBytes.
const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// unknownReplica stands in for replica_id between the dial and HelloAck,
// which is the one window in which a tunnel has state to report but no
// replica identity yet. It keeps the label present and bounded rather than
// letting an empty string through.
const unknownReplica = "unknown"

// Metric renders a connection state as the gauge value README.md documents:
// 0 disconnected, 1 connecting, 2 connected, 3 draining, 4 retired,
// 5 failed.
//
// Two pairs collapse. StateReconnecting reports as connecting, because from a
// dashboard's point of view both mean "no usable link, one being built";
// MetricReconnectsTotal is where a reconnect is actually visible.
// StateServing reports as connected, because the two differ only in whether
// slots are parked and MetricSlotsTotal already reports that — collapsing
// them keeps "2 means this replica can be dispatched to" true.
func (s State) Metric() float64 {
	switch s {
	case StateConnecting, StateReconnecting:
		return 1
	case StateConnected, StateServing:
		return 2
	case StateDraining:
		return 3
	case StateRetired:
		return 4
	case StateFailed:
		return 5
	default:
		return 0
	}
}

// Descriptions is this package's metric catalogue, for a service to hand to
// metrics.New. It carries the help text and bucket choices for the thirteen
// instruments named above, so an Agent's /metrics endpoint documents them
// without main.go restating anything this file already knows.
//
// Descriptions 是本包的指标目录，供服务交给 metrics.New。它带着上面十三个仪器的
// help 文本与分桶选择，因此 Agent 的 /metrics 端点能为它们附上说明，而 main.go
// 不必复述任何本文件已经知道的东西。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricConnectionState: {
			Kind: metrics.KindGauge,
			Help: "Tunnel state: 0 disconnected, 1 connecting, 2 connected, 3 draining, 4 retired, 5 failed.",
		},
		MetricConnectedReplicas: {
			Kind: metrics.KindGauge,
			Help: "Replicas this node currently holds a usable tunnel to. Zero, and only zero, means offline.",
		},
		MetricRosterVersion: {
			Kind: metrics.KindGauge,
			Help: "Replica roster version this node has applied.",
		},
		MetricReconnectsTotal: {
			Kind: metrics.KindCounter,
			Help: "Reconnect attempts, by the classified reason the previous connection ended.",
		},
		MetricHeartbeatRTTSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Round trip of acknowledged Control heartbeats.",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricSlotsTotal: {
			Kind: metrics.KindGauge,
			Help: "Slots this node holds, by class and state. Slots still opening count as neither.",
		},
		MetricSlotAcquireFailuresTotal: {
			Kind: metrics.KindCounter,
			Help: "Slots that could not be brought into service: the stream would not open, or Ready would not send.",
		},
		MetricLimiterRejectionsTotal: {
			Kind: metrics.KindCounter,
			Help: "Requests the per-instance hard quota refused after the slot soft quota had already admitted them.",
		},
		MetricRequestsTotal: {
			Kind: metrics.KindCounter,
			Help: "Finished requests, by operation and result.",
		},
		MetricRequestDurationSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Whole-request latency, by operation.",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricStreamFirstEventSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Time from RequestHeaders arriving to the first response frame of a progressive operation.",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricFrameBytes: {
			Kind:    metrics.KindHistogram,
			Help:    "Serialized data-plane frame size, by direction.",
			Buckets: metrics.BytesBuckets(),
		},
		MetricCancelTotal: {
			Kind: metrics.KindCounter,
			Help: "Cancelled requests, by reason.",
		},
	}
}

// recorder is the tunnel's typed view of runtime.Metrics: every recording site
// in the package calls one of its methods, so the label vocabulary above is
// enforced by the type system rather than by review.
//
// The zero value is not usable; newRecorder builds one, and a nil sink is
// replaced by a discard so no caller needs a nil check.
//
// recorder 是隧道对 runtime.Metrics 的类型化视图：包内每个记录点都调用它的某个方法，
// 因此上面那套标签词汇由类型系统而非评审来保证。
//
// 零值不可用；newRecorder 负责构造，nil 的 sink 会被替换成丢弃实现，因此调用方无需
// 做 nil 判断。
type recorder struct {
	sink      runtime.Metrics
	nodeID    string
	replicaID string
}

// newRecorder returns a recorder for one node, optionally already bound to a
// replica. A nil sink discards everything, which is what an agent with no
// metrics backend configured gets.
func newRecorder(sink runtime.Metrics, nodeID, replicaID string) *recorder {
	if sink == nil {
		sink = discardMetrics{}
	}
	if replicaID == "" {
		replicaID = unknownReplica
	}
	return &recorder{sink: sink, nodeID: nodeID, replicaID: replicaID}
}

// forReplica returns a copy bound to replicaID, for use once HelloAck has
// named the replica this tunnel reached.
func (m *recorder) forReplica(replicaID string) *recorder {
	return newRecorder(m.sink, m.nodeID, replicaID)
}

// node returns the labels of a node-scoped metric.
func (m *recorder) node() map[string]string {
	return map[string]string{LabelNodeID: m.nodeID}
}

// link returns the labels of a per-tunnel metric, plus the extra pairs the
// call site adds.
func (m *recorder) link(extra ...string) map[string]string {
	labels := map[string]string{LabelNodeID: m.nodeID, LabelReplicaID: m.replicaID}
	for i := 0; i+1 < len(extra); i += 2 {
		labels[extra[i]] = extra[i+1]
	}
	return labels
}

// ConnectionState publishes this tunnel's state.
func (m *recorder) ConnectionState(state State) {
	m.sink.Gauge(MetricConnectionState, m.link()).Set(state.Metric())
}

// ConnectedReplicas publishes how many tunnels can take a request.
func (m *recorder) ConnectedReplicas(n int) {
	m.sink.Gauge(MetricConnectedReplicas, m.node()).Set(float64(n))
}

// RosterVersion publishes the roster version this node has applied.
func (m *recorder) RosterVersion(version int64) {
	m.sink.Gauge(MetricRosterVersion, m.node()).Set(float64(version))
}

// Reconnect counts one reconnect attempt.
func (m *recorder) Reconnect(reason ReconnectReason) {
	m.sink.Counter(MetricReconnectsTotal, m.link(LabelReason, string(reason))).Add(1)
}

// HeartbeatRTT observes one acknowledged heartbeat's round trip.
func (m *recorder) HeartbeatRTT(d time.Duration) {
	m.sink.Histogram(MetricHeartbeatRTTSeconds, m.link()).Observe(d.Seconds())
}

// Slots publishes one class's occupancy. Opening slots are counted as
// neither: they cannot take a request yet, and reporting them as idle would
// make the gauge disagree with what the pool will actually serve.
func (m *recorder) Slots(class tunnelv1.SlotClass, idle, busy int) {
	label := slotClassLabel(class)
	m.sink.Gauge(MetricSlotsTotal, m.link(LabelClass, label, LabelState, SlotStateIdle)).Set(float64(idle))
	m.sink.Gauge(MetricSlotsTotal, m.link(LabelClass, label, LabelState, SlotStateBusy)).Set(float64(busy))
}

// SlotAcquireFailure counts one slot that could not be brought into service.
func (m *recorder) SlotAcquireFailure(class tunnelv1.SlotClass) {
	m.sink.Counter(MetricSlotAcquireFailuresTotal, m.link(LabelClass, slotClassLabel(class))).Add(1)
}

// LimiterRejection counts one request the instance's hard quota refused.
func (m *recorder) LimiterRejection(runtimeID string) {
	labels := m.node()
	labels[LabelRuntimeID] = runtimeID
	m.sink.Counter(MetricLimiterRejectionsTotal, labels).Add(1)
}

// Request records one finished request: its outcome and its duration.
func (m *recorder) Request(op tunnelv1.Operation, result Result, d time.Duration) {
	label := operationLabel(op)
	m.sink.Counter(MetricRequestsTotal, m.link(LabelOperation, label, LabelResult, string(result))).Add(1)
	m.sink.Histogram(MetricRequestDurationSeconds, m.link(LabelOperation, label)).Observe(d.Seconds())
}

// StreamFirstEvent observes the tunnel-side TTFT of one progressive
// response.
func (m *recorder) StreamFirstEvent(op tunnelv1.Operation, d time.Duration) {
	m.sink.Histogram(MetricStreamFirstEventSeconds, m.link(LabelOperation, operationLabel(op))).Observe(d.Seconds())
}

// FrameBytes observes one data-plane frame's serialized size.
func (m *recorder) FrameBytes(direction string, n int) {
	m.sink.Histogram(MetricFrameBytes, m.link(LabelDirection, direction)).Observe(float64(n))
}

// Cancel counts one cancelled request.
func (m *recorder) Cancel(reason CancelReason) {
	m.sink.Counter(MetricCancelTotal, m.link(LabelReason, string(reason))).Add(1)
}

// operationLabel renders an Operation for a label. An enum value this build
// does not know becomes "unknown" rather than a number: an older Agent facing
// a newer Gateway must not turn unrecognized traffic into unbounded label
// values.
func operationLabel(op tunnelv1.Operation) string {
	if name, ok := tunnelv1.Operation_name[int32(op)]; ok {
		return name
	}
	return "unknown"
}

// reconnectReasonFor classifies the error that ended a connection.
func reconnectReasonFor(err error) ReconnectReason {
	if err == nil {
		return ReconnectOther
	}
	var re *runtime.RuntimeError
	if !errors.As(err, &re) {
		return ReconnectOther
	}
	switch re.Code {
	case runtime.ErrorConnection:
		return ReconnectTransport
	case runtime.ErrorProtocol:
		return ReconnectProtocol
	case runtime.ErrorTimeout:
		return ReconnectTimeout
	case runtime.ErrorUnauthorized:
		return ReconnectUnauthorized
	default:
		return ReconnectOther
	}
}

// discardMetrics is the sink used when no metrics backend is configured. It
// exists so recording sites stay unconditional: a nil check at every call
// site is a nil check that will eventually be forgotten.
type discardMetrics struct{}

func (discardMetrics) Counter(string, map[string]string) runtime.Counter {
	return discardInstrument{}
}

func (discardMetrics) Gauge(string, map[string]string) runtime.Gauge {
	return discardInstrument{}
}

func (discardMetrics) Histogram(string, map[string]string) runtime.Histogram {
	return discardInstrument{}
}

// discardInstrument drops every sample.
type discardInstrument struct{}

func (discardInstrument) Add(float64)     {}
func (discardInstrument) Set(float64)     {}
func (discardInstrument) Observe(float64) {}

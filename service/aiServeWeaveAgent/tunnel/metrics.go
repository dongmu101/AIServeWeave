package tunnel

import (
	"context"
	"errors"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
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

// Result is the outcome label on MetricRequestsTotal. It takes the same six
// values as the runtime package's result convention, so a request can be
// followed across the two layers without translating.
type Result string

// The six result values. Raw status codes are deliberately not among them.
const (
	ResultSuccess       Result = "success"
	ResultClientError   Result = "client_error"
	ResultUpstreamError Result = "upstream_error"
	ResultTimeout       Result = "timeout"
	ResultCancelled     Result = "cancelled"
	ResultBackpressure  Result = "backpressure"
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

// metrics is the tunnel's typed view of runtime.Metrics: every recording site
// in the package calls one of its methods, so the label vocabulary above is
// enforced by the type system rather than by review.
//
// The zero value is not usable; newMetrics builds one, and a nil sink is
// replaced by a discard so no caller needs a nil check.
type metrics struct {
	sink      runtime.Metrics
	nodeID    string
	replicaID string
}

// newMetrics returns a recorder for one node, optionally already bound to a
// replica. A nil sink discards everything, which is what an agent with no
// metrics backend configured gets.
func newMetrics(sink runtime.Metrics, nodeID, replicaID string) *metrics {
	if sink == nil {
		sink = discardMetrics{}
	}
	if replicaID == "" {
		replicaID = unknownReplica
	}
	return &metrics{sink: sink, nodeID: nodeID, replicaID: replicaID}
}

// forReplica returns a copy bound to replicaID, for use once HelloAck has
// named the replica this tunnel reached.
func (m *metrics) forReplica(replicaID string) *metrics {
	return newMetrics(m.sink, m.nodeID, replicaID)
}

// node returns the labels of a node-scoped metric.
func (m *metrics) node() map[string]string {
	return map[string]string{LabelNodeID: m.nodeID}
}

// link returns the labels of a per-tunnel metric, plus the extra pairs the
// call site adds.
func (m *metrics) link(extra ...string) map[string]string {
	labels := map[string]string{LabelNodeID: m.nodeID, LabelReplicaID: m.replicaID}
	for i := 0; i+1 < len(extra); i += 2 {
		labels[extra[i]] = extra[i+1]
	}
	return labels
}

// ConnectionState publishes this tunnel's state.
func (m *metrics) ConnectionState(state State) {
	m.sink.Gauge(MetricConnectionState, m.link()).Set(state.Metric())
}

// ConnectedReplicas publishes how many tunnels can take a request.
func (m *metrics) ConnectedReplicas(n int) {
	m.sink.Gauge(MetricConnectedReplicas, m.node()).Set(float64(n))
}

// RosterVersion publishes the roster version this node has applied.
func (m *metrics) RosterVersion(version int64) {
	m.sink.Gauge(MetricRosterVersion, m.node()).Set(float64(version))
}

// Reconnect counts one reconnect attempt.
func (m *metrics) Reconnect(reason ReconnectReason) {
	m.sink.Counter(MetricReconnectsTotal, m.link(LabelReason, string(reason))).Add(1)
}

// HeartbeatRTT observes one acknowledged heartbeat's round trip.
func (m *metrics) HeartbeatRTT(d time.Duration) {
	m.sink.Histogram(MetricHeartbeatRTTSeconds, m.link()).Observe(d.Seconds())
}

// Slots publishes one class's occupancy. Opening slots are counted as
// neither: they cannot take a request yet, and reporting them as idle would
// make the gauge disagree with what the pool will actually serve.
func (m *metrics) Slots(class tunnelv1.SlotClass, idle, busy int) {
	label := slotClassLabel(class)
	m.sink.Gauge(MetricSlotsTotal, m.link(LabelClass, label, LabelState, SlotStateIdle)).Set(float64(idle))
	m.sink.Gauge(MetricSlotsTotal, m.link(LabelClass, label, LabelState, SlotStateBusy)).Set(float64(busy))
}

// SlotAcquireFailure counts one slot that could not be brought into service.
func (m *metrics) SlotAcquireFailure(class tunnelv1.SlotClass) {
	m.sink.Counter(MetricSlotAcquireFailuresTotal, m.link(LabelClass, slotClassLabel(class))).Add(1)
}

// LimiterRejection counts one request the instance's hard quota refused.
func (m *metrics) LimiterRejection(runtimeID string) {
	labels := m.node()
	labels[LabelRuntimeID] = runtimeID
	m.sink.Counter(MetricLimiterRejectionsTotal, labels).Add(1)
}

// Request records one finished request: its outcome and its duration.
func (m *metrics) Request(op tunnelv1.Operation, result Result, d time.Duration) {
	label := operationLabel(op)
	m.sink.Counter(MetricRequestsTotal, m.link(LabelOperation, label, LabelResult, string(result))).Add(1)
	m.sink.Histogram(MetricRequestDurationSeconds, m.link(LabelOperation, label)).Observe(d.Seconds())
}

// StreamFirstEvent observes the tunnel-side TTFT of one progressive
// response.
func (m *metrics) StreamFirstEvent(op tunnelv1.Operation, d time.Duration) {
	m.sink.Histogram(MetricStreamFirstEventSeconds, m.link(LabelOperation, operationLabel(op))).Observe(d.Seconds())
}

// FrameBytes observes one data-plane frame's serialized size.
func (m *metrics) FrameBytes(direction string, n int) {
	m.sink.Histogram(MetricFrameBytes, m.link(LabelDirection, direction)).Observe(float64(n))
}

// Cancel counts one cancelled request.
func (m *metrics) Cancel(reason CancelReason) {
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

// resultFor maps a request outcome onto the six-value result convention. It
// reads the error's code, never its message.
func resultFor(err error) Result {
	if err == nil {
		return ResultSuccess
	}

	var re *runtime.RuntimeError
	code := runtime.ErrorUpstream
	if errors.As(err, &re) {
		code = re.Code
	} else {
		code, _ = tunnelwire.ClassifyBareError(err)
		if errors.Is(err, context.Canceled) {
			return ResultCancelled
		}
	}

	switch code {
	case runtime.ErrorRateLimited, runtime.ErrorBackpressure:
		return ResultBackpressure
	case runtime.ErrorTimeout:
		return ResultTimeout
	case runtime.ErrorInvalidConfig, runtime.ErrorUnauthorized, runtime.ErrorCapability,
		runtime.ErrorProtocol, runtime.ErrorResponseTooLarge, runtime.ErrorCancelUnsupported,
		runtime.ErrorProbeMismatch:
		return ResultClientError
	case runtime.ErrorConnection:
		// A link teardown is how a cancelled request surfaces: the tunnel's
		// error set has no cancellation code, so the cause is what separates
		// "the user hung up" from "the connection broke".
		if errors.Is(err, context.Canceled) {
			return ResultCancelled
		}
		return ResultUpstreamError
	default:
		return ResultUpstreamError
	}
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

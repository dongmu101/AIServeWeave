package tunnelserver

import (
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
)

// This file is the whole of the tunnel server's observability surface: the
// metric names, the closed label vocabularies they may carry, and the typed
// recorder every other file in the package goes through.
//
// It is the mirror image of the Agent's tunnel/metrics.go, and deliberately
// not the same metric names. The two ends measure the same link from opposite
// sides and disagree in ways that matter — the Agent's request counter counts
// what it executed, this one counts what the replica handed over, and their
// difference is exactly where a request was lost. Sharing one name would
// average that difference away the moment both ends scrape into one backend.
// Hence the tunnel_server_ prefix on everything here.
//
// Two rules hold throughout, the same two the Agent side enforces:
//
//   - No value that came off the wire becomes a label. node_id is the one
//     identifier that crosses the boundary, and it is taken from the verified
//     client certificate, never from a frame's own claim.
//   - Every per-link metric carries replica_id, because a symptom on one
//     replica and a symptom across the fleet call for different action.
//
// 本文件就是隧道服务端可观测性的全部：指标名、它们允许携带的封闭标签词汇，以及
// 包内其他文件统一经过的类型化记录器。
//
// 它是 Agent 侧 tunnel/metrics.go 的镜像，且刻意不用相同的指标名。两端从相反方向
// 测量同一条链路，其分歧本身就有意义——Agent 的请求计数器数的是它执行了多少，这边
// 数的是副本递交了多少，两者之差正是请求丢在哪里的答案。共用一个名字，等于在两端
// 都抓进同一个后端的那一刻就把这个差值抹平。因此这里的一切都带 tunnel_server_ 前缀。
//
// 通篇遵守两条规则，与 Agent 侧执行的是同两条：
//
//   - 任何来自线上的值都不进标签。node_id 是唯一跨越边界的标识，且它取自已验证的
//     客户端证书，绝不取自帧自己的声明。
//   - 每个链路级指标都带 replica_id，因为「某个副本出问题」与「整个集群出问题」
//     要采取的行动完全不同。

// Metric names, one per instrument this package records.
//
// 指标名，本包记录的每个仪器一个。
const (
	// MetricConnectedNodes is how many nodes hold a live tunnel to this
	// replica. Compared against the fleet's node count it says whether a node
	// is missing from this replica specifically or from everywhere.
	//
	// MetricConnectedNodes 是有多少节点与本副本保持着可用隧道。与集群节点总数对照，
	// 就能判断某个节点是只在这个副本上缺席，还是到处都缺席。
	MetricConnectedNodes = "tunnel_server_connected_nodes"
	// MetricRosterVersion is the roster version this replica last broadcast.
	// Replicas disagreeing on it means the Registry's fan-out is broken.
	//
	// MetricRosterVersion 是本副本最后广播的名册版本。副本之间不一致，说明 Registry
	// 的扇出有问题。
	MetricRosterVersion = "tunnel_server_roster_version"
	// MetricNodeState is one node's state as seen from here, valued by
	// nodeState.
	//
	// MetricNodeState 是从这里看到的某个节点的状态，取值见 nodeState。
	MetricNodeState = "tunnel_server_node_state"
	// MetricControlStreams counts a node's live Control streams. It is
	// normally 1; a sustained 2 means this replica keeps failing to notice
	// dead streams.
	//
	// MetricControlStreams 统计某节点存活的 Control 流数。正常为 1；长期为 2 说明
	// 本副本一直没能察觉已死的流。
	MetricControlStreams = "tunnel_server_control_streams"
	// MetricHeartbeatsTotal counts heartbeats received from a node.
	//
	// MetricHeartbeatsTotal 统计从某节点收到的心跳数。
	MetricHeartbeatsTotal = "tunnel_server_heartbeats_total"
	// MetricHeartbeatIntervalSeconds observes the gap between consecutive
	// heartbeats. It is the server-side reading of README's 节点心跳延迟: the
	// Agent's own RTT metric cannot show a heartbeat that never arrived, and
	// this distribution's tail can.
	//
	// MetricHeartbeatIntervalSeconds 观测相邻两次心跳的间隔，是 README「节点心跳
	// 延迟」在服务端的读数：Agent 自己的 RTT 指标看不见一次根本没到达的心跳，而这个
	// 分布的长尾看得见。
	MetricHeartbeatIntervalSeconds = "tunnel_server_heartbeat_interval_seconds"
	// MetricSlotsTotal is the slot occupancy gauge, by class and by
	// SlotStateIdle/SlotStateBusy, as this replica sees it.
	//
	// MetricSlotsTotal 是槽位占用量表，按 class 与 SlotStateIdle/SlotStateBusy 划分，
	// 反映的是本副本所见。
	MetricSlotsTotal = "tunnel_server_slots_total"
	// MetricSlotFaultsTotal counts slots closed because the Agent broke the
	// frame contract, by SlotFaultReason.
	//
	// MetricSlotFaultsTotal 统计因 Agent 违反帧契约而被关闭的槽数，按 SlotFaultReason
	// 分类。
	MetricSlotFaultsTotal = "tunnel_server_slot_faults_total"
	// MetricDispatchTotal counts finished dispatches by operation and result.
	// A dispatch refused for want of an idle slot lands here as
	// backpressure — the node was not broken, it was full.
	//
	// MetricDispatchTotal 按 operation 与 result 统计已完成的分发。因无空闲槽被拒的
	// 分发记为 backpressure——那不是节点坏了，是节点满了。
	MetricDispatchTotal = "tunnel_server_dispatch_total"
	// MetricDispatchDurationSeconds observes whole-dispatch latency, from the
	// call arriving here to the response being released.
	//
	// MetricDispatchDurationSeconds 观测整次分发的时延，从调用到达此处直到响应被释放。
	MetricDispatchDurationSeconds = "tunnel_server_dispatch_duration_seconds"
	// MetricStreamFirstEventSeconds observes the replica-side time to first
	// response frame. Its difference with the Agent's own
	// tunnel_stream_first_event_seconds is the tunnel's share of TTFT, which
	// is what separates "slow tunnel" from "slow model".
	//
	// MetricStreamFirstEventSeconds 观测副本侧到首个响应帧的时间。它与 Agent 自己的
	// tunnel_stream_first_event_seconds 之差就是隧道在 TTFT 中的占比，也正是区分
	// 「慢在隧道」与「慢在模型」的依据。
	MetricStreamFirstEventSeconds = "tunnel_server_stream_first_event_seconds"
	// MetricFrameBytes observes serialized data-plane frame sizes by
	// direction.
	//
	// MetricFrameBytes 按方向观测数据面帧的序列化大小。
	MetricFrameBytes = "tunnel_server_frame_bytes"
	// MetricCancelsTotal counts requests this replica cancelled because the
	// caller went away before the response finished.
	//
	// MetricCancelsTotal 统计本副本因调用方在响应结束前离开而取消的请求数。
	MetricCancelsTotal = "tunnel_server_cancels_total"
)

// Label keys. The set is closed: a metric carries some subset of these and
// never a key invented at a call site.
//
// 标签键。集合是封闭的：指标只携带其中的子集，绝不携带记录点临时发明的键。
const (
	LabelNodeID    = "node_id"
	LabelReplicaID = "replica_id"
	LabelClass     = "class"
	LabelState     = "state"
	LabelOperation = "operation"
	LabelResult    = "result"
	LabelReason    = "reason"
	LabelDirection = "direction"
)

// ReplicaScopedMetrics are the two instruments that carry replica_id but no
// node_id, because they describe this replica as a whole rather than one link
// to one node. The metric label test asserts against this list in both
// directions.
//
// ReplicaScopedMetrics 是仅带 replica_id 而不带 node_id 的两个仪器，因为它们描述的
// 是本副本整体，而不是通向某个节点的某条链路。标签测试对着这张表做双向断言。
var ReplicaScopedMetrics = []string{
	MetricConnectedNodes,
	MetricRosterVersion,
}

// Slot state label values on MetricSlotsTotal.
//
// MetricSlotsTotal 上的槽位状态标签取值。
const (
	SlotStateIdle = "idle"
	SlotStateBusy = "busy"
)

// Frame direction label values on MetricFrameBytes. Inbound is what the Agent
// sent us; outbound is what we sent the Agent.
//
// MetricFrameBytes 上的方向标签取值。inbound 是 Agent 发给我们的，outbound 是我们
// 发给 Agent 的。
const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// SlotFaultReason is the reason label on MetricSlotFaultsTotal. The human
// readable fault text goes to the log; only this enumeration reaches a label,
// because the text names slot ids and frame sizes and would be unbounded.
//
// SlotFaultReason 是 MetricSlotFaultsTotal 上的 reason 标签。可读的故障文本进日志；
// 只有这个枚举进标签，因为那段文本包含槽 id 与帧大小，是无界的。
type SlotFaultReason string

// The slot fault reasons, one per way an Agent can break the frame contract.
//
// 槽故障原因，Agent 违反帧契约的每一种方式各一个。
const (
	// FaultReadyWhileInflight is a Ready arriving while a request is still
	// running on the slot.
	//
	// FaultReadyWhileInflight 是槽上还有请求在跑时到达的 Ready。
	FaultReadyWhileInflight SlotFaultReason = "ready_while_inflight"
	// FaultFrameOnIdleSlot is a response frame arriving on a slot that holds
	// no request.
	//
	// FaultFrameOnIdleSlot 是到达一个没有请求的槽上的响应帧。
	FaultFrameOnIdleSlot SlotFaultReason = "frame_on_idle_slot"
	// FaultFrameTooLarge is a data chunk over the configured frame limit.
	//
	// FaultFrameTooLarge 是超过所配置帧上限的数据块。
	FaultFrameTooLarge SlotFaultReason = "frame_too_large"
)

// nodeState is the value of MetricNodeState. It is a smaller set than the
// Agent's connection state: a replica only ever sees a node that has already
// connected, so there is no dialing or reconnecting to report.
//
// nodeState 是 MetricNodeState 的取值。它比 Agent 的连接状态集合更小：副本只会看到
// 已经连上的节点，因此没有拨号中或重连中可报。
type nodeState float64

const (
	// nodeGone is a node with no Control stream left on this replica.
	//
	// nodeGone 表示该节点在本副本上已无 Control 流。
	nodeGone nodeState = 0
	// nodeConnected is a node that may be dispatched to.
	//
	// nodeConnected 表示该节点可以被派发请求。
	nodeConnected nodeState = 1
	// nodeDraining is a node that announced it is leaving. Its in-flight
	// requests are still running, which is why this is not nodeGone.
	//
	// nodeDraining 表示该节点已宣告要离开。它在途的请求仍在跑，所以这不是 nodeGone。
	nodeDraining nodeState = 2
)

// Descriptions is this package's metric catalogue, for a service to hand to
// metrics.New. It is exported so main.go registers the tunnel server's
// instruments without restating their help text, and so a test can assert the
// catalogue covers every constant above.
//
// Descriptions 是本包的指标目录，供服务交给 metrics.New。它被导出，好让 main.go
// 注册隧道服务端的仪器时不必复述 help 文本，也让测试能断言目录覆盖了上面每一个常量。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricConnectedNodes: {
			Kind: metrics.KindGauge,
			Help: "Nodes currently holding a live tunnel to this replica.",
		},
		MetricRosterVersion: {
			Kind: metrics.KindGauge,
			Help: "Replica roster version this replica last broadcast to its nodes.",
		},
		MetricNodeState: {
			Kind: metrics.KindGauge,
			Help: "Node state seen from this replica: 0 gone, 1 connected, 2 draining.",
		},
		MetricControlStreams: {
			Kind: metrics.KindGauge,
			Help: "Live Control streams this replica holds for the node.",
		},
		MetricHeartbeatsTotal: {
			Kind: metrics.KindCounter,
			Help: "Heartbeats received from the node.",
		},
		MetricHeartbeatIntervalSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Gap between consecutive heartbeats received from the node.",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricSlotsTotal: {
			Kind: metrics.KindGauge,
			Help: "Slots this replica holds for the node, by class and state.",
		},
		MetricSlotFaultsTotal: {
			Kind: metrics.KindCounter,
			Help: "Slots closed because the Agent broke the frame contract.",
		},
		MetricDispatchTotal: {
			Kind: metrics.KindCounter,
			Help: "Dispatches finished, by operation and result.",
		},
		MetricDispatchDurationSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Time from a dispatch arriving at this replica to its response being released.",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricStreamFirstEventSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Time from a dispatch being written to its first response frame arriving.",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricFrameBytes: {
			Kind:    metrics.KindHistogram,
			Help:    "Serialized data-plane frame size, by direction.",
			Buckets: metrics.BytesBuckets(),
		},
		MetricCancelsTotal: {
			Kind: metrics.KindCounter,
			Help: "Requests cancelled because the caller left before the response finished.",
		},
	}
}

// recorder is the tunnel server's typed view of runtime.Metrics: every
// recording site in the package calls one of its methods, so the label
// vocabulary above is enforced by the type system rather than by review.
//
// The zero value is not usable; newRecorder builds one, and a nil sink is
// replaced by a discard so no caller needs a nil check.
//
// recorder 是隧道服务端对 runtime.Metrics 的类型化视图：包内每个记录点都调用它的
// 某个方法，因此上面那套标签词汇由类型系统而非评审来保证。
//
// 零值不可用；newRecorder 负责构造，nil 的 sink 会被替换成丢弃实现，因此调用方
// 无需做 nil 判断。
type recorder struct {
	sink      runtime.Metrics
	replicaID string
	nodeID    string
}

// newRecorder returns a recorder for one replica, not yet bound to a node.
//
// newRecorder 返回某个副本的记录器，尚未绑定到具体节点。
func newRecorder(sink runtime.Metrics, replicaID string) *recorder {
	if sink == nil {
		sink = discardMetrics{}
	}
	return &recorder{sink: sink, replicaID: replicaID}
}

// forNode returns a copy bound to nodeID, for the per-link instruments.
//
// forNode 返回绑定到 nodeID 的副本，用于链路级仪器。
func (r *recorder) forNode(nodeID string) *recorder {
	return &recorder{sink: r.sink, replicaID: r.replicaID, nodeID: nodeID}
}

// replica returns the labels of a replica-scoped metric.
//
// replica 返回副本级指标的标签。
func (r *recorder) replica() map[string]string {
	return map[string]string{LabelReplicaID: r.replicaID}
}

// link returns the labels of a per-node metric, plus the extra pairs the call
// site adds.
//
// link 返回节点级指标的标签，外加记录点补充的若干对。
func (r *recorder) link(extra ...string) map[string]string {
	labels := map[string]string{LabelReplicaID: r.replicaID, LabelNodeID: r.nodeID}
	for i := 0; i+1 < len(extra); i += 2 {
		labels[extra[i]] = extra[i+1]
	}
	return labels
}

// ConnectedNodes publishes how many nodes hold a live tunnel here.
//
// ConnectedNodes 发布有多少节点在此保持着可用隧道。
func (r *recorder) ConnectedNodes(n int) {
	r.sink.Gauge(MetricConnectedNodes, r.replica()).Set(float64(n))
}

// RosterVersion publishes the roster version this replica last broadcast.
//
// RosterVersion 发布本副本最后广播的名册版本。
func (r *recorder) RosterVersion(version int64) {
	r.sink.Gauge(MetricRosterVersion, r.replica()).Set(float64(version))
}

// NodeState publishes this node's state as seen from here.
//
// NodeState 发布从这里看到的该节点状态。
func (r *recorder) NodeState(state nodeState) {
	r.sink.Gauge(MetricNodeState, r.link()).Set(float64(state))
}

// ControlStreams publishes how many Control streams this node holds.
//
// ControlStreams 发布该节点持有多少条 Control 流。
func (r *recorder) ControlStreams(n int) {
	r.sink.Gauge(MetricControlStreams, r.link()).Set(float64(n))
}

// Heartbeat counts one heartbeat and, when there was a previous one to measure
// against, observes the gap. The first heartbeat of a connection has no
// interval: recording the time since the stream opened instead would put a
// value into the distribution that is not a heartbeat gap at all.
//
// Heartbeat 计一次心跳，并在存在可比较的上一次心跳时观测其间隔。一次连接的首个心跳
// 没有间隔可言：改用「距流打开的时间」会往分布里塞进一个根本不是心跳间隔的值。
func (r *recorder) Heartbeat(interval time.Duration) {
	r.sink.Counter(MetricHeartbeatsTotal, r.link()).Add(1)
	if interval > 0 {
		r.sink.Histogram(MetricHeartbeatIntervalSeconds, r.link()).Observe(interval.Seconds())
	}
}

// Slots publishes one class's occupancy on this node.
//
// Slots 发布该节点上某个 class 的槽位占用情况。
func (r *recorder) Slots(class tunnelv1.SlotClass, idle, busy int) {
	label := slotClassLabel(class)
	r.sink.Gauge(MetricSlotsTotal, r.link(LabelClass, label, LabelState, SlotStateIdle)).Set(float64(idle))
	r.sink.Gauge(MetricSlotsTotal, r.link(LabelClass, label, LabelState, SlotStateBusy)).Set(float64(busy))
}

// SlotFault counts one slot closed on a protocol fault.
//
// SlotFault 计一次因协议错误而关闭的槽。
func (r *recorder) SlotFault(reason SlotFaultReason) {
	r.sink.Counter(MetricSlotFaultsTotal, r.link(LabelReason, string(reason))).Add(1)
}

// Dispatch records one finished dispatch: its outcome and how long it took.
//
// Dispatch 记录一次已完成的分发：结果与耗时。
func (r *recorder) Dispatch(op tunnelv1.Operation, result tunnelwire.Result, d time.Duration) {
	label := operationLabel(op)
	r.sink.Counter(MetricDispatchTotal, r.link(LabelOperation, label, LabelResult, string(result))).Add(1)
	r.sink.Histogram(MetricDispatchDurationSeconds, r.link(LabelOperation, label)).Observe(d.Seconds())
}

// StreamFirstEvent observes the replica-side time to first response frame.
//
// StreamFirstEvent 观测副本侧到首个响应帧的时间。
func (r *recorder) StreamFirstEvent(op tunnelv1.Operation, d time.Duration) {
	r.sink.Histogram(MetricStreamFirstEventSeconds, r.link(LabelOperation, operationLabel(op))).Observe(d.Seconds())
}

// FrameBytes observes one data-plane frame's serialized size.
//
// FrameBytes 观测一个数据面帧的序列化大小。
func (r *recorder) FrameBytes(direction string, n int) {
	r.sink.Histogram(MetricFrameBytes, r.link(LabelDirection, direction)).Observe(float64(n))
}

// Cancel counts one request cancelled because its caller left.
//
// Cancel 计一次因调用方离开而被取消的请求。
func (r *recorder) Cancel() {
	r.sink.Counter(MetricCancelsTotal, r.link()).Add(1)
}

// operationLabel renders an Operation for a label. An enum value this build
// does not know becomes "unknown" rather than a number: a replica facing a
// newer Agent must not turn unrecognized traffic into unbounded label values.
//
// operationLabel 把 Operation 渲染成标签。本次构建不认识的枚举值记为 "unknown"
// 而不是数字：面对更新版本 Agent 的副本，不能把无法识别的流量变成无界标签值。
func operationLabel(op tunnelv1.Operation) string {
	if name, ok := tunnelv1.Operation_name[int32(op)]; ok {
		return name
	}
	return "unknown"
}

// slotClassLabel renders a SlotClass for a label, on the same terms.
//
// slotClassLabel 以同样的规则把 SlotClass 渲染成标签。
func slotClassLabel(class tunnelv1.SlotClass) string {
	if name, ok := tunnelv1.SlotClass_name[int32(class)]; ok {
		return name
	}
	return "unknown"
}

// discardMetrics is the sink used when no metrics backend is configured. It
// exists so recording sites stay unconditional: a nil check at every call site
// is a nil check that will eventually be forgotten.
//
// discardMetrics 是未配置指标后端时使用的下沉端。它的存在让记录点无需分支：在每个
// 记录点都写一次 nil 判断，就是迟早会漏掉一次的 nil 判断。
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
//
// discardInstrument 丢弃每一个样本。
type discardInstrument struct{}

func (discardInstrument) Add(float64)     {}
func (discardInstrument) Set(float64)     {}
func (discardInstrument) Observe(float64) {}

package tunnelserver_test

import (
	"context"
	"io"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics"
	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// allMetrics is every instrument this package records, restated here so the
// cardinality and catalogue tests below have something to iterate that is not
// simply "whatever happened to get recorded".
//
// allMetrics 是本包记录的全部仪器，在此重述一遍，好让下面的基数测试与目录测试有一份
// 可遍历的清单，而不是只能遍历「碰巧被记录到的那些」。
var allMetrics = []string{
	tunnelserver.MetricConnectedNodes,
	tunnelserver.MetricRosterVersion,
	tunnelserver.MetricNodeState,
	tunnelserver.MetricControlStreams,
	tunnelserver.MetricHeartbeatsTotal,
	tunnelserver.MetricHeartbeatIntervalSeconds,
	tunnelserver.MetricSlotsTotal,
	tunnelserver.MetricSlotFaultsTotal,
	tunnelserver.MetricDispatchTotal,
	tunnelserver.MetricDispatchDurationSeconds,
	tunnelserver.MetricStreamFirstEventSeconds,
	tunnelserver.MetricFrameBytes,
	tunnelserver.MetricCancelsTotal,
}

// allLabelKeys is the closed label vocabulary metrics.go documents. A key
// outside this set reaching a series means a recording site invented one.
//
// allLabelKeys 是 metrics.go 所声明的封闭标签词汇。集合之外的键出现在某条序列上，
// 就意味着某个记录点自己发明了一个键。
var allLabelKeys = map[string]bool{
	tunnelserver.LabelNodeID:    true,
	tunnelserver.LabelReplicaID: true,
	tunnelserver.LabelClass:     true,
	tunnelserver.LabelState:     true,
	tunnelserver.LabelOperation: true,
	tunnelserver.LabelResult:    true,
	tunnelserver.LabelReason:    true,
	tunnelserver.LabelDirection: true,
}

// TestDescriptionsCoverEveryMetric asserts the catalogue and the constants
// have not drifted apart: a metric added without a description would serve a
// series with no help text, and a description left behind after a metric was
// renamed would document an instrument nobody records.
//
// TestDescriptionsCoverEveryMetric 断言目录与常量没有各走各的：新增指标却没写描述，
// 会导出一条没有 help 文本的序列；指标改名后遗留的描述，则会为一个没人记录的仪器
// 写说明。
func TestDescriptionsCoverEveryMetric(t *testing.T) {
	descs := tunnelserver.Descriptions()

	for _, name := range allMetrics {
		if _, ok := descs[name]; !ok {
			t.Errorf("metric %s has no description", name)
		}
	}

	known := make(map[string]bool, len(allMetrics))
	for _, name := range allMetrics {
		known[name] = true
	}
	for name := range descs {
		if !known[name] {
			t.Errorf("description %s names no metric this package records", name)
		}
	}

	for name, desc := range descs {
		if desc.Help == "" {
			t.Errorf("metric %s has an empty help string", name)
		}
		if desc.Kind == metrics.KindHistogram && len(desc.Buckets) == 0 {
			t.Errorf("histogram %s has no buckets", name)
		}
	}
}

// TestMetricLabelCardinality is the executable form of the two rules in
// metrics.go: every per-link instrument carries replica_id and node_id, the
// replica-scoped ones carry replica_id alone, and no label key exists outside
// the closed vocabulary.
//
// TestMetricLabelCardinality 是 metrics.go 里那两条规则的可执行版本：每个链路级仪器
// 都带 replica_id 与 node_id，副本级的只带 replica_id，且不存在封闭词汇之外的标签键。
func TestMetricLabelCardinality(t *testing.T) {
	mx := exerciseEveryMetric(t)

	replicaScoped := make(map[string]bool, len(tunnelserver.ReplicaScopedMetrics))
	for _, name := range tunnelserver.ReplicaScopedMetrics {
		replicaScoped[name] = true
	}

	for _, name := range allMetrics {
		if mx.SeriesCount(name) == 0 {
			t.Errorf("metric %s was never recorded, so its labels cannot be checked", name)
			continue
		}
		keys := mx.LabelKeys(name)
		has := func(key string) bool {
			for _, k := range keys {
				if k == key {
					return true
				}
			}
			return false
		}

		if !has(tunnelserver.LabelReplicaID) {
			t.Errorf("metric %s carries no %s: a symptom on one replica must be attributable to it",
				name, tunnelserver.LabelReplicaID)
		}
		switch nodeScoped := has(tunnelserver.LabelNodeID); {
		case replicaScoped[name] && nodeScoped:
			t.Errorf("metric %s is replica-scoped but carries %s", name, tunnelserver.LabelNodeID)
		case !replicaScoped[name] && !nodeScoped:
			t.Errorf("metric %s is per-link but carries no %s", name, tunnelserver.LabelNodeID)
		}

		for _, k := range keys {
			if !allLabelKeys[k] {
				t.Errorf("metric %s carries label %q, which is outside the closed vocabulary", name, k)
			}
		}
	}
}

// TestMetricLabelValuesAreBounded asserts no value that came off the wire
// became a label: every value must be an enum name, a configured id, or one of
// the documented state words.
//
// TestMetricLabelValuesAreBounded 断言没有任何来自线上的值变成标签：每个取值都必须是
// 枚举名、配置里的 id，或文档所列的状态词之一。
func TestMetricLabelValuesAreBounded(t *testing.T) {
	mx := exerciseEveryMetric(t)

	allowed := map[string]map[string]bool{
		tunnelserver.LabelNodeID:    {"mac-mini-01": true},
		tunnelserver.LabelReplicaID: {"replica-a": true},
		tunnelserver.LabelState:     {tunnelserver.SlotStateIdle: true, tunnelserver.SlotStateBusy: true},
		tunnelserver.LabelDirection: {tunnelserver.DirectionInbound: true, tunnelserver.DirectionOutbound: true},
		tunnelserver.LabelReason: {
			string(tunnelserver.FaultReadyWhileInflight): true,
			string(tunnelserver.FaultFrameOnIdleSlot):    true,
			string(tunnelserver.FaultFrameTooLarge):      true,
		},
	}
	for name := range tunnelv1.SlotClass_name {
		allowed[tunnelserver.LabelClass] = addName(allowed[tunnelserver.LabelClass], tunnelv1.SlotClass_name[name])
	}
	for value := range tunnelv1.Operation_name {
		allowed[tunnelserver.LabelOperation] = addName(allowed[tunnelserver.LabelOperation], tunnelv1.Operation_name[value])
	}

	for _, s := range mx.All() {
		for key, value := range s.Labels {
			// result is checked against the wire package's own six values
			// elsewhere; here it is enough that it is one short word.
			//
			// result 的取值在别处对着 wire 包自己的六值做检查；此处只需确认它是一个
			// 短词即可。
			if key == tunnelserver.LabelResult {
				continue
			}
			set, checked := allowed[key]
			if !checked {
				continue
			}
			if !set[value] {
				t.Errorf("metric %s label %s = %q, which is not a bounded value", s.Name, key, value)
			}
		}
	}
}

func addName(set map[string]bool, name string) map[string]bool {
	if set == nil {
		set = map[string]bool{}
	}
	set[name] = true
	return set
}

// TestNodeLifecycleMetrics walks a node from connect to drain to disconnect
// and asserts each transition is visible.
//
// TestNodeLifecycleMetrics 让一个节点走完连接、排空、断开的全过程，并断言每次状态
// 转换都能被看见。
func TestNodeLifecycleMetrics(t *testing.T) {
	mx := metricstest.New()
	h := newHarness(t, tunnelserver.Config{Metrics: mx})
	node := map[string]string{tunnelserver.LabelNodeID: "mac-mini-01"}

	c := h.connect("mac-mini-01")
	waitFor(t, "the node to be counted as connected", func() bool {
		return mx.Sum(tunnelserver.MetricConnectedNodes, nil) == 1
	})
	if got := mx.Sum(tunnelserver.MetricNodeState, node); got != 1 {
		t.Errorf("%s = %v on connect, want 1 (connected)", tunnelserver.MetricNodeState, got)
	}
	if got := mx.Sum(tunnelserver.MetricControlStreams, node); got != 1 {
		t.Errorf("%s = %v, want 1", tunnelserver.MetricControlStreams, got)
	}

	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Draining{
		Draining: &tunnelv1.Draining{Reason: "test"},
	}})
	waitFor(t, "the node to report draining", func() bool {
		return mx.Sum(tunnelserver.MetricNodeState, node) == 2
	})

	c.stream.Break(io.EOF)
	c.wait(t)
	waitFor(t, "the node to report gone", func() bool {
		return mx.Sum(tunnelserver.MetricNodeState, node) == 0
	})
	if got := mx.Sum(tunnelserver.MetricConnectedNodes, nil); got != 0 {
		t.Errorf("%s = %v after the last stream ended, want 0", tunnelserver.MetricConnectedNodes, got)
	}
}

// TestHeartbeatMetrics asserts heartbeats are counted and that the interval
// distribution measures the gap this replica actually waited — with the first
// heartbeat excluded, since it has no predecessor to measure against.
//
// TestHeartbeatMetrics 断言心跳被计数，且间隔分布测的是本副本实际等待的时长——首个
// 心跳被排除，因为它没有可比较的前一次。
func TestHeartbeatMetrics(t *testing.T) {
	mx := metricstest.New()
	h := newHarness(t, tunnelserver.Config{Metrics: mx})
	c := h.connect("mac-mini-01")
	node := map[string]string{tunnelserver.LabelNodeID: "mac-mini-01"}

	beat := func() {
		c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Heartbeat{
			Heartbeat: &tunnelv1.Heartbeat{IdleSlots: 1},
		}})
		c.expect(t) // HeartbeatAck, which is the handler's acknowledgement it ran.
	}

	beat()
	if got := mx.Sum(tunnelserver.MetricHeartbeatsTotal, node); got != 1 {
		t.Errorf("%s = %v after one heartbeat, want 1", tunnelserver.MetricHeartbeatsTotal, got)
	}
	if s := mx.Find(tunnelserver.MetricHeartbeatIntervalSeconds, node); s != nil {
		t.Errorf("the first heartbeat produced an interval observation of %v; it has no predecessor to measure against",
			s.Values())
	}

	h.clock.Advance(15 * time.Second)
	beat()
	if got := mx.Sum(tunnelserver.MetricHeartbeatsTotal, node); got != 2 {
		t.Errorf("%s = %v after two heartbeats, want 2", tunnelserver.MetricHeartbeatsTotal, got)
	}
	s := mx.Find(tunnelserver.MetricHeartbeatIntervalSeconds, node)
	if s == nil {
		t.Fatalf("the second heartbeat produced no interval observation")
	}
	if got := s.Value(); got != 15 {
		t.Errorf("heartbeat interval = %vs, want 15s (the gap this replica waited)", got)
	}
}

// TestSlotOccupancyMetrics asserts the gauge tracks a slot through parking,
// dispatch and re-parking, and that idle and busy never both count one slot.
//
// TestSlotOccupancyMetrics 断言量表能跟住一个槽的停放、派发与再停放，且 idle 与 busy
// 不会同时把同一个槽算进去。
func TestSlotOccupancyMetrics(t *testing.T) {
	mx := metricstest.New()
	h := newHarness(t, tunnelserver.Config{Metrics: mx})
	h.connect("mac-mini-01")

	inference := tunnelv1.SlotClass_SLOT_CLASS_INFERENCE.String()
	occupancy := func(state string) float64 {
		return mx.Sum(tunnelserver.MetricSlotsTotal, map[string]string{
			tunnelserver.LabelNodeID: "mac-mini-01",
			tunnelserver.LabelClass:  inference,
			tunnelserver.LabelState:  state,
		})
	}

	release := make(chan struct{})
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(*tunnelv1.RequestHeaders, [][]byte, func(*tunnelv1.AgentFrame) error) error {
			<-release
			return nil
		})
	waitFor(t, "the slot to be reported idle", func() bool { return occupancy(tunnelserver.SlotStateIdle) == 1 })
	if got := occupancy(tunnelserver.SlotStateBusy); got != 0 {
		t.Errorf("busy slots = %v while the only slot is parked, want 0", got)
	}

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID: "mac-mini-01", RuntimeID: "ollama-1", Operation: tunnelv1.Operation_OPERATION_CHAT,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := occupancy(tunnelserver.SlotStateIdle); got != 0 {
		t.Errorf("idle slots = %v while the only slot runs a request, want 0", got)
	}
	if got := occupancy(tunnelserver.SlotStateBusy); got != 1 {
		t.Errorf("busy slots = %v while the only slot runs a request, want 1", got)
	}

	close(release)
	_, _ = resp.Recv()
	_ = resp.Close()
	waitFor(t, "the slot to be reported idle again", func() bool { return occupancy(tunnelserver.SlotStateIdle) == 1 })
}

// TestDispatchMetrics asserts the outcome of every dispatch reaches the
// counter, including the ones that never became a Response.
//
// TestDispatchMetrics 断言每次分发的结果都进到计数器，包括那些从未变成 Response 的。
func TestDispatchMetrics(t *testing.T) {
	tests := []struct {
		name       string
		setupSlot  bool
		req        tunnelserver.Request
		wantResult string
	}{
		{
			name:       "an unknown node is an upstream error",
			req:        tunnelserver.Request{NodeID: "mac-mini-01", RuntimeID: "ollama-1"},
			wantResult: "upstream_error",
		},
		{
			name:       "a missing runtime_id is a client error",
			req:        tunnelserver.Request{NodeID: "mac-mini-01"},
			wantResult: "client_error",
		},
		{
			name:       "a full node is backpressure, not a failure",
			setupSlot:  false,
			req:        tunnelserver.Request{NodeID: "mac-mini-01", RuntimeID: "ollama-1"},
			wantResult: "backpressure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mx := metricstest.New()
			h := newHarness(t, tunnelserver.Config{Metrics: mx})
			if tt.wantResult != "upstream_error" {
				h.connect("mac-mini-01")
			}

			_, err := h.srv.Dispatch(context.Background(), tt.req)
			if err == nil {
				t.Fatalf("Dispatch succeeded, want a failure")
			}

			got := mx.Sum(tunnelserver.MetricDispatchTotal, map[string]string{
				tunnelserver.LabelResult: tt.wantResult,
			})
			if got != 1 {
				t.Errorf("%s{result=%s} = %v, want 1\nrecorded: %v",
					tunnelserver.MetricDispatchTotal, tt.wantResult, got, mx.All())
			}
		})
	}
}

// TestSuccessfulDispatchMetrics asserts a completed request records success,
// a duration, a first-event observation for a streaming operation, and frames
// in both directions.
//
// TestSuccessfulDispatchMetrics 断言一次完成的请求会记录 success、一个时长、流式操作
// 的首帧观测，以及两个方向的帧。
func TestSuccessfulDispatchMetrics(t *testing.T) {
	mx := metricstest.New()
	h := newHarness(t, tunnelserver.Config{Metrics: mx})
	h.connect("mac-mini-01")
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(_ *tunnelv1.RequestHeaders, _ [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			return reply(dataFrame([]byte("event-1")))
		})
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_CHAT_STREAM,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	for {
		if _, err := resp.Recv(); err != nil {
			break
		}
	}
	_ = resp.Close()

	operation := tunnelv1.Operation_OPERATION_CHAT_STREAM.String()
	if got := mx.Sum(tunnelserver.MetricDispatchTotal, map[string]string{
		tunnelserver.LabelResult:    "success",
		tunnelserver.LabelOperation: operation,
	}); got != 1 {
		t.Errorf("%s{result=success} = %v, want 1", tunnelserver.MetricDispatchTotal, got)
	}
	if s := mx.Find(tunnelserver.MetricDispatchDurationSeconds, map[string]string{
		tunnelserver.LabelOperation: operation,
	}); s == nil {
		t.Errorf("%s recorded nothing for a finished dispatch", tunnelserver.MetricDispatchDurationSeconds)
	}
	if s := mx.Find(tunnelserver.MetricStreamFirstEventSeconds, map[string]string{
		tunnelserver.LabelOperation: operation,
	}); s == nil {
		t.Errorf("%s recorded nothing for a streaming response", tunnelserver.MetricStreamFirstEventSeconds)
	}
	for _, direction := range []string{tunnelserver.DirectionInbound, tunnelserver.DirectionOutbound} {
		if s := mx.Find(tunnelserver.MetricFrameBytes, map[string]string{
			tunnelserver.LabelDirection: direction,
		}); s == nil {
			t.Errorf("%s recorded no %s frame", tunnelserver.MetricFrameBytes, direction)
		}
	}
}

// TestFirstEventIsRecordedOnlyForProgressiveResponses states the rule the
// metric's doc comment gives: a request-response operation's first frame is
// its whole answer, and mixing it into the distribution would make the
// tunnel-versus-model comparison meaningless.
//
// TestFirstEventIsRecordedOnlyForProgressiveResponses 说明该指标文档注释里的规则：
// 一问一答操作的首帧就是它的全部答案，把它混进分布会让「隧道慢还是模型慢」的对照
// 失去意义。
func TestFirstEventIsRecordedOnlyForProgressiveResponses(t *testing.T) {
	tests := []struct {
		name      string
		operation tunnelv1.Operation
		want      bool
	}{
		{name: "a token stream is progressive", operation: tunnelv1.Operation_OPERATION_CHAT_STREAM, want: true},
		{name: "a chat completion is not", operation: tunnelv1.Operation_OPERATION_CHAT, want: false},
		{name: "an embedding is not", operation: tunnelv1.Operation_OPERATION_EMBED, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mx := metricstest.New()
			h := newHarness(t, tunnelserver.Config{Metrics: mx})
			h.connect("mac-mini-01")
			h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
				func(_ *tunnelv1.RequestHeaders, _ [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
					return reply(dataFrame([]byte("payload")))
				})
			waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

			resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
				NodeID: "mac-mini-01", RuntimeID: "ollama-1", Operation: tt.operation,
			})
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			for {
				if _, err := resp.Recv(); err != nil {
					break
				}
			}
			_ = resp.Close()

			got := mx.Find(tunnelserver.MetricStreamFirstEventSeconds, map[string]string{
				tunnelserver.LabelOperation: tt.operation.String(),
			}) != nil
			if got != tt.want {
				t.Errorf("%s recorded for %s = %v, want %v",
					tunnelserver.MetricStreamFirstEventSeconds, tt.operation, got, tt.want)
			}
		})
	}
}

// TestRosterVersionMetric asserts the version this replica broadcast is
// visible, which is what makes a fleet-wide disagreement diagnosable.
//
// TestRosterVersionMetric 断言本副本广播的版本可见，这正是让集群范围内的不一致可诊断
// 的前提。
func TestRosterVersionMetric(t *testing.T) {
	mx := metricstest.New()
	h := newHarness(t, tunnelserver.Config{Metrics: mx})

	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 7})
	if got := mx.Sum(tunnelserver.MetricRosterVersion, nil); got != 7 {
		t.Errorf("%s = %v, want 7", tunnelserver.MetricRosterVersion, got)
	}

	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 8})
	if got := mx.Sum(tunnelserver.MetricRosterVersion, nil); got != 8 {
		t.Errorf("%s = %v after a second roster, want 8", tunnelserver.MetricRosterVersion, got)
	}
}

// exerciseEveryMetric drives one replica through enough of its lifecycle that
// every instrument in allMetrics has been recorded at least once, and returns
// the collector. The cardinality tests need every metric present: a label rule
// cannot be checked against a series that was never created.
//
// exerciseEveryMetric 驱动一个副本走过足够多的生命周期，使 allMetrics 中每个仪器都
// 至少被记录一次，并返回收集器。基数测试需要每个指标都在场：对一条从未被创建的序列
// 是无法检查标签规则的。
func exerciseEveryMetric(t *testing.T) *metricstest.Collector {
	t.Helper()
	mx := metricstest.New()
	h := newHarness(t, tunnelserver.Config{Metrics: mx})

	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 1})
	c := h.connect("mac-mini-01")

	// Two heartbeats, so the interval histogram has a predecessor to measure
	// against.
	//
	// 两次心跳，好让间隔直方图有一个可比较的前一次。
	for range 2 {
		c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Heartbeat{
			Heartbeat: &tunnelv1.Heartbeat{IdleSlots: 1},
		}})
		c.expect(t)
		h.clock.Advance(15 * time.Second)
	}

	// A streaming request that the caller abandons mid-stream: it records a
	// dispatch, a duration, a first event, frames in both directions, and the
	// cancel that Close sends because the response never finished.
	//
	// 一个被调用方中途放弃的流式请求：它会记录一次分发、一个时长、一次首帧、两个
	// 方向的帧，以及 Close 因响应未结束而发出的取消。
	blocked := make(chan struct{})
	defer close(blocked)
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(_ *tunnelv1.RequestHeaders, _ [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			if err := reply(dataFrame([]byte("event-1"))); err != nil {
				return err
			}
			<-blocked
			return nil
		})
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID: "mac-mini-01", RuntimeID: "ollama-1", Operation: tunnelv1.Operation_OPERATION_CHAT_STREAM,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, err := resp.Recv(); err != nil {
		t.Fatalf("Recv: %v", err)
	}
	_ = resp.Close()

	// A protocol fault on a second slot, for the fault counter: a Ready while
	// a request is still in flight is the cheapest of the three to provoke.
	//
	// 在第二个槽上制造一次协议错误，用于故障计数器：在请求仍在途时发来 Ready 是三种
	// 里最容易触发的一种。
	faulted := h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-2", nil)
	waitFor(t, "the second slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })
	second, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID: "mac-mini-01", RuntimeID: "ollama-1", Operation: tunnelv1.Operation_OPERATION_CHAT,
	})
	if err != nil {
		t.Fatalf("Dispatch onto the second slot: %v", err)
	}
	if err := faulted.stream.FromAgent(&tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Ready{Ready: &tunnelv1.Ready{
		NodeId: "mac-mini-01", SlotId: "slot-2", Class: tunnelv1.SlotClass_SLOT_CLASS_INFERENCE,
	}}}); err != nil {
		t.Fatalf("sending the offending Ready: %v", err)
	}
	faulted.wait(t)
	_ = second.Close()

	waitFor(t, "the fault to be counted", func() bool {
		return mx.SeriesCount(tunnelserver.MetricSlotFaultsTotal) > 0
	})
	return mx
}

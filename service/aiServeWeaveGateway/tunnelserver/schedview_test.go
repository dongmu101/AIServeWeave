package tunnelserver_test

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// schedSnapshot is a minimal healthy runtime snapshot for runtimeID.
func schedSnapshot(runtimeID string, queue int) runtime.Snapshot {
	return runtime.Snapshot{
		Descriptor: runtime.Descriptor{ID: runtimeID, Kind: runtime.KindOllama, BaseURL: "http://127.0.0.1:11434"},
		State:      runtime.StateHealthy,
		Health:     runtime.HealthReport{QueueRunning: queue},
		Discovery: runtime.Discovery{Models: []runtime.Model{{
			ID:           "m",
			Capabilities: runtime.CapabilitySet{runtime.CapabilityChat: {Level: runtime.SupportSupported}},
		}}},
	}
}

func sendStatus(t *testing.T, h *harness, c *control, full bool, snaps ...runtime.Snapshot) {
	t.Helper()
	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       full,
		ReportedAt: timestamppb.New(h.clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto(snaps),
	}}})
}

// schedViews returns AppendSchedNodes' answer keyed by node id.
func schedViews(h *harness) map[string]tunnelserver.SchedNode {
	out := make(map[string]tunnelserver.SchedNode)
	for _, n := range h.srv.AppendSchedNodes(nil) {
		out[n.NodeID] = n
	}
	return out
}

// requireSchedViewMatchesNodes asserts that, for every node, SchedNode says
// what NodeInfo says. NodeInfo is the long-standing view; SchedNode must be a
// cheaper rendering of the same facts, never a different answer.
//
// requireSchedViewMatchesNodes 断言对每个节点，SchedNode 与 NodeInfo 说的是同一回事。
// NodeInfo 是长期存在的视图；SchedNode 必须是同样事实的更廉价渲染，而不是另一个答案。
func requireSchedViewMatchesNodes(t *testing.T, h *harness) {
	t.Helper()
	views := schedViews(h)
	infos := h.srv.Nodes()
	if len(views) != len(infos) {
		t.Fatalf("AppendSchedNodes returned %d nodes, Nodes returned %d", len(views), len(infos))
	}
	for _, info := range infos {
		v, ok := views[info.NodeID]
		if !ok {
			t.Fatalf("AppendSchedNodes is missing node %q", info.NodeID)
		}
		if v.Live != info.Live || v.Draining != info.Draining || v.Maintenance != info.Maintenance {
			t.Errorf("%s flags: sched live/draining/maintenance = %v/%v/%v, Nodes = %v/%v/%v",
				info.NodeID, v.Live, v.Draining, v.Maintenance, info.Live, info.Draining, info.Maintenance)
		}
		if got, want := v.IdleInference, info.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_INFERENCE]; got != want {
			t.Errorf("%s IdleInference = %d, Nodes says %d", info.NodeID, got, want)
		}
		if v.InflightRequests != info.InflightRequests {
			t.Errorf("%s InflightRequests = %d, Nodes says %d", info.NodeID, v.InflightRequests, info.InflightRequests)
		}
		if len(v.Labels) != len(info.Labels) || (len(v.Labels) > 0 && !reflect.DeepEqual(v.Labels, info.Labels)) {
			t.Errorf("%s Labels = %v, Nodes says %v", info.NodeID, v.Labels, info.Labels)
		}
		if v.Resources.GetGpuMemoryBytes() != info.Resources.GetGpuMemoryBytes() {
			t.Errorf("%s GPU memory = %d, Nodes says %d", info.NodeID, v.Resources.GetGpuMemoryBytes(), info.Resources.GetGpuMemoryBytes())
		}
		if len(v.Runtimes) != len(info.Runtimes) {
			t.Fatalf("%s has %d runtimes, Nodes says %d", info.NodeID, len(v.Runtimes), len(info.Runtimes))
		}
		for i := range v.Runtimes {
			if v.Runtimes[i].Descriptor.ID != info.Runtimes[i].Descriptor.ID ||
				v.Runtimes[i].Health.QueueRunning != info.Runtimes[i].Health.QueueRunning {
				t.Errorf("%s runtime %d = %q/queue %d, Nodes says %q/queue %d", info.NodeID, i,
					v.Runtimes[i].Descriptor.ID, v.Runtimes[i].Health.QueueRunning,
					info.Runtimes[i].Descriptor.ID, info.Runtimes[i].Health.QueueRunning)
			}
		}
	}
}

// TestSchedNodesAgreeWithNodesThroughEveryStateChange drives nodes through the
// events that move each field of the view — a Hello with labels and hardware,
// full and incremental inventory reports, parked and busy slots, a heartbeat,
// maintenance, draining and a missed heartbeat — and checks after each that
// the scheduler's view and the admin view agree.
//
// TestSchedNodesAgreeWithNodesThroughEveryStateChange 让节点依次经历会改变视图各字段的事件
// ——带标签与硬件的 Hello、全量与增量清单上报、停放与忙碌的槽、心跳、maintenance、
// draining 与错过心跳——并在每一步之后检查调度器视图与管理视图是否一致。
func TestSchedNodesAgreeWithNodesThroughEveryStateChange(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{HeartbeatTimeout: 45 * time.Second})
	requireSchedViewMatchesNodes(t, h)
	if n := len(schedViews(h)); n != 0 {
		t.Fatalf("empty server has %d scheduler nodes", n)
	}

	a := h.startControl("node-a", &tunnelv1.Hello{
		NodeId:     "node-a",
		RuntimeIds: []string{"rt1", "rt2"},
		Labels:     map[string]string{"zone": "z1"},
		Resources:  &tunnelv1.NodeResources{GpuMemoryBytes: 24 << 30},
	})
	a.expect(t)
	b := h.connect("node-b", "rt1")
	requireSchedViewMatchesNodes(t, h)

	sendStatus(t, h, a, true, schedSnapshot("rt2", 1), schedSnapshot("rt1", 2))
	sendStatus(t, h, b, true, schedSnapshot("rt1", 0))
	waitFor(t, "inventories", func() bool {
		v := schedViews(h)
		return len(v["node-a"].Runtimes) == 2 && len(v["node-b"].Runtimes) == 1
	})
	requireSchedViewMatchesNodes(t, h)
	if got := schedViews(h)["node-a"].Runtimes; got[0].Descriptor.ID != "rt1" || got[1].Descriptor.ID != "rt2" {
		t.Errorf("runtimes are not sorted by runtime_id: %q, %q", got[0].Descriptor.ID, got[1].Descriptor.ID)
	}
	v := schedViews(h)["node-a"]
	if v.Labels["zone"] != "z1" || v.Resources.GetGpuMemoryBytes() != 24<<30 {
		t.Errorf("Hello fields missing from the scheduler view: labels %v, resources %v", v.Labels, v.Resources)
	}

	// An incremental report changes one runtime and keeps the rest; the
	// previously taken view must not change under its holder.
	before := schedViews(h)["node-a"]
	sendStatus(t, h, a, false, schedSnapshot("rt1", 7), schedSnapshot("rt3", 0))
	waitFor(t, "incremental report", func() bool { return len(schedViews(h)["node-a"].Runtimes) == 3 })
	requireSchedViewMatchesNodes(t, h)
	if got := before.Runtimes[0].Health.QueueRunning; got != 2 || len(before.Runtimes) != 2 {
		t.Errorf("a view taken earlier changed after a report: rt1 queue %d, %d runtimes", got, len(before.Runtimes))
	}
	if got := schedViews(h)["node-a"].Runtimes[0].Health.QueueRunning; got != 7 {
		t.Errorf("rt1 queue after the incremental report = %d, want 7", got)
	}

	// A full report replaces the inventory wholesale.
	sendStatus(t, h, a, true, schedSnapshot("rt9", 0))
	waitFor(t, "full report", func() bool {
		r := schedViews(h)["node-a"].Runtimes
		return len(r) == 1 && r[0].Descriptor.ID == "rt9"
	})
	requireSchedViewMatchesNodes(t, h)

	h.openSlot("node-a", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "a-1", nil)
	h.openSlot("node-a", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "a-2", nil)
	h.openSlot("node-a", tunnelv1.SlotClass_SLOT_CLASS_BULK, "a-bulk", nil)
	waitFor(t, "slots to park", func() bool { return schedViews(h)["node-a"].IdleInference == 2 })
	requireSchedViewMatchesNodes(t, h)

	a.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Heartbeat{Heartbeat: &tunnelv1.Heartbeat{InflightRequests: 5, IdleSlots: 2}}})
	a.expect(t)
	if got := schedViews(h)["node-a"].InflightRequests; got != 5 {
		t.Errorf("InflightRequests = %d, want 5", got)
	}
	requireSchedViewMatchesNodes(t, h)

	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 1, MaintenanceNodeIds: []string{"node-b"}})
	if !schedViews(h)["node-b"].Maintenance || schedViews(h)["node-a"].Maintenance {
		t.Error("maintenance flag did not reach the scheduler view only for node-b")
	}
	requireSchedViewMatchesNodes(t, h)

	b.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Draining{Draining: &tunnelv1.Draining{Reason: "test"}}})
	waitFor(t, "draining", func() bool { return schedViews(h)["node-b"].Draining })
	if schedViews(h)["node-b"].Live {
		t.Error("a draining node is Live in the scheduler view")
	}
	requireSchedViewMatchesNodes(t, h)

	// Liveness is evaluated against the clock each time the view is built, so
	// a silent node drops out without any event.
	h.clock.Advance(time.Minute)
	if schedViews(h)["node-a"].Live {
		t.Error("node-a is still Live a minute after its last heartbeat, want a missed heartbeat to end that")
	}
	requireSchedViewMatchesNodes(t, h)
}

// TestSchedNodesForgetsDisconnectedNodes checks the published node list tracks
// the entry map: a node that connected is listed, and one whose last stream
// ended is gone.
//
// TestSchedNodesForgetsDisconnectedNodes 检查已发布的节点列表与条目 map 保持同步：连接过
// 的节点会被列出，最后一条流结束后则消失。
func TestSchedNodesForgetsDisconnectedNodes(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	a := h.connect("node-a")
	h.connect("node-b")
	names := func() []string {
		var out []string
		for _, n := range h.srv.AppendSchedNodes(nil) {
			out = append(out, n.NodeID)
		}
		sort.Strings(out)
		return out
	}
	if got := fmt.Sprint(names()); got != "[node-a node-b]" {
		t.Fatalf("nodes = %s, want [node-a node-b]", got)
	}

	a.stream.Break(nil)
	waitFor(t, "node-a to be forgotten", func() bool { return fmt.Sprint(names()) == "[node-b]" })
	requireSchedViewMatchesNodes(t, h)
}

// TestSchedNodesAppendsToTheBufferItIsGiven pins the append contract the
// scheduler's pooled buffer relies on.
//
// TestSchedNodesAppendsToTheBufferItIsGiven 固定调度器的池化缓冲所依赖的 append 约定。
func TestSchedNodesAppendsToTheBufferItIsGiven(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("node-a")
	buf := make([]tunnelserver.SchedNode, 1, 8)
	buf[0].NodeID = "kept"
	got := h.srv.AppendSchedNodes(buf)
	if len(got) != 2 || got[0].NodeID != "kept" || got[1].NodeID != "node-a" {
		t.Fatalf("AppendSchedNodes = %+v, want the given element kept and node-a appended", got)
	}
	if &got[0] != &buf[0] {
		t.Error("AppendSchedNodes reallocated although the buffer had capacity")
	}
}

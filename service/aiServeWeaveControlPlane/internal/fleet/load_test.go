package fleet_test

import (
	"context"
	"testing"

	"AIServeWeave/common/nodeview"
)

// TestNodeLoadComesFromTheFresherView pins that the live host load travels
// with the winning view: it is evidence about the node from the same
// heartbeat, so it must not be taken from a staler replica.
//
// TestNodeLoadComesFromTheFresherView 钉住实时主机负载跟随胜出的视图：它是同一次心跳
// 带来的节点证据，不能取自更陈旧的副本。
func TestNodeLoadComesFromTheFresherView(t *testing.T) {
	stale := node("node-a", true, earlier)
	stale.Load = &nodeview.Load{Pressure: false}
	fresh := node("node-a", true, later)
	cpu := 0.95
	fresh.Load = &nodeview.Load{CPUUtilization: &cpu, Pressure: true, PressureReasons: []string{"cpu"}}

	first := replica(t, nodeview.Replica{ReplicaID: "replica-1", GeneratedAt: earlier, Nodes: []nodeview.Node{stale}})
	second := replica(t, nodeview.Replica{ReplicaID: "replica-2", GeneratedAt: later, Nodes: []nodeview.Node{fresh}})

	snapshot, err := aggregate(t, first, second).Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	load := snapshot.Nodes[0].Load
	if load == nil || !load.Pressure || load.CPUUtilization == nil || *load.CPUUtilization != 0.95 {
		t.Fatalf("load = %+v, want the fresher replica's pressured view", load)
	}
}

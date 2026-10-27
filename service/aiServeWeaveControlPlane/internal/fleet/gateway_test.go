package fleet_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/common/nodeview"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/fleet"
)

// TestGatewayNodesKeepsTheSelectedReplicasOwnObservation checks replica-local state and models.
// TestGatewayNodesKeepsTheSelectedReplicasOwnObservation 验证状态与模型来自所选副本自身。
func TestGatewayNodesKeepsTheSelectedReplicasOwnObservation(t *testing.T) {
	first := replica(t, nodeview.Replica{ReplicaID: "gateway-a", GeneratedAt: earlier,
		LocalRuntimes: []nodeview.Runtime{{ID: "local-a", State: "healthy", Models: []nodeview.Model{{ID: "model-a"}}}},
		Nodes:         []nodeview.Node{node("shared-node", false, earlier, "model-a")}})
	second := replica(t, nodeview.Replica{ReplicaID: "gateway-b", GeneratedAt: later,
		LocalRuntimes: []nodeview.Runtime{{ID: "local-b", State: "healthy", Models: []nodeview.Model{{ID: "model-b"}}}},
		Nodes:         []nodeview.Node{node("shared-node", true, later, "model-b")}})
	aggregator := aggregate(t, first, second)
	for _, tt := range []struct {
		name, selector, replicaID, model string
		live                             bool
	}{
		{name: "default Gateway", selector: "default", replicaID: "gateway-a", model: "model-a"},
		{name: "first Gateway by endpoint", selector: first, replicaID: "gateway-a", model: "model-a"},
		{name: "second Gateway by endpoint", selector: second, replicaID: "gateway-b", model: "model-b", live: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := aggregator.GatewayNodes(context.Background(), tt.selector)
			if err != nil || len(snapshot.Nodes) != 1 || len(snapshot.Replicas) != 1 {
				t.Fatalf("snapshot/error = %+v/%v, want one node and one replica, nil error", snapshot, err)
			}
			entry := snapshot.Nodes[0]
			local := snapshot.Replicas[0].LocalRuntimes
			if len(local) != 1 || len(local[0].Models) != 1 || local[0].Models[0].ID != tt.model {
				t.Errorf("local runtimes = %+v, want selected Gateway's model %s", local, tt.model)
			}
			if entry.Live != tt.live || entry.Replicas[0] != tt.replicaID || entry.Runtimes[0].Models[0].ID != tt.model {
				t.Errorf("node = %+v, want live=%v replica=%s model=%s", entry, tt.live, tt.replicaID, tt.model)
			}
		})
	}
	merged, err := aggregator.Nodes(context.Background())
	if err != nil || len(merged.Replicas) != 2 || !merged.Nodes[0].Live {
		t.Errorf("unfiltered snapshot/error = %+v/%v, want two replicas and merged live node, nil error", merged, err)
	}
}

// TestGatewayNodesRejectsUnknownEndpoints checks that selectors cannot request arbitrary URLs.
// TestGatewayNodesRejectsUnknownEndpoints 验证选择值不能用于请求任意 URL。
func TestGatewayNodesRejectsUnknownEndpoints(t *testing.T) {
	for _, tt := range []struct {
		name       string
		aggregator *fleet.Aggregator
		selector   string
		want       error
	}{
		{name: "not configured", selector: "default", want: fleet.ErrDisabled},
		{name: "arbitrary URL", aggregator: fleet.New(fleet.Config{Gateways: []string{"http://configured.invalid"}}), selector: "http://127.0.0.1:1/private", want: fleet.ErrUnknownGateway},
		{name: "blank selector", aggregator: fleet.New(fleet.Config{Gateways: []string{"http://configured.invalid"}}), want: fleet.ErrUnknownGateway},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.aggregator.GatewayNodes(context.Background(), tt.selector)
			if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

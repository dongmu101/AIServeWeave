package fleet_test

import (
	"context"
	"testing"

	"AIServeWeave/common/nodeview"
)

// TestTenantModelsUsesAgentReports verifies source selection and offline semantics.
// TestTenantModelsUsesAgentReports 验证数据只来自 Agent 上报，并保留离线语义。
func TestTenantModelsUsesAgentReports(t *testing.T) {
	for _, tt := range []struct {
		name       string
		nodes      []nodeview.Node
		wantCount  int
		wantOnline bool
	}{
		{name: "online Agent reports model", nodes: []nodeview.Node{node("agent", true, earlier, "agent-model")}, wantCount: 1, wantOnline: true},
		{name: "offline Agent preserves last report", nodes: []nodeview.Node{node("agent", false, earlier, "agent-model")}, wantCount: 1},
		{name: "connected Agent without runtime report", nodes: []nodeview.Node{{NodeID: "agent", Live: true}}},
		{name: "Gateway local model without Agents is excluded", nodes: []nodeview.Node{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			endpoint := replica(t, nodeview.Replica{
				ReplicaID: "gateway", GeneratedAt: earlier, Nodes: tt.nodes,
				LocalRuntimes: []nodeview.Runtime{{ID: "gateway-runtime", Kind: "ollama", State: "healthy", Models: []nodeview.Model{{ID: "gateway-only-model"}}}},
			})
			catalog, err := aggregate(t, endpoint).TenantModels(context.Background())
			if err != nil || catalog.Partial || len(catalog.Services) != tt.wantCount {
				t.Fatalf("catalog/error = %+v/%v, want %d services, partial=false and nil error", catalog, err, tt.wantCount)
			}
			if tt.wantCount == 0 {
				return
			}
			service := catalog.Services[0]
			if service.AgentID != "agent" || service.ServiceID != tt.nodes[0].Runtimes[0].ID || service.NodeOnline != tt.wantOnline || len(service.Models) != 1 || service.Models[0].ID != "agent-model" {
				t.Errorf("service = %+v, want online=%v and only agent-model", service, tt.wantOnline)
			}
		})
	}
}

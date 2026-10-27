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

// TestTenantModelsAttachesCLIUsageByKind verifies a codex or claude service
// carries its own CLI's quota, and other services carry none.
// TestTenantModelsAttachesCLIUsageByKind 验证 codex 或 claude 服务带有各自 CLI 的额度，
// 其他服务不带。
func TestTenantModelsAttachesCLIUsageByKind(t *testing.T) {
	entry := nodeview.Node{
		NodeID: "agent", Live: true, LastHeartbeat: &earlier,
		Runtimes: []nodeview.Runtime{
			{ID: "claude-code-local", Kind: "claude", State: "healthy", Models: []nodeview.Model{{ID: "sonnet"}}},
			{ID: "codex-local", Kind: "codex", State: "healthy", Models: []nodeview.Model{{ID: "gpt"}}},
			{ID: "vllm-a", Kind: "vllm", State: "healthy", Models: []nodeview.Model{{ID: "qwen"}}},
		},
		CLIUsage: []nodeview.CLIUsage{
			{Kind: "claude", Status: "ready", Windows: []nodeview.CLIUsageWindow{{Name: "five_hour", UsedPercent: 6}}},
			{Kind: "codex", Status: "ready", Windows: []nodeview.CLIUsageWindow{{Name: "primary", UsedPercent: 0}}},
		},
	}
	endpoint := replica(t, nodeview.Replica{ReplicaID: "gateway", GeneratedAt: earlier, Nodes: []nodeview.Node{entry}})
	catalog, err := aggregate(t, endpoint).TenantModels(context.Background())
	if err != nil || len(catalog.Services) != 3 {
		t.Fatalf("catalog/error = %+v/%v, want 3 services and nil error", catalog, err)
	}
	want := map[string]float64{"claude-code-local": 6, "codex-local": 0}
	for _, service := range catalog.Services {
		percent, expected := want[service.ServiceID]
		if !expected {
			if service.CLIUsage != nil {
				t.Errorf("%s CLIUsage = %+v, want none for a non-CLI service", service.ServiceID, service.CLIUsage)
			}
			continue
		}
		if service.CLIUsage == nil || len(service.CLIUsage.Windows) != 1 || service.CLIUsage.Windows[0].UsedPercent != percent {
			t.Errorf("%s CLIUsage = %+v, want one window at %v%%", service.ServiceID, service.CLIUsage, percent)
		}
	}
}

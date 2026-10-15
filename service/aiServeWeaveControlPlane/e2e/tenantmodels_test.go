package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/nodeview"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/fleet"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// TestTenantModelsUsesTenantSessionsAndExcludesInfrastructure verifies the public projection.
// TestTenantModelsUsesTenantSessionsAndExcludesInfrastructure 验证租户认证与公开字段投影。
func TestTenantModelsUsesTenantSessionsAndExcludesInfrastructure(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+gatewayToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(nodeview.Replica{
			ReplicaID: "private-replica", GeneratedAt: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC),
			Nodes: []nodeview.Node{{NodeID: "agent-01", Live: true, Runtimes: []nodeview.Runtime{{
				ID: "ollama-11434", Kind: "ollama", State: "healthy", Version: "1.0",
				BaseURL: "http://private-agent-host:11434", ErrorSummary: "private-diagnostic",
				Models: []nodeview.Model{{ID: "agent-model", Capabilities: map[string]string{"chat": "supported"}}},
			}}}},
			LocalRuntimes: []nodeview.Runtime{{ID: "private-runtime", Kind: "ollama", State: "healthy", Version: "1.0",
				BaseURL: "http://private-host:11434", ErrorSummary: "private-diagnostic", Warnings: []string{"private-warning"},
				Models: []nodeview.Model{{ID: "local-model", Capabilities: map[string]string{"chat": "supported"}}},
			}},
		})
	}))
	defer gateway.Close()
	h := newHarnessWith(t, []string{gateway.URL})
	_, owner := bootstrap(h, "Tenant One", "tenant-one@example.com")
	_, other := bootstrap(h, "Tenant Two", "tenant-two@example.com")
	var member types.User
	if got := h.call(http.MethodPost, "/admin/v1/users", owner, types.CreateUserRequest{
		Email: "member-models@example.com", Password: ownerPassword, Role: "member",
	}, &member); got != http.StatusCreated {
		t.Fatalf("create member status = %d, want 201", got)
	}
	memberSession := loginTenant(t, h, member.Email, ownerPassword)
	platform := bootstrapPlatformOperator(h, "models-operator@example.com")
	for _, tt := range []struct {
		name, session string
		status        int
	}{
		{name: "tenant owner", session: owner, status: http.StatusOK},
		{name: "tenant member", session: memberSession, status: http.StatusOK},
		{name: "other tenant reads shared discovery", session: other, status: http.StatusOK},
		{name: "no session", status: http.StatusUnauthorized},
		{name: "platform token cannot replace tenant session", session: platform, status: http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var raw map[string]any
			status := h.call(http.MethodGet, "/admin/v1/models?gateway=http://untrusted.invalid&tenant_id=other", tt.session, nil, &raw)
			if status != tt.status {
				t.Fatalf("status = %d, want %d", status, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			body, _ := json.Marshal(raw)
			if strings.Contains(string(body), "private-") || strings.Contains(string(body), gateway.URL) {
				t.Fatalf("tenant response = %s, want no private infrastructure fields", body)
			}
			var got fleet.TenantModelCatalog
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got.Partial || len(got.Services) != 1 || len(got.Services[0].Models) != 1 || got.Services[0].Models[0].ID != "agent-model" || !got.Services[0].NodeOnline || got.Services[0].AgentID != "agent-01" || got.Services[0].ServiceID != "ollama-11434" {
				t.Errorf("catalog = %+v, want one online Agent service and agent-model", got)
			}
		})
	}
}

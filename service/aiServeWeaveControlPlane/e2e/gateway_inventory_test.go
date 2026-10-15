package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"AIServeWeave/common/nodeview"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/fleet"
)

// TestGatewayInventorySelectionAndSessionIsolation checks HTTP selection and platform authorization.
// TestGatewayInventorySelectionAndSessionIsolation 验证 HTTP 选择范围与平台会话授权。
func TestGatewayInventorySelectionAndSessionIsolation(t *testing.T) {
	gateways := make([]string, 0, 2)
	for _, id := range []string{"gateway-a", "gateway-b"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+gatewayToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(nodeview.Replica{
				ReplicaID: id, GeneratedAt: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC),
				Nodes: []nodeview.Node{{NodeID: id + "-node", Live: true}},
			})
		}))
		t.Cleanup(server.Close)
		gateways = append(gateways, server.URL)
	}
	h := newHarnessWith(t, gateways)
	platformSession := bootstrapPlatformOperator(h, "operator@example.com")
	_, tenantSession := bootstrap(h, "Acme", "owner@example.com")
	for _, tt := range []struct {
		name, selector, session, replica string
		status                           int
	}{
		{name: "current Gateway", selector: "default", session: platformSession, replica: "gateway-a", status: http.StatusOK},
		{name: "configured second Gateway", selector: gateways[1], session: platformSession, replica: "gateway-b", status: http.StatusOK},
		{name: "unknown URL", selector: "http://127.0.0.1:1/private", session: platformSession, status: http.StatusNotFound},
		{name: "tenant cannot read Gateway inventory", selector: "default", session: tenantSession, status: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var snapshot fleet.Snapshot
			status := h.call(http.MethodGet, "/operator/v1/nodes?gateway="+url.QueryEscape(tt.selector), tt.session, nil, &snapshot)
			if status != tt.status {
				t.Fatalf("status = %d, want %d", status, tt.status)
			}
			if status == http.StatusOK && (len(snapshot.Replicas) != 1 || len(snapshot.Nodes) != 1 || snapshot.Nodes[0].NodeID != tt.replica+"-node") {
				t.Errorf("snapshot = %+v, want only %s and its node", snapshot, tt.replica)
			}
		})
	}
}

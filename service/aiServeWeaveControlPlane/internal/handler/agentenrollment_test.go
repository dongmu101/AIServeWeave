package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/session"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/token"
)

// TestAgentEnrollmentMiddlewareRejectsMissingAndTenantSessions checks the platform session boundary.
//
// TestAgentEnrollmentMiddlewareRejectsMissingAndTenantSessions 检查平台会话边界。
func TestAgentEnrollmentMiddlewareRejectsMissingAndTenantSessions(t *testing.T) {
	clock := runtime.NewSystemClock()
	issuer, err := token.NewIssuer(strings.Repeat("s", 32), time.Hour, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &svc.ServiceContext{Issuer: issuer, Sessions: session.NewMemory(clock), Logic: logic.New(memstore.New(), clock)}
	for _, tc := range []struct {
		name, role, tenant string
		status             int
	}{
		{"anonymous", "", "", http.StatusUnauthorized},
		{"tenant owner", model.RoleOwner, "tenant_1", http.StatusForbidden},
		{"tenant admin", model.RoleAdmin, "tenant_1", http.StatusForbidden},
		{"revoked operator", model.RolePlatformOperator, model.PlatformScope, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, handler := range []http.HandlerFunc{agentEnrollmentCA(ctx), createAgentEnrollment(ctx)} {
				req := httptest.NewRequest(http.MethodPost, "/operator/v1/agent-enrollment", strings.NewReader(`{}`))
				if tc.role != "" {
					signed, _, err := issuer.Issue(token.Claims{SessionID: "test-session", UserID: "user_1", Role: tc.role, TenantID: tc.tenant})
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Authorization", "Bearer "+signed)
				}
				w := httptest.NewRecorder()
				requirePlatformSession(ctx, handler)(w, req)
				if w.Code != tc.status {
					t.Errorf("status=%d, want %d", w.Code, tc.status)
				}
			}
		})
	}
}

// TestAgentEnrollmentHandlerRejectsNodeOverrideAndExtremeTTL checks unsafe enrollment requests.
//
// TestAgentEnrollmentHandlerRejectsNodeOverrideAndExtremeTTL 检查不安全的接入请求。
func TestAgentEnrollmentHandlerRejectsNodeOverrideAndExtremeTTL(t *testing.T) {
	ctx := &svc.ServiceContext{Logic: logic.New(memstore.New(), runtime.NewSystemClock())}
	for _, tc := range []struct{ name, body string }{
		{"node override", `{"node_id":"existing-node","ttl_minutes":60}`},
		{"overflow", `{"registry_addr":"r:9443","gateway_addr":"g:8443","ttl_minutes":9223372036854775807}`},
		{"too long", `{"registry_addr":"r:9443","gateway_addr":"g:8443","ttl_minutes":1441}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/operator/v1/agent-enrollment", strings.NewReader(tc.body))
			req = req.WithContext(withActor(req.Context(), logic.Actor{UserID: "op_1", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}))
			w := httptest.NewRecorder()
			createAgentEnrollment(ctx)(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400", w.Code)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control=%q, want no-store", w.Header().Get("Cache-Control"))
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		})
	}
}

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/agentenrollment"
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

type enrollmentCAStub struct{}

func (enrollmentCAStub) CA() (agentenrollment.Certificate, error) {
	return agentenrollment.Certificate{PEM: "public-ca", SHA256: "cafe"}, nil
}

func (enrollmentCAStub) Create(context.Context, string, string, time.Duration) (agentenrollment.Bundle, error) {
	return agentenrollment.Bundle{}, nil
}

// TestAgentEnrollmentCAOffersConfiguredAddresses checks the page receives the
// configured addresses as defaults, and none when they are not configured.
//
// TestAgentEnrollmentCAOffersConfiguredAddresses 检查页面拿到已配置的地址作为默认值，
// 未配置时不带地址。
func TestAgentEnrollmentCAOffersConfiguredAddresses(t *testing.T) {
	for _, tc := range []struct {
		name              string
		opts              []logic.Option
		registry, gateway string
	}{
		{"configured", []logic.Option{logic.WithAgentEnrollment(enrollmentCAStub{}), logic.WithEnrollmentAddresses("registry.example.com:9443", "agent.example.com:8443")}, "registry.example.com:9443", "agent.example.com:8443"},
		{"not configured", []logic.Option{logic.WithAgentEnrollment(enrollmentCAStub{})}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &svc.ServiceContext{Logic: logic.New(memstore.New(), runtime.NewSystemClock(), tc.opts...)}
			req := httptest.NewRequest(http.MethodGet, "/operator/v1/agent-enrollment", nil)
			req = req.WithContext(withActor(req.Context(), logic.Actor{UserID: "op_1", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}))
			w := httptest.NewRecorder()
			agentEnrollmentCA(ctx)(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d, want 200", w.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"registry_addr": tc.registry, "gateway_addr": tc.gateway} {
				got, present := body[key]
				if want == "" && present {
					t.Errorf("%s=%v, want absent", key, got)
				}
				if want != "" && got != want {
					t.Errorf("%s=%v, want %q", key, got, want)
				}
			}
		})
	}
}

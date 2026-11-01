package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/agentenrollment"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

type installIssuerStub struct{}

func (installIssuerStub) CA() (agentenrollment.Certificate, error) {
	return agentenrollment.Certificate{PEM: "public-ca", SHA256: "cafe"}, nil
}

func (installIssuerStub) MintCredential(_ context.Context, nodeID string, ttl time.Duration) (agentenrollment.Credential, error) {
	return agentenrollment.Credential{NodeID: nodeID, Token: "secret-token", ExpiresAt: time.Now().Add(ttl), CAPEM: "public-ca", CASHA256: "cafe"}, nil
}

// gateStub admits or refuses every attempt and counts them.
//
// gateStub 放行或拒绝每次尝试并计数。
type gateStub struct {
	admit bool
	err   error
	calls int
}

func (g *gateStub) Admit(context.Context) (bool, error) { g.calls++; return g.admit, g.err }

func newInstallCtx(gate *gateStub) *svc.ServiceContext {
	ctx := &svc.ServiceContext{Logic: logic.New(memstore.New(), runtime.NewSystemClock(),
		logic.WithAgentInstall(logic.AgentInstallConfig{Issuer: installIssuerStub{}, PublicURL: "https://open7x.com"}))}
	if gate != nil {
		ctx.RedeemGate = gate
	}
	return ctx
}

func operatorRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/operator/v1/agent-install-codes", strings.NewReader(body))
	return req.WithContext(withActor(req.Context(), logic.Actor{UserID: "op_1", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}))
}

// TestInstallIssueAndRedeemRoundTrip checks the issued code redeems through
// the handlers and that nothing is cacheable.
//
// TestInstallIssueAndRedeemRoundTrip 检查签发的码可经 handler 兑换，且任何响应都不可缓存。
func TestInstallIssueAndRedeemRoundTrip(t *testing.T) {
	ctx := newInstallCtx(&gateStub{admit: true})
	w := httptest.NewRecorder()
	createOperatorInstall(ctx)(w, operatorRequest(`{"registry_addr":"r.example.com:9443","gateway_addr":"g.example.com:8443","ttl_minutes":60}`))
	var issued types.InstallIssueResponse
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &issued) != nil || issued.Credential != "code" || issued.Code == "" || issued.InstallBaseURL != "https://open7x.com" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("issue status=%d body=%s, want 201 with a code and no-store", w.Code, w.Body)
	}
	body, _ := json.Marshal(types.InstallRedeemRequest{Code: issued.Code})
	w = httptest.NewRecorder()
	redeemInstallCode(ctx)(w, httptest.NewRequest(http.MethodPost, "/internal/v1/agent-install/redeem", strings.NewReader(string(body))))
	var redeemed types.InstallRedeemResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &redeemed) != nil || redeemed.NodeID != issued.NodeID || redeemed.BootstrapToken != "secret-token" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("redeem status=%d body=%s, want 200 with node %q, token and no-store", w.Code, w.Body, issued.NodeID)
	}
	w = httptest.NewRecorder()
	redeemInstallCode(ctx)(w, httptest.NewRequest(http.MethodPost, "/internal/v1/agent-install/redeem", strings.NewReader(string(body))))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "invalid_code") || strings.Contains(w.Body.String(), "secret-token") {
		t.Fatalf("second redeem status=%d body=%s, want 404 invalid_code", w.Code, w.Body)
	}
}

// TestRedeemRefusalsAreIndistinguishable checks unknown and malformed codes
// answer the same 404.
//
// TestRedeemRefusalsAreIndistinguishable 检查未知与格式错误的码回答同一个 404。
func TestRedeemRefusalsAreIndistinguishable(t *testing.T) {
	ctx := newInstallCtx(nil)
	var bodies []string
	for _, code := range []string{"K7QF-9M2D-X4TB-H8RW-3NPC", "garbage", ""} {
		w := httptest.NewRecorder()
		redeemInstallCode(ctx)(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"code":"`+code+`"}`)))
		if w.Code != http.StatusNotFound {
			t.Fatalf("code %q status = %d, want 404", code, w.Code)
		}
		bodies = append(bodies, w.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("refusal bodies differ: %q", bodies)
	}
}

// TestInstallGateLimitsRedeemAndCA checks 429 with Retry-After, fail-closed
// 503 and that a refused attempt never reaches the logic.
//
// TestInstallGateLimitsRedeemAndCA 检查 429 与 Retry-After、故障时关闭的 503，以及被拒绝的
// 尝试不会到达 logic。
func TestInstallGateLimitsRedeemAndCA(t *testing.T) {
	for _, tc := range []struct {
		name       string
		gate       *gateStub
		status     int
		retryAfter string
	}{
		{"over limit", &gateStub{admit: false}, http.StatusTooManyRequests, "60"},
		{"redis down", &gateStub{err: errors.New("redis down")}, http.StatusServiceUnavailable, ""},
		{"admitted", &gateStub{admit: true}, http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newInstallCtx(tc.gate)
			w := httptest.NewRecorder()
			installCA(ctx)(w, httptest.NewRequest(http.MethodGet, "/internal/v1/agent-install/ca", nil))
			if w.Code != tc.status || w.Header().Get("Retry-After") != tc.retryAfter {
				t.Fatalf("CA status=%d Retry-After=%q, want %d / %q", w.Code, w.Header().Get("Retry-After"), tc.status, tc.retryAfter)
			}
			w = httptest.NewRecorder()
			redeemInstallCode(ctx)(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"code":"K7QF-9M2D-X4TB-H8RW-3NPC"}`)))
			wantRedeem := tc.status
			if tc.status == http.StatusOK {
				wantRedeem = http.StatusNotFound
			}
			if w.Code != wantRedeem || tc.gate.calls != 2 {
				t.Fatalf("redeem status=%d gate calls=%d, want %d and 2", w.Code, tc.gate.calls, wantRedeem)
			}
		})
	}
}

// TestInstallIssueRejectsBadRequests checks TTL bounds and unknown fields.
//
// TestInstallIssueRejectsBadRequests 检查有效期范围与未知字段。
func TestInstallIssueRejectsBadRequests(t *testing.T) {
	ctx := newInstallCtx(nil)
	for _, tc := range []struct{ name, body string }{
		{"ttl too short", `{"registry_addr":"r.example.com:9443","gateway_addr":"g.example.com:8443","ttl_minutes":1}`},
		{"ttl too long", `{"registry_addr":"r.example.com:9443","gateway_addr":"g.example.com:8443","ttl_minutes":1441}`},
		{"node override", `{"node_id":"existing","ttl_minutes":60}`},
		{"unknown credential", `{"registry_addr":"r.example.com:9443","gateway_addr":"g.example.com:8443","ttl_minutes":60,"credential":"password"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			createOperatorInstall(ctx)(w, operatorRequest(tc.body))
			if w.Code != http.StatusBadRequest || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d Cache-Control=%q, want 400 and no-store; body=%s", w.Code, w.Header().Get("Cache-Control"), w.Body)
			}
		})
	}
}

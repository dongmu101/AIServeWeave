package e2e_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// TestConsoleSessionAuthenticatesAtTheGatewayStudioAPI is the closed loop
// Studio's sign-in rests on: a session from the Admin API's own login
// verifies through the Gateway's real client, names the tenant user, and
// stops verifying once it is logged out and the short session cache lapses.
//
// TestConsoleSessionAuthenticatesAtTheGatewayStudioAPI 是 Studio 登录所依赖的闭环：
// 经 Admin API 自己的登录得到的会话，能通过 Gateway 真实的客户端校验并指明租户用户；
// 登出且短会话缓存过期后即不再通过。
func TestConsoleSessionAuthenticatesAtTheGatewayStudioAPI(t *testing.T) {
	h := newHarness(t)
	created, session := bootstrap(h, "Acme", "studio-owner@example.com")
	clock := newSteppableClock()
	verifier := gatewayVerifier(h, clock)

	identity, err := verifier.VerifySession(context.Background(), session)
	if err != nil {
		t.Fatalf("verifying a fresh session: %v", err)
	}
	if identity.TenantID != created.Tenant.ID {
		t.Errorf("TenantID = %q, want %q", identity.TenantID, created.Tenant.ID)
	}
	if identity.UserID == "" || identity.Role != "owner" {
		t.Errorf("identity = %+v, want a user with role owner", identity)
	}
	if identity.KeyID != "" {
		t.Errorf("KeyID = %q, want empty for a session", identity.KeyID)
	}

	if status := h.call(http.MethodDelete, "/admin/v1/auth/session", session, nil, nil); status != http.StatusNoContent {
		t.Fatalf("logout status = %d", status)
	}
	clock.Advance(controlplaneclient.SessionCacheTTL)
	if _, err := verifier.VerifySession(context.Background(), session); !errors.Is(err, httpapi.ErrSessionRejected) {
		t.Errorf("after logout: err = %v, want ErrSessionRejected", err)
	}
}

// TestSessionVerificationRefusesWhatIsNotATenantSession covers the tokens
// the endpoint must refuse with the same rejection: a platform operator's
// session, a forged token, and an API key presented as if it were a session.
//
// TestSessionVerificationRefusesWhatIsNotATenantSession 覆盖该端点必须以同一种拒绝
// 回答的令牌：平台运维的会话、伪造的令牌，以及冒充会话出示的 API Key。
func TestSessionVerificationRefusesWhatIsNotATenantSession(t *testing.T) {
	h := newHarness(t)
	_, session := bootstrap(h, "Acme", "studio-refuse@example.com")
	platform := bootstrapPlatformOperator(h, "studio-operator@example.com")

	var key types.CreateAPIKeyResponse
	if status := h.call(http.MethodPost, "/admin/v1/apikeys", session, types.CreateAPIKeyRequest{Name: "k"}, &key); status != http.StatusCreated {
		t.Fatalf("creating a key: status %d", status)
	}

	verifier := gatewayVerifier(h, newSteppableClock())
	for name, token := range map[string]string{
		"platform operator": platform,
		"forged":            session + "x",
		"api key":           key.Key,
	} {
		if _, err := verifier.VerifySession(context.Background(), token); !errors.Is(err, httpapi.ErrSessionRejected) {
			t.Errorf("%s: err = %v, want ErrSessionRejected", name, err)
		}
	}

	if status := h.call(http.MethodPost, "/internal/v1/sessions/verify", session, types.SessionVerifyRequest{Token: session}, nil); status != http.StatusUnauthorized {
		t.Errorf("a session token as the internal credential: status = %d, want 401", status)
	}
}

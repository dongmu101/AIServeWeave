package e2e_test

import (
	"context"
	"net/http"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/session"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

type unavailableSessions struct{}

func (unavailableSessions) Create(context.Context, session.Record) error {
	return session.ErrUnavailable
}
func (unavailableSessions) Validate(context.Context, string, session.Subject, string, string) error {
	return session.ErrUnavailable
}
func (unavailableSessions) Revoke(context.Context, session.Subject, string) (bool, error) {
	return false, session.ErrUnavailable
}
func (unavailableSessions) RevokeAll(context.Context, session.Subject) (int, error) {
	return 0, session.ErrUnavailable
}
func (unavailableSessions) BeginMutation(context.Context, session.Subject) (session.Gate, error) {
	return session.Gate{}, session.ErrUnavailable
}
func (unavailableSessions) EndMutation(context.Context, session.Gate) error {
	return session.ErrUnavailable
}

func TestTenantLogoutRevokesServerSession(t *testing.T) {
	h := newHarness(t)
	_, signed := bootstrap(h, "Acme", "owner-logout@example.com")
	if status := h.call(http.MethodDelete, "/admin/v1/auth/session", signed, nil, nil); status != http.StatusNoContent {
		t.Fatalf("logout status = %d, want %d", status, http.StatusNoContent)
	}
	if status := h.call(http.MethodGet, "/admin/v1/users", signed, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("request after logout status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestTenantBulkSessionRevocationIsImmediate(t *testing.T) {
	h := newHarness(t)
	_, first := bootstrap(h, "Acme", "owner-sessions@example.com")
	var secondLogin struct {
		Token string `json:"token"`
	}
	if status := h.call(http.MethodPost, "/admin/v1/auth/login", "", map[string]string{
		"email": "owner-sessions@example.com", "password": ownerPassword,
	}, &secondLogin); status != http.StatusOK {
		t.Fatalf("second login status = %d, want %d", status, http.StatusOK)
	}
	if status := h.call(http.MethodPost, "/admin/v1/auth/sessions/revoke", first, struct{}{}, nil); status != http.StatusNoContent {
		t.Fatalf("revoke-all status = %d, want %d", status, http.StatusNoContent)
	}
	for name, signed := range map[string]string{"first": first, "second": secondLogin.Token} {
		t.Run(name, func(t *testing.T) {
			if status := h.call(http.MethodGet, "/admin/v1/users", signed, nil, nil); status != http.StatusUnauthorized {
				t.Errorf("request status = %d, want %d", status, http.StatusUnauthorized)
			}
		})
	}
}

func TestRedisFailureIsServiceUnavailableNotInvalidSession(t *testing.T) {
	h := newHarness(t)
	_, signed := bootstrap(h, "Unavailable", "redis-unavailable@example.com")
	h.svcCtx.Sessions = unavailableSessions{}
	if status := h.call(http.MethodGet, "/admin/v1/users", signed, nil, nil); status != http.StatusServiceUnavailable {
		t.Errorf("protected request status = %d, want %d", status, http.StatusServiceUnavailable)
	}
}

func TestRedisFailureDuringLoginReturnsServiceUnavailable(t *testing.T) {
	h := newHarness(t)
	if status := h.call(http.MethodPost, "/admin/v1/tenants", bootstrapToken, types.CreateTenantRequest{
		Name: "Unavailable login", OwnerEmail: "redis-login@example.com", OwnerPassword: ownerPassword,
	}, nil); status != http.StatusCreated {
		t.Fatalf("create tenant status = %d, want %d", status, http.StatusCreated)
	}
	h.svcCtx.Sessions = unavailableSessions{}
	if status := loginTenantStatus(h, "redis-login@example.com", ownerPassword, nil); status != http.StatusServiceUnavailable {
		t.Errorf("login status = %d, want %d", status, http.StatusServiceUnavailable)
	}
	user, err := h.store.GetUserByEmail(context.Background(), "redis-login@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if user.LastLoginAt != nil {
		t.Errorf("LastLoginAt = %v, want nil after a session-store failure", user.LastLoginAt)
	}
}

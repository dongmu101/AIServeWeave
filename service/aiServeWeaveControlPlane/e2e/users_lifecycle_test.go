package e2e_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

func TestTenantUserLifecycleOverHTTP(t *testing.T) {
	h := newHarness(t)
	_, ownerSession := bootstrap(h, "Lifecycle", "lifecycle-owner@example.com")

	var user types.User
	if status := h.call(http.MethodPost, "/admin/v1/users", ownerSession, types.CreateUserRequest{
		Email: "managed@example.com", Password: ownerPassword, Name: "Managed", Role: "admin",
	}, &user); status != http.StatusCreated {
		t.Fatalf("create user status = %d, want %d", status, http.StatusCreated)
	}
	managedSession := loginTenant(t, h, "managed@example.com", ownerPassword)
	var key types.CreateAPIKeyResponse
	if status := h.call(http.MethodPost, "/admin/v1/apikeys", managedSession,
		types.CreateAPIKeyRequest{Name: "managed-automation"}, &key); status != http.StatusCreated {
		t.Fatalf("create managed key status = %d, want %d", status, http.StatusCreated)
	}

	if status := h.call(http.MethodPut, "/admin/v1/users/"+user.ID+"/role", ownerSession,
		map[string]string{"role": "member"}, nil); status != http.StatusNoContent {
		t.Fatalf("change role status = %d, want %d", status, http.StatusNoContent)
	}
	if status := h.call(http.MethodGet, "/admin/v1/users", managedSession, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("old-role session status = %d, want %d", status, http.StatusUnauthorized)
	}
	if _, err := gatewayVerifier(h, newSteppableClock()).Verify(context.Background(), key.Key); err != nil {
		t.Errorf("key after role change error = %v, want nil", err)
	}

	managedSession = loginTenant(t, h, "managed@example.com", ownerPassword)
	if status := h.call(http.MethodPut, "/admin/v1/users/"+user.ID+"/password", ownerSession,
		map[string]string{"new_password": "managed-replacement"}, nil); status != http.StatusNoContent {
		t.Fatalf("reset password status = %d, want %d", status, http.StatusNoContent)
	}
	if status := h.call(http.MethodGet, "/admin/v1/users", managedSession, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("pre-reset session status = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := loginTenantStatus(h, "managed@example.com", ownerPassword, nil); status != http.StatusUnauthorized {
		t.Errorf("old managed password status = %d, want %d", status, http.StatusUnauthorized)
	}

	managedSession = loginTenant(t, h, "managed@example.com", "managed-replacement")
	if status := h.call(http.MethodPost, "/admin/v1/users/"+user.ID+"/disable", ownerSession, nil, nil); status != http.StatusNoContent {
		t.Fatalf("disable status = %d, want %d", status, http.StatusNoContent)
	}
	if status := h.call(http.MethodGet, "/admin/v1/users", managedSession, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("disabled session status = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := loginTenantStatus(h, "managed@example.com", "managed-replacement", nil); status != http.StatusUnauthorized {
		t.Errorf("disabled login status = %d, want %d", status, http.StatusUnauthorized)
	}
	assertKeyRejected(t, h, key.Key)

	if status := h.call(http.MethodPost, "/admin/v1/users/"+user.ID+"/enable", ownerSession, nil, nil); status != http.StatusNoContent {
		t.Fatalf("enable status = %d, want %d", status, http.StatusNoContent)
	}
	managedSession = loginTenant(t, h, "managed@example.com", "managed-replacement")
	assertKeyRejected(t, h, key.Key)
	if status := h.call(http.MethodPost, "/admin/v1/users/"+hOwnerID(t, h, ownerSession)+"/disable", managedSession, nil, nil); status != http.StatusForbidden {
		t.Errorf("member disable status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestTenantSelfPasswordChangeRevokesCurrentSession(t *testing.T) {
	h := newHarness(t)
	_, signed := bootstrap(h, "Password", "self-password@example.com")
	if status := h.call(http.MethodPost, "/admin/v1/auth/password", signed, map[string]string{
		"current_password": ownerPassword,
		"new_password":     "replacement-password",
	}, nil); status != http.StatusNoContent {
		t.Fatalf("change password status = %d, want %d", status, http.StatusNoContent)
	}
	if status := h.call(http.MethodGet, "/admin/v1/users", signed, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("old session status = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := loginTenantStatus(h, "self-password@example.com", ownerPassword, nil); status != http.StatusUnauthorized {
		t.Errorf("old password login status = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := loginTenantStatus(h, "self-password@example.com", "replacement-password", nil); status != http.StatusOK {
		t.Errorf("new password login status = %d, want %d", status, http.StatusOK)
	}
}

func loginTenant(t *testing.T, h *harness, email, password string) string {
	t.Helper()
	var response types.LoginResponse
	if status := loginTenantStatus(h, email, password, &response); status != http.StatusOK {
		t.Fatalf("login status = %d, want %d", status, http.StatusOK)
	}
	return response.Token
}

func loginTenantStatus(h *harness, email, password string, out any) int {
	return h.call(http.MethodPost, "/admin/v1/auth/login", "", types.LoginRequest{Email: email, Password: password}, out)
}

func assertKeyRejected(t *testing.T, h *harness, plaintext string) {
	t.Helper()
	if _, err := gatewayVerifier(h, newSteppableClock()).Verify(context.Background(), plaintext); !errors.Is(err, httpapi.ErrKeyRejected) {
		t.Errorf("Verify error = %v, want %v", err, httpapi.ErrKeyRejected)
	}
}

func hOwnerID(t *testing.T, h *harness, session string) string {
	t.Helper()
	var users types.UserListResponse
	if status := h.call(http.MethodGet, "/admin/v1/users", session, nil, &users); status != http.StatusOK {
		t.Fatalf("list users status = %d, want %d", status, http.StatusOK)
	}
	for _, user := range users.Items {
		if user.Role == "owner" {
			return user.ID
		}
	}
	t.Fatal("owner not found")
	return ""
}

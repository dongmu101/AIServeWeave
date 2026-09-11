package e2e_test

import (
	"net/http"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

func TestPlatformOperatorLifecycleOverHTTP(t *testing.T) {
	h := newHarness(t)
	primary := bootstrapPlatformOperator(h, "primary-operator@example.com")

	var managed types.PlatformOperator
	if status := h.call(http.MethodPost, "/operator/v1/operators", primary, types.CreatePlatformOperatorRequest{
		Email: "managed-operator@example.com", Password: ownerPassword, Name: "Managed operator",
	}, &managed); status != http.StatusCreated {
		t.Fatalf("create operator status = %d, want %d", status, http.StatusCreated)
	}
	managedSession := loginPlatform(t, h, managed.Email, ownerPassword)
	if status := h.call(http.MethodPut, "/operator/v1/operators/"+managed.ID+"/password", primary,
		map[string]string{"new_password": "operator-replacement"}, nil); status != http.StatusNoContent {
		t.Fatalf("reset operator password status = %d, want %d", status, http.StatusNoContent)
	}
	if status := h.call(http.MethodGet, "/operator/v1/operators", managedSession, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("old operator session status = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := loginPlatformStatus(h, managed.Email, ownerPassword, nil); status != http.StatusUnauthorized {
		t.Errorf("old operator password status = %d, want %d", status, http.StatusUnauthorized)
	}

	managedSession = loginPlatform(t, h, managed.Email, "operator-replacement")
	if status := h.call(http.MethodPost, "/operator/v1/operators/"+managed.ID+"/disable", primary, nil, nil); status != http.StatusNoContent {
		t.Fatalf("disable operator status = %d, want %d", status, http.StatusNoContent)
	}
	if status := h.call(http.MethodGet, "/operator/v1/operators", managedSession, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("disabled operator session status = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := loginPlatformStatus(h, managed.Email, "operator-replacement", nil); status != http.StatusUnauthorized {
		t.Errorf("disabled operator login status = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := h.call(http.MethodPost, "/operator/v1/operators/"+managed.ID+"/enable", primary, nil, nil); status != http.StatusNoContent {
		t.Fatalf("enable operator status = %d, want %d", status, http.StatusNoContent)
	}
	_ = loginPlatform(t, h, managed.Email, "operator-replacement")
}

func TestPlatformSelfPasswordChangeRevokesCurrentSession(t *testing.T) {
	h := newHarness(t)
	signed := bootstrapPlatformOperator(h, "self-platform-password@example.com")
	if status := h.call(http.MethodPost, "/operator/v1/auth/password", signed, map[string]string{
		"current_password": ownerPassword,
		"new_password":     "platform-replacement",
	}, nil); status != http.StatusNoContent {
		t.Fatalf("change platform password status = %d, want %d", status, http.StatusNoContent)
	}
	if status := h.call(http.MethodGet, "/operator/v1/audit", signed, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("old platform session status = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := loginPlatformStatus(h, "self-platform-password@example.com", "platform-replacement", nil); status != http.StatusOK {
		t.Errorf("new platform password status = %d, want %d", status, http.StatusOK)
	}
}

func loginPlatform(t *testing.T, h *harness, email, password string) string {
	t.Helper()
	var response types.PlatformLoginResponse
	if status := loginPlatformStatus(h, email, password, &response); status != http.StatusOK {
		t.Fatalf("platform login status = %d, want %d", status, http.StatusOK)
	}
	return response.Token
}

func loginPlatformStatus(h *harness, email, password string, out any) int {
	return h.call(http.MethodPost, "/admin/v1/platform/auth/login", "", types.LoginRequest{Email: email, Password: password}, out)
}

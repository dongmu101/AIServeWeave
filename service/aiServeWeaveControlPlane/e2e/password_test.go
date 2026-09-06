package e2e_test

import (
	"net/http"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// TestInitialPasswordsViaHTTP verifies unrestricted passwords across the wire.
// TestInitialPasswordsViaHTTP 验证无策略限制的初始密码可通过真实 HTTP 创建并登录。
func TestInitialPasswordsViaHTTP(t *testing.T) {
	cases := []struct{ name, password string }{
		{"empty", ""}, {"short", "1"}, {"unicode", "密码"}, {"long", strings.Repeat("x", 72) + "suffix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			var created types.CreateTenantResponse
			code := h.call(http.MethodPost, "/admin/v1/tenants", bootstrapToken, types.CreateTenantRequest{Name: "Password test", OwnerEmail: "password@example.com", OwnerPassword: tc.password}, &created)
			if code != http.StatusCreated {
				t.Fatalf("create status = %d, want %d", code, http.StatusCreated)
			}
			var session types.LoginResponse
			code = h.call(http.MethodPost, "/admin/v1/auth/login", "", types.LoginRequest{Email: created.Owner.Email, Password: tc.password}, &session)
			if code != http.StatusOK {
				t.Fatalf("login status = %d, want %d", code, http.StatusOK)
			}
			if session.User.ID != created.Owner.ID {
				t.Fatalf("login user = %q, want %q", session.User.ID, created.Owner.ID)
			}
			code = h.call(http.MethodPost, "/admin/v1/auth/login", "", types.LoginRequest{Email: created.Owner.Email, Password: tc.password + "!"}, nil)
			if code != http.StatusUnauthorized {
				t.Fatalf("wrong password status = %d, want %d", code, http.StatusUnauthorized)
			}
		})
	}
}

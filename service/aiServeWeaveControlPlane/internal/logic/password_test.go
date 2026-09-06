package logic_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// TestInitialPasswordsRoundTripWithoutPolicy covers both account creation paths.
// TestInitialPasswordsRoundTripWithoutPolicy 覆盖两条账号创建路径的无密码策略限制回环。
func TestInitialPasswordsRoundTripWithoutPolicy(t *testing.T) {
	cases := []struct{ name, password string }{
		{"empty", ""},
		{"single digit", "1"},
		{"short numeric", "123456"},
		{"whitespace preserved", "  "},
		{"unicode", "密码"},
		{"exact bcrypt boundary", strings.Repeat("x", 72)},
		{"unicode bcrypt boundary", strings.Repeat("密", 24)},
		{"beyond bcrypt boundary", strings.Repeat("x", 72) + "tail"},
		{"long unicode", strings.Repeat("密码", 512)},
	}
	for _, tc := range cases {
		for _, path := range []string{"tenant owner", "tenant user"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				f := newFixture(t)
				ctx := context.Background()
				var user model.User
				var err error
				if path == "tenant owner" {
					_, user, err = f.svc.CreateTenant(ctx, "Password test", "password-test@example.com", tc.password, "")
				} else {
					user, err = f.svc.CreateUser(ctx, f.ownerAt, "password-test@example.com", tc.password, "Password test", model.RoleMember)
				}
				if err != nil {
					t.Fatalf("create error = %v, want nil", err)
				}
				signedIn, err := f.svc.Authenticate(ctx, user.Email, tc.password, "")
				if err != nil || signedIn.ID != user.ID {
					t.Fatalf("login id/error = %q/%v, want %q/nil", signedIn.ID, err, user.ID)
				}
				_, err = f.svc.Authenticate(ctx, user.Email, tc.password+"!", "")
				if !errors.Is(err, logic.ErrInvalidCredentials) {
					t.Fatalf("changed password error = %v, want ErrInvalidCredentials", err)
				}
			})
		}
	}
}

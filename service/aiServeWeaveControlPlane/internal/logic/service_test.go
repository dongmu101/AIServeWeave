package logic_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// TestAuthenticateRefusesEveryFailureAlike is the account-enumeration
// assertion: an unknown email, a wrong password and a suspended account must
// be indistinguishable to the caller. A sign-in form that answers "does this
// person have an account here" is a directory.
//
// TestAuthenticateRefusesEveryFailureAlike 是账户枚举防护的断言：email 不存在、密码
// 错误与账户已停用，对调用方而言必须无法区分。一个会回答「这个人在这里有没有账户」
// 的登录表单，就是一份通讯录。
func TestAuthenticateRefusesEveryFailureAlike(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		password string
		prepare  func(f *fixture)
	}{
		{
			name:     "unknown email",
			email:    "nobody@example.com",
			password: testPassword,
		},
		{
			name:     "wrong password",
			email:    "owner@example.com",
			password: "not-the-right-password",
		},
		{
			name:     "empty password",
			email:    "owner@example.com",
			password: "",
		},
		{
			name:     "suspended account",
			email:    "owner@example.com",
			password: testPassword,
			prepare: func(f *fixture) {
				user, err := f.store.GetUserByEmail(context.Background(), "owner@example.com")
				if err != nil {
					f.t.Fatalf("reading the owner: %v", err)
				}
				user.Status = model.StatusSuspended
				f.store.ReplaceUser(user)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.prepare != nil {
				tt.prepare(f)
			}

			_, err := f.svc.Authenticate(context.Background(), tt.email, tt.password, "10.0.0.1")
			if !errors.Is(err, logic.ErrInvalidCredentials) {
				t.Errorf("Authenticate error = %v, want ErrInvalidCredentials", err)
			}
		})
	}
}

func TestAuthenticateAcceptsTheOwner(t *testing.T) {
	f := newFixture(t)

	user, err := f.svc.Authenticate(context.Background(), "owner@example.com", testPassword, "10.0.0.1")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.ID != f.owner.ID {
		t.Errorf("authenticated as %q, want %q", user.ID, f.owner.ID)
	}
	if user.LastLoginAt == nil || !user.LastLoginAt.Equal(f.clock.Now()) {
		t.Errorf("LastLoginAt = %v, want the current clock reading %v", user.LastLoginAt, f.clock.Now())
	}
	if user.PasswordHash == "" {
		t.Errorf("the returned user carries no password hash; the test below assumes it is never rendered")
	}
}

// TestEmailIsCaseInsensitive asserts one person cannot end up with two
// accounts that differ only in case, and can sign in however they type it.
//
// TestEmailIsCaseInsensitive 断言一个人不会拥有两个仅大小写不同的账户，并且无论怎么
// 输入都能登录。
func TestEmailIsCaseInsensitive(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.Authenticate(context.Background(), "OWNER@Example.COM", testPassword, "10.0.0.1"); err != nil {
		t.Errorf("Authenticate with differently-cased email: %v", err)
	}
	_, err := f.svc.CreateUser(context.Background(), f.ownerAt, "Owner@example.com", testPassword, "dup", model.RoleMember)
	if !errors.Is(err, logic.ErrConflict) {
		t.Errorf("creating a user whose email differs only in case: err = %v, want ErrConflict", err)
	}
}

func TestCreateUserPermissions(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		wantErr error
	}{
		{name: "an owner may add users", role: model.RoleOwner},
		{name: "an admin may not", role: model.RoleAdmin, wantErr: logic.ErrForbidden},
		{name: "a member may not", role: model.RoleMember, wantErr: logic.ErrForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.svc.CreateUser(context.Background(),
				f.actorWithRole(tt.role), "new@example.com", testPassword, "New", model.RoleMember)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("CreateUser error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCreateUserValidation(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		password string
		role     string
		wantErr  error
	}{
		{name: "valid", email: "a@example.com", password: testPassword, role: model.RoleMember},
		{name: "empty email", email: "  ", password: testPassword, role: model.RoleMember, wantErr: logic.ErrInvalidInput},
		{name: "short password", email: "b@example.com", password: "short", role: model.RoleMember, wantErr: logic.ErrInvalidInput},
		{
			name:     "password past bcrypt's truncation point",
			email:    "c@example.com",
			password: strings.Repeat("x", 73),
			role:     model.RoleMember,
			wantErr:  logic.ErrInvalidInput,
		},
		{name: "unknown role", email: "d@example.com", password: testPassword, role: "superuser", wantErr: logic.ErrInvalidInput},
		{name: "duplicate email", email: "owner@example.com", password: testPassword, role: model.RoleMember, wantErr: logic.ErrConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.svc.CreateUser(context.Background(), f.ownerAt, tt.email, tt.password, "n", tt.role)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("CreateUser error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestPasswordIsNeverStoredInPlaintext reaches into the store for the same
// reason the API key test does: the rule is about what is persisted.
//
// TestPasswordIsNeverStoredInPlaintext 直接查看 store，理由与 API Key 那个测试相同：
// 这条规则约束的是「被持久化了什么」。
func TestPasswordIsNeverStoredInPlaintext(t *testing.T) {
	f := newFixture(t)

	user, err := f.store.GetUserByEmail(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatalf("reading the owner: %v", err)
	}
	if strings.Contains(user.PasswordHash, testPassword) {
		t.Errorf("the stored digest contains the password verbatim")
	}
	if !strings.HasPrefix(user.PasswordHash, "$2") {
		t.Errorf("PasswordHash = %q, want a bcrypt digest", user.PasswordHash)
	}
}

// TestTenantCreationIsAudited covers the bootstrap path's audit record, whose
// actor is the system rather than a user.
//
// TestTenantCreationIsAudited 覆盖引导路径的审计记录，其行为人是系统而不是某个用户。
func TestTenantCreationIsAudited(t *testing.T) {
	f := newFixture(t)

	entries, err := f.svc.ListAudit(context.Background(), f.ownerAt, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}

	var found bool
	for _, entry := range entries {
		if entry.Action == model.ActionTenantCreate {
			found = true
			if entry.ActorID != "" {
				t.Errorf("tenant creation names actor %q, want the system (empty)", entry.ActorID)
			}
			if entry.Target != f.tenant.ID {
				t.Errorf("Target = %q, want %q", entry.Target, f.tenant.ID)
			}
		}
	}
	if !found {
		t.Errorf("no %s audit record", model.ActionTenantCreate)
	}
}

// TestAuditIsScopedToTheTenant asserts one tenant cannot read another's
// administrative history.
//
// TestAuditIsScopedToTheTenant 断言一个租户无法读取另一个租户的管理历史。
func TestAuditIsScopedToTheTenant(t *testing.T) {
	f := newFixture(t)
	f.mustCreateKey(f.ownerAt, "mine")

	otherTenant, otherOwner, err := f.svc.CreateTenant(context.Background(), "Other", "other@example.com", testPassword, "10.0.0.9")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	other := logic.Actor{UserID: otherOwner.ID, TenantID: otherTenant.ID, Role: model.RoleOwner}

	entries, err := f.svc.ListAudit(context.Background(), other, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, entry := range entries {
		if entry.TenantID != otherTenant.ID {
			t.Errorf("tenant %q read an audit record belonging to %q", otherTenant.ID, entry.TenantID)
		}
	}
}

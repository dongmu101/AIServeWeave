package logic_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/apikey"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/session"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

func (f *fixture) userWithSession(role, email string) (model.User, logic.Actor) {
	f.t.Helper()
	user, err := f.svc.CreateUser(context.Background(), f.ownerAt, email, testPassword, email, role)
	if err != nil {
		f.t.Fatalf("CreateUser: %v", err)
	}
	sessionID := "ses_" + user.ID
	if err := f.sessions.Create(context.Background(), session.Record{
		ID: sessionID, Subject: session.Subject{Kind: session.SubjectTenantUser, ID: user.ID},
		TenantID: user.TenantID, Role: user.Role, ExpiresAt: f.clock.Now().Add(time.Hour),
	}); err != nil {
		f.t.Fatalf("Create session: %v", err)
	}
	return user, logic.Actor{SessionID: sessionID, UserID: user.ID, TenantID: user.TenantID, Role: user.Role, IP: "10.0.0.2"}
}

func TestChangeOwnPasswordRequiresCurrentPasswordAndRevokesSessions(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.ChangeOwnPassword(context.Background(), f.ownerAt, "wrong", "new-password"); !errors.Is(err, logic.ErrInvalidCredentials) {
		t.Errorf("wrong-current-password error = %v, want %v", err, logic.ErrInvalidCredentials)
	}
	if err := f.sessions.Validate(context.Background(), f.ownerAt.SessionID,
		session.Subject{Kind: session.SubjectTenantUser, ID: f.owner.ID}, f.tenant.ID, f.owner.Role); err != nil {
		t.Fatalf("failed password attempt revoked session: %v", err)
	}

	if err := f.svc.ChangeOwnPassword(context.Background(), f.ownerAt, testPassword, "new-password"); err != nil {
		t.Fatalf("ChangeOwnPassword: %v", err)
	}
	if err := f.sessions.Validate(context.Background(), f.ownerAt.SessionID,
		session.Subject{Kind: session.SubjectTenantUser, ID: f.owner.ID}, f.tenant.ID, f.owner.Role); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("Validate old session error = %v, want %v", err, session.ErrInvalid)
	}
	if _, err := f.svc.Authenticate(context.Background(), f.owner.Email, testPassword, "10.0.0.1"); !errors.Is(err, logic.ErrInvalidCredentials) {
		t.Errorf("old password error = %v, want %v", err, logic.ErrInvalidCredentials)
	}
	if _, err := f.svc.Authenticate(context.Background(), f.owner.Email, "new-password", "10.0.0.1"); err != nil {
		t.Errorf("new password error = %v, want nil", err)
	}
}

func TestDisableUserRevokesSessionsAndCreatedKeysPermanently(t *testing.T) {
	f := newFixture(t)
	user, actor := f.userWithSession(model.RoleAdmin, "disabled@example.com")
	created := f.mustCreateKey(actor, "automation")

	if err := f.svc.DisableUser(context.Background(), f.ownerAt, user.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	stored, err := f.store.GetUser(context.Background(), f.tenant.ID, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if stored.Status != model.StatusSuspended {
		t.Errorf("Status = %q, want %q", stored.Status, model.StatusSuspended)
	}
	if err := f.sessions.Validate(context.Background(), actor.SessionID,
		session.Subject{Kind: session.SubjectTenantUser, ID: user.ID}, f.tenant.ID, user.Role); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("Validate disabled session error = %v, want %v", err, session.ErrInvalid)
	}
	if _, err := f.svc.VerifyKeyHash(context.Background(), apikey.Hash(created.Plaintext)); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("disabled creator key error = %v, want %v", err, logic.ErrNotFound)
	}
	if err := f.svc.EnableUser(context.Background(), f.ownerAt, user.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	if _, err := f.svc.Authenticate(context.Background(), user.Email, testPassword, "10.0.0.2"); err != nil {
		t.Errorf("Authenticate after enable error = %v, want nil", err)
	}
	if _, err := f.svc.VerifyKeyHash(context.Background(), apikey.Hash(created.Plaintext)); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("key after enable error = %v, want %v", err, logic.ErrNotFound)
	}
}

func TestRoleChangeRevokesSessionsButKeepsKeys(t *testing.T) {
	f := newFixture(t)
	user, actor := f.userWithSession(model.RoleAdmin, "role@example.com")
	created := f.mustCreateKey(actor, "automation")

	if err := f.svc.ChangeUserRole(context.Background(), f.ownerAt, user.ID, model.RoleMember); err != nil {
		t.Fatalf("ChangeUserRole: %v", err)
	}
	stored, err := f.store.GetUser(context.Background(), f.tenant.ID, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if stored.Role != model.RoleMember {
		t.Errorf("Role = %q, want %q", stored.Role, model.RoleMember)
	}
	if err := f.sessions.Validate(context.Background(), actor.SessionID,
		session.Subject{Kind: session.SubjectTenantUser, ID: user.ID}, f.tenant.ID, user.Role); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("Validate old-role session error = %v, want %v", err, session.ErrInvalid)
	}
	if _, err := f.svc.VerifyKeyHash(context.Background(), apikey.Hash(created.Plaintext)); err != nil {
		t.Errorf("key after role change error = %v, want nil", err)
	}
}

func TestDisabledUserCannotCreateKeyThroughAnAlreadyAuthorizedRequest(t *testing.T) {
	f := newFixture(t)
	user, actor := f.userWithSession(model.RoleAdmin, "in-flight-key@example.com")
	if err := f.svc.DisableUser(context.Background(), f.ownerAt, user.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	if _, err := f.svc.CreateAPIKey(context.Background(), actor, "too-late", 0); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("CreateAPIKey error = %v, want %v", err, logic.ErrNotFound)
	}
}

func TestUserLifecyclePermissionAndVisibility(t *testing.T) {
	tests := []struct {
		name    string
		actor   func(*fixture) logic.Actor
		target  func(*fixture, model.User) string
		wantErr error
	}{
		{name: "admin cannot reset", actor: func(f *fixture) logic.Actor { return f.actorWithRole(model.RoleAdmin) }, target: func(_ *fixture, user model.User) string { return user.ID }, wantErr: logic.ErrForbidden},
		{name: "member cannot reset", actor: func(f *fixture) logic.Actor { return f.actorWithRole(model.RoleMember) }, target: func(_ *fixture, user model.User) string { return user.ID }, wantErr: logic.ErrForbidden},
		{name: "owner cannot administratively reset self", actor: func(f *fixture) logic.Actor { return f.ownerAt }, target: func(f *fixture, _ model.User) string { return f.owner.ID }, wantErr: logic.ErrConflict},
		{name: "missing target is hidden", actor: func(f *fixture) logic.Actor { return f.ownerAt }, target: func(_ *fixture, _ model.User) string { return model.NewID(model.PrefixUser) }, wantErr: logic.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			user, _ := f.userWithSession(model.RoleMember, "target@example.com")
			err := f.svc.ResetUserPassword(context.Background(), tt.actor(f), tt.target(f, user), "replacement")
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ResetUserPassword error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestConcurrentDisablesPreserveOneActiveOwner(t *testing.T) {
	f := newFixture(t)
	second, secondActor := f.userWithSession(model.RoleOwner, "second-owner@example.com")

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, request := range []struct {
		actor  logic.Actor
		target string
	}{{actor: f.ownerAt, target: second.ID}, {actor: secondActor, target: f.owner.ID}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- f.svc.DisableUser(context.Background(), request.actor, request.target)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var successes, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, logic.ErrConflict):
			conflicts++
		default:
			t.Errorf("DisableUser error = %v, want nil or %v", err, logic.ErrConflict)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Errorf("results = %d successes, %d conflicts; want 1 and 1", successes, conflicts)
	}
	page, err := f.store.ListUsers(context.Background(), f.tenant.ID, store.ListQuery{}, store.UserFilter{})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	activeOwners := 0
	for _, user := range page.Items {
		if user.Status == model.StatusActive && user.Role == model.RoleOwner {
			activeOwners++
		}
	}
	if activeOwners != 1 {
		t.Errorf("active owners = %d, want 1", activeOwners)
	}
}

package logic_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/registryclient"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

// fakeRegistryClient is a logic.RegistryClient substitute, so these tests
// exercise the platform node-ops methods without a real Registry process —
// the same reasoning memstore exists for the store interfaces.
type fakeRegistryClient struct {
	mu sync.Mutex

	calls  []string // "approve:node-1", "disable:node-1", ...
	states []registryclient.NodeState
	err    error // returned by every call when set
}

func (f *fakeRegistryClient) record(action, nodeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, action+":"+nodeID)
	return f.err
}

func (f *fakeRegistryClient) ApproveNode(_ context.Context, nodeID string) error {
	return f.record("approve", nodeID)
}
func (f *fakeRegistryClient) DisableNode(_ context.Context, nodeID string) error {
	return f.record("disable", nodeID)
}
func (f *fakeRegistryClient) EnableNode(_ context.Context, nodeID string) error {
	return f.record("enable", nodeID)
}
func (f *fakeRegistryClient) SetMaintenance(_ context.Context, nodeID string) error {
	return f.record("maintenance.enter", nodeID)
}
func (f *fakeRegistryClient) ClearMaintenance(_ context.Context, nodeID string) error {
	return f.record("maintenance.exit", nodeID)
}
func (f *fakeRegistryClient) ListNodeStates(_ context.Context) ([]registryclient.NodeState, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.states, nil
}

var _ logic.RegistryClient = (*fakeRegistryClient)(nil)

// platformFixture is a Service wired with a fakeRegistryClient, plus a
// platform operator already created — the starting point for every test in
// this file.
type platformFixture struct {
	t        *testing.T
	svc      *logic.Service
	store    *memstore.Store
	clock    *fakeClock
	registry *fakeRegistryClient
	operator model.PlatformOperator
	actor    logic.Actor
}

func newPlatformFixture(t *testing.T) *platformFixture {
	t.Helper()
	st := memstore.New()
	clock := newFakeClock()
	registry := &fakeRegistryClient{}
	svc := logic.New(st, clock, logic.WithRegistryClient(registry))

	operator, err := svc.CreatePlatformOperator(context.Background(), "operator@example.com", testPassword, "Ops", "10.0.0.1")
	if err != nil {
		t.Fatalf("CreatePlatformOperator: %v", err)
	}
	return &platformFixture{
		t:        t,
		svc:      svc,
		store:    st,
		clock:    clock,
		registry: registry,
		operator: operator,
		actor: logic.Actor{
			UserID:   operator.ID,
			TenantID: model.PlatformScope,
			Role:     model.RolePlatformOperator,
			IP:       "10.0.0.1",
		},
	}
}

func TestPlatformAuthenticateRefusesEveryFailureAlike(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		password string
		prepare  func(f *platformFixture)
	}{
		{name: "unknown email", email: "nobody@example.com", password: testPassword},
		{name: "wrong password", email: "operator@example.com", password: "not-the-right-password"},
		{
			name: "suspended account", email: "operator@example.com", password: testPassword,
			prepare: func(f *platformFixture) {
				operator := f.operator
				operator.Status = model.StatusSuspended
				f.store.ReplacePlatformOperator(operator)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPlatformFixture(t)
			if tt.prepare != nil {
				tt.prepare(f)
			}
			_, err := f.svc.PlatformAuthenticate(context.Background(), tt.email, tt.password, "10.0.0.1")
			if !errors.Is(err, logic.ErrInvalidCredentials) {
				t.Errorf("PlatformAuthenticate error = %v, want ErrInvalidCredentials", err)
			}
		})
	}
}

func TestPlatformAuthenticateAcceptsTheOperatorAndAudits(t *testing.T) {
	f := newPlatformFixture(t)

	operator, err := f.svc.PlatformAuthenticate(context.Background(), "operator@example.com", testPassword, "10.0.0.1")
	if err != nil {
		t.Fatalf("PlatformAuthenticate: %v", err)
	}
	if operator.ID != f.operator.ID {
		t.Errorf("authenticated as %q, want %q", operator.ID, f.operator.ID)
	}
	if operator.LastLoginAt == nil || !operator.LastLoginAt.Equal(f.clock.Now()) {
		t.Errorf("LastLoginAt = %v, want %v", operator.LastLoginAt, f.clock.Now())
	}

	page, err := f.store.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, entry := range page.Items {
		if entry.Action == model.ActionPlatformOperatorLogin && entry.Target == operator.ID {
			found = true
		}
	}
	if !found {
		t.Error("no ActionPlatformOperatorLogin audit entry was recorded under PlatformScope")
	}
}

func TestNodeOpsRejectATenantActor(t *testing.T) {
	f := newPlatformFixture(t)
	tenantActor := logic.Actor{UserID: model.NewID(model.PrefixUser), TenantID: model.NewID(model.PrefixTenant), Role: model.RoleOwner}

	if err := f.svc.ApproveNode(context.Background(), tenantActor, "node-1"); !errors.Is(err, logic.ErrForbidden) {
		t.Errorf("ApproveNode with a tenant actor = %v, want ErrForbidden", err)
	}
	if len(f.registry.calls) != 0 {
		t.Errorf("the Registry was called (%v) for a request that should have been refused first", f.registry.calls)
	}
}

func TestNodeOpsRequireARegistryClient(t *testing.T) {
	st := memstore.New()
	svc := logic.New(st, newFakeClock()) // no WithRegistryClient
	operator, err := svc.CreatePlatformOperator(context.Background(), "operator@example.com", testPassword, "", "10.0.0.1")
	if err != nil {
		t.Fatalf("CreatePlatformOperator: %v", err)
	}
	actor := logic.Actor{UserID: operator.ID, TenantID: model.PlatformScope, Role: model.RolePlatformOperator}

	if err := svc.ApproveNode(context.Background(), actor, "node-1"); !errors.Is(err, logic.ErrRegistryUnconfigured) {
		t.Errorf("ApproveNode with no RegistryClient = %v, want ErrRegistryUnconfigured", err)
	}
}

func TestNodeOpsRejectAnEmptyNodeID(t *testing.T) {
	f := newPlatformFixture(t)
	if err := f.svc.ApproveNode(context.Background(), f.actor, ""); !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("ApproveNode(\"\") = %v, want ErrInvalidInput", err)
	}
}

// TestNodeOpsForwardToTheRegistryAndAudit covers all five write methods:
// each must call the Registry with the right node_id and record a
// distinctly actioned audit entry under PlatformScope.
func TestNodeOpsForwardToTheRegistryAndAudit(t *testing.T) {
	tests := []struct {
		name       string
		call       func(f *platformFixture) error
		wantCall   string
		wantAction string
	}{
		{
			name:       "ApproveNode",
			call:       func(f *platformFixture) error { return f.svc.ApproveNode(context.Background(), f.actor, "node-1") },
			wantCall:   "approve:node-1",
			wantAction: model.ActionNodeApprove,
		},
		{
			name:       "DisableNode",
			call:       func(f *platformFixture) error { return f.svc.DisableNode(context.Background(), f.actor, "node-1") },
			wantCall:   "disable:node-1",
			wantAction: model.ActionNodeDisable,
		},
		{
			name:       "EnableNode",
			call:       func(f *platformFixture) error { return f.svc.EnableNode(context.Background(), f.actor, "node-1") },
			wantCall:   "enable:node-1",
			wantAction: model.ActionNodeEnable,
		},
		{
			name:       "EnterMaintenance",
			call:       func(f *platformFixture) error { return f.svc.EnterMaintenance(context.Background(), f.actor, "node-1") },
			wantCall:   "maintenance.enter:node-1",
			wantAction: model.ActionNodeMaintenanceEnter,
		},
		{
			name:       "ExitMaintenance",
			call:       func(f *platformFixture) error { return f.svc.ExitMaintenance(context.Background(), f.actor, "node-1") },
			wantCall:   "maintenance.exit:node-1",
			wantAction: model.ActionNodeMaintenanceExit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPlatformFixture(t)
			if err := tt.call(f); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			if len(f.registry.calls) != 1 || f.registry.calls[0] != tt.wantCall {
				t.Errorf("registry calls = %v, want [%s]", f.registry.calls, tt.wantCall)
			}
			page, err := f.store.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
			if err != nil {
				t.Fatalf("ListAudit: %v", err)
			}
			found := false
			for _, entry := range page.Items {
				if entry.Action == tt.wantAction && entry.Target == "node-1" && entry.ActorID == f.operator.ID {
					found = true
				}
			}
			if !found {
				t.Errorf("no %s audit entry recorded for actor %s", tt.wantAction, f.operator.ID)
			}
		})
	}
}

// TestNodeOpsDoNotAuditOnFailure asserts a Registry failure leaves no audit
// trail behind — see service.go's audit doc comment for why an action must
// actually have happened before it is recorded.
func TestNodeOpsDoNotAuditOnFailure(t *testing.T) {
	f := newPlatformFixture(t)
	f.registry.err = errors.New("registry unreachable")

	if err := f.svc.ApproveNode(context.Background(), f.actor, "node-1"); err == nil {
		t.Fatal("ApproveNode with a failing Registry = nil error, want the Registry's error")
	}
	page, err := f.store.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, entry := range page.Items {
		if entry.Action == model.ActionNodeApprove {
			t.Errorf("an audit entry was recorded for a failed ApproveNode call: %+v", entry)
		}
	}
}

func TestListNodeStatesForwardsToTheRegistry(t *testing.T) {
	f := newPlatformFixture(t)
	f.registry.states = []registryclient.NodeState{
		{NodeID: "node-1", PendingApproval: true},
		{NodeID: "node-2", Disabled: true},
	}

	states, err := f.svc.ListNodeStates(context.Background(), f.actor)
	if err != nil {
		t.Fatalf("ListNodeStates: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("ListNodeStates returned %d states, want 2", len(states))
	}
}

func TestListNodeStatesRejectsATenantActor(t *testing.T) {
	f := newPlatformFixture(t)
	tenantActor := logic.Actor{UserID: model.NewID(model.PrefixUser), TenantID: model.NewID(model.PrefixTenant), Role: model.RoleOwner}

	if _, err := f.svc.ListNodeStates(context.Background(), tenantActor); !errors.Is(err, logic.ErrForbidden) {
		t.Errorf("ListNodeStates with a tenant actor = %v, want ErrForbidden", err)
	}
}

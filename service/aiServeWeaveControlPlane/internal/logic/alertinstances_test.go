package logic_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

func TestListAlertInstancesRejectsANonPlatformActor(t *testing.T) {
	f := newPlatformFixture(t)
	tenantActor := logic.Actor{UserID: model.NewID(model.PrefixUser), TenantID: model.NewID(model.PrefixTenant), Role: model.RoleOwner}

	if _, err := f.svc.ListAlertInstances(context.Background(), tenantActor, store.ListQuery{}, store.AlertInstanceFilter{}); !errors.Is(err, logic.ErrForbidden) {
		t.Errorf("ListAlertInstances with a tenant actor = %v, want %v", err, logic.ErrForbidden)
	}
}

func TestListAlertInstancesPassesFilterThrough(t *testing.T) {
	f := newPlatformFixture(t)
	now := f.clock.Now()
	firing := model.AlertInstance{ID: "ali_firing", RuleID: "rule_a", Status: model.AlertStatusFiring, CreatedAt: now}
	resolved := model.AlertInstance{ID: "ali_resolved", RuleID: "rule_b", Status: model.AlertStatusResolved, CreatedAt: now}
	if err := f.store.CreateAlertInstance(context.Background(), &firing); err != nil {
		t.Fatalf("CreateAlertInstance(firing): %v", err)
	}
	if err := f.store.CreateAlertInstance(context.Background(), &resolved); err != nil {
		t.Fatalf("CreateAlertInstance(resolved): %v", err)
	}

	page, err := f.svc.ListAlertInstances(context.Background(), f.actor, store.ListQuery{}, store.AlertInstanceFilter{Status: model.AlertStatusFiring})
	if err != nil {
		t.Fatalf("ListAlertInstances(firing): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != firing.ID {
		t.Errorf("ListAlertInstances(Status=firing) = %+v, want only %q", page.Items, firing.ID)
	}

	page, err = f.svc.ListAlertInstances(context.Background(), f.actor, store.ListQuery{}, store.AlertInstanceFilter{RuleID: "rule_b"})
	if err != nil {
		t.Fatalf("ListAlertInstances(rule_b): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != resolved.ID {
		t.Errorf("ListAlertInstances(RuleID=rule_b) = %+v, want only %q", page.Items, resolved.ID)
	}
}

func TestAcknowledgeAlertRejectsANonPlatformActor(t *testing.T) {
	f := newPlatformFixture(t)
	tenantActor := logic.Actor{UserID: model.NewID(model.PrefixUser), TenantID: model.NewID(model.PrefixTenant), Role: model.RoleOwner}

	if _, err := f.svc.AcknowledgeAlert(context.Background(), tenantActor, "ali_1"); !errors.Is(err, logic.ErrForbidden) {
		t.Errorf("AcknowledgeAlert with a tenant actor = %v, want %v", err, logic.ErrForbidden)
	}
}

func TestAcknowledgeAlertSucceedsAndAudits(t *testing.T) {
	f := newPlatformFixture(t)
	instance := model.AlertInstance{ID: "ali_1", RuleID: "rule_a", Status: model.AlertStatusFiring, CreatedAt: f.clock.Now()}
	if err := f.store.CreateAlertInstance(context.Background(), &instance); err != nil {
		t.Fatalf("CreateAlertInstance: %v", err)
	}

	acked, err := f.svc.AcknowledgeAlert(context.Background(), f.actor, instance.ID)
	if err != nil {
		t.Fatalf("AcknowledgeAlert: %v", err)
	}
	if acked.Status != model.AlertStatusAcknowledged || acked.AcknowledgedBy != f.actor.UserID {
		t.Errorf("AcknowledgeAlert() = %+v, want Status=%q AcknowledgedBy=%q", acked, model.AlertStatusAcknowledged, f.actor.UserID)
	}

	page, err := f.store.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, entry := range page.Items {
		if entry.Action == model.ActionAlertAcknowledge && entry.Target == instance.ID && entry.TenantID == model.PlatformScope && entry.ActorID == f.actor.UserID {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s audit entry recorded under PlatformScope for instance %s", model.ActionAlertAcknowledge, instance.ID)
	}
}

func TestAcknowledgeAlertSurfacesConflictForAnAlreadyResolvedInstance(t *testing.T) {
	f := newPlatformFixture(t)
	resolvedAt := f.clock.Now().Add(-time.Minute)
	instance := model.AlertInstance{
		ID: "ali_1", RuleID: "rule_a", Status: model.AlertStatusResolved,
		CreatedAt: f.clock.Now(), ResolvedAt: &resolvedAt,
	}
	if err := f.store.CreateAlertInstance(context.Background(), &instance); err != nil {
		t.Fatalf("CreateAlertInstance: %v", err)
	}

	if _, err := f.svc.AcknowledgeAlert(context.Background(), f.actor, instance.ID); !errors.Is(err, logic.ErrConflict) {
		t.Errorf("AcknowledgeAlert(resolved) error = %v, want %v", err, logic.ErrConflict)
	}

	page, err := f.store.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, entry := range page.Items {
		if entry.Action == model.ActionAlertAcknowledge && entry.Target == instance.ID {
			t.Errorf("an audit entry was recorded for a failed AcknowledgeAlert call: %+v", entry)
		}
	}
}

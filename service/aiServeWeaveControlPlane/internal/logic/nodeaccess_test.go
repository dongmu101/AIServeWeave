package logic_test

import (
	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"errors"
	"testing"
)

func TestAgentSharingRequiresPlatformConfiguration(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	op := logic.Actor{TenantID: model.PlatformScope, Role: model.RolePlatformOperator, UserID: "operator"}
	permits := func(tenant string) bool {
		t.Helper()
		filter, err := f.svc.TenantAccountFilter(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		return filter("agent", "cli")
	}
	if permits(f.tenant.ID) {
		t.Fatal("unconfigured Agent was shared")
	}
	if _, err := f.svc.SetNodeAccessMode(ctx, f.ownerAt, "agent", []string{}, nodeaccess.ModeShared); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("tenant could enable sharing", err)
	}
	for _, mode := range []string{nodeaccess.ModeShared, nodeaccess.ModePrivate, nodeaccess.ModeDedicated} {
		tenants := []string{}
		if mode == nodeaccess.ModeDedicated {
			tenants = []string{f.tenant.ID}
		}
		entry, err := f.svc.SetNodeAccessMode(ctx, op, "agent", tenants, mode)
		if err != nil || entry.Mode != mode {
			t.Fatal(entry, err)
		}
		if permits(f.tenant.ID) != (mode != nodeaccess.ModePrivate) {
			t.Fatalf("wrong owner access in mode %s", mode)
		}
		if permits("other") != (mode == nodeaccess.ModeShared) {
			t.Fatalf("wrong other tenant access in mode %s", mode)
		}
	}
	if _, err := f.svc.SetNodeAccessMode(ctx, op, "agent", []string{f.tenant.ID}, nodeaccess.ModeShared); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("mixed shared and dedicated mode accepted", err)
	}
}

func TestAgentApprovalDefaultsToPrivateAndCanOptIntoSharing(t *testing.T) {
	f := newPlatformFixture(t)
	for _, shared := range []bool{false, true} {
		if err := f.svc.ApproveNodeWithSharing(t.Context(), f.actor, "agent", shared); err != nil {
			t.Fatal(err)
		}
		rows, err := f.svc.ListNodeAccess(t.Context(), f.actor)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		want := nodeaccess.ModePrivate
		if shared {
			want = nodeaccess.ModeShared
		}
		if rows[0].Mode != want {
			t.Fatalf("approval stored %s, want %s", rows[0].Mode, want)
		}
	}
}

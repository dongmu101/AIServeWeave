package memstore

import (
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// TestAgentInstallCodeLifecycle checks create, single redemption, expiry and
// restoring a redemption.
//
// TestAgentInstallCodeLifecycle 检查创建、仅一次兑换、过期以及撤销兑换。
func TestAgentInstallCodeLifecycle(t *testing.T) {
	s := New()
	ctx := t.Context()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	row := model.AgentInstallCode{CodeHash: "h1", NodeID: "node_1", TenantID: model.PlatformScope, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateAgentInstallCode(ctx, &row); err != nil {
		t.Fatalf("CreateAgentInstallCode() error = %v", err)
	}
	if err := s.CreateAgentInstallCode(ctx, &row); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate CreateAgentInstallCode() error = %v, want ErrConflict", err)
	}
	got, err := s.RedeemAgentInstallCode(ctx, "h1", now.Add(time.Minute))
	if err != nil || got.NodeID != "node_1" || got.RedeemedAt == nil {
		t.Fatalf("RedeemAgentInstallCode() = %+v, %v; want node_1 marked redeemed", got, err)
	}
	if _, err := s.RedeemAgentInstallCode(ctx, "h1", now.Add(2*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second RedeemAgentInstallCode() error = %v, want ErrNotFound", err)
	}
	if err := s.UnredeemAgentInstallCode(ctx, "h1"); err != nil {
		t.Fatalf("UnredeemAgentInstallCode() error = %v", err)
	}
	if _, err := s.RedeemAgentInstallCode(ctx, "h1", now.Add(time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RedeemAgentInstallCode() at expiry error = %v, want ErrNotFound", err)
	}
	if _, err := s.RedeemAgentInstallCode(ctx, "missing", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RedeemAgentInstallCode(missing) error = %v, want ErrNotFound", err)
	}
}

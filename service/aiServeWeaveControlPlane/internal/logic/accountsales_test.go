package logic_test

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
	"errors"
	"testing"
)

// No payment call is needed: this feature spends the existing credit balance.
type accountTestProvider struct{ billing.Provider }

func TestMonthlyAccountLifecycleAndAuthorization(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc = logic.New(f.store, f.clock, logic.WithBilling(logic.BillingConfig{Provider: &accountTestProvider{}}))
	op := logic.Actor{TenantID: model.PlatformScope, Role: model.RolePlatformOperator, UserID: "operator"}
	draft := model.AccountSale{NodeID: "agent", RuntimeID: "claude-a", Name: "Claude A", PriceMicro: 100, Active: true}
	if _, err := f.svc.SaveAccountSale(ctx, f.ownerAt, draft, true); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("tenant could sell accounts", err)
	}
	sale, err := f.svc.SaveAccountSale(ctx, op, draft, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.AdjustCredit(ctx, model.CreditLedgerEntry{ID: "credit", TenantID: f.tenant.ID, AmountMicro: 200, CreatedAt: f.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BuyAccount(ctx, f.actorWithRole(model.RoleMember), sale.ID, "00000000000000000000000000000001", 100); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("member could spend", err)
	}
	if _, err := f.svc.BuyAccount(ctx, f.ownerAt, sale.ID, "invalid", 100); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal(err)
	}
	receipt, err := f.svc.BuyAccount(ctx, f.ownerAt, sale.ID, "00000000000000000000000000000001", 100)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := f.svc.AccountPolicy(ctx, f.tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Only || !policy.Allows("agent", "claude-a", f.clock.Now()) || policy.Allows("agent", "claude-b", f.clock.Now()) {
		t.Fatal(policy)
	}
	other := logic.Actor{TenantID: "other", Role: model.RoleOwner, UserID: "other"}
	visible, err := f.svc.ListAccountSales(ctx, other)
	if err != nil || len(visible) != 0 {
		t.Fatalf("another tenant sees sold account: %+v %v", visible, err)
	}
	otherPolicy, err := f.svc.AccountPolicy(ctx, "other")
	if err != nil || otherPolicy.Allows("agent", "claude-a", f.clock.Now()) {
		t.Fatal("exclusive runtime open to another tenant")
	}
	f.clock.Advance(receipt.ExpiresAt.Sub(f.clock.Now()))
	if policy.Allows("agent", "claude-a", f.clock.Now()) {
		t.Fatal("cached authorization remained usable after expiry")
	}
	visible, err = f.svc.ListAccountSales(ctx, other)
	if err != nil || len(visible) != 1 || visible[0].TenantID != "" {
		t.Fatalf("expired offer leaked tenant: %+v %v", visible, err)
	}
	if _, err := f.svc.BuyAccount(ctx, f.ownerAt, sale.ID, "00000000000000000000000000000002", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BuyAccount(ctx, f.ownerAt, sale.ID, "00000000000000000000000000000003", 100); !errors.Is(err, store.ErrInsufficientCredit) {
		t.Fatal(err)
	}
	// A node binding still narrows account inventory and purchases.
	if _, err := f.svc.SetNodeAccess(ctx, op, "agent", []string{f.tenant.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BuyAccount(ctx, other, sale.ID, "00000000000000000000000000000004", 100); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("node dedication was bypassed", err)
	}
}

func TestMonthlyAccountBillingDisabled(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.ListAccountSales(context.Background(), f.ownerAt); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal(err)
	}
	p, err := f.svc.AccountPolicy(context.Background(), f.tenant.ID)
	if err != nil || !p.Unrestricted() {
		t.Fatal(p, err)
	}
}

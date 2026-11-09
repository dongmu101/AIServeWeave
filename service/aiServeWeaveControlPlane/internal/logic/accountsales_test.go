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

// Claude Code and Codex are independent sale items even on the same Agent.
func TestClaudeAndCodexOnOneAgentSellToDifferentTenants(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc = logic.New(f.store, f.clock, logic.WithBilling(logic.BillingConfig{Provider: &accountTestProvider{}}))
	op := logic.Actor{TenantID: model.PlatformScope, Role: model.RolePlatformOperator, UserID: "operator"}
	otherTenant, otherOwner, err := f.svc.CreateTenant(ctx, "Codex buyer", "codex-buyer@example.com", testPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	other := logic.Actor{TenantID: otherTenant.ID, UserID: otherOwner.ID, Role: model.RoleOwner}
	claude, err := f.svc.SaveAccountSale(ctx, op, model.AccountSale{NodeID: "agent-one", RuntimeID: "claude-a", Name: "Claude Code A", PriceMicro: 100, Active: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	codex, err := f.svc.SaveAccountSale(ctx, op, model.AccountSale{NodeID: "agent-one", RuntimeID: "codex-a", Name: "Codex A", PriceMicro: 200, Active: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []logic.Actor{f.ownerAt, other} {
		if _, _, err := f.store.AdjustCredit(ctx, model.CreditLedgerEntry{ID: model.NewID(model.PrefixCreditLedger), TenantID: actor.TenantID, AmountMicro: 1000, CreatedAt: f.clock.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.BuyAccount(ctx, f.ownerAt, claude.ID, "00000000000000000000000000000011", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BuyAccount(ctx, other, codex.ID, "00000000000000000000000000000012", 200); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		actor        logic.Actor
		mine, closed model.AccountSale
	}{
		{f.ownerAt, claude, codex}, {other, codex, claude},
	} {
		policy, err := f.svc.AccountPolicy(ctx, test.actor.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		if !policy.Allows("agent-one", test.mine.RuntimeID, f.clock.Now()) || policy.Allows("agent-one", test.closed.RuntimeID, f.clock.Now()) {
			t.Fatalf("account isolation: %+v", policy)
		}
		visible, err := f.svc.ListAccountSales(ctx, test.actor)
		if err != nil || len(visible) != 1 || visible[0].ID != test.mine.ID {
			t.Fatalf("inventory: %+v %v", visible, err)
		}
		if _, err := f.svc.BuyAccount(ctx, test.actor, test.closed.ID, "00000000000000000000000000000013", test.closed.PriceMicro); !errors.Is(err, logic.ErrConflict) {
			t.Fatalf("sold account repurchased: %v", err)
		}
	}
}

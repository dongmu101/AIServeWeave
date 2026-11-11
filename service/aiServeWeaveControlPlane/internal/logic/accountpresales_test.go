package logic_test

import (
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// countingInvalidator counts full cache invalidations.
type countingInvalidator struct{ all atomic.Int64 }

func (c *countingInvalidator) Invalidate(context.Context, string) {}
func (c *countingInvalidator) InvalidateAll(context.Context)      { c.all.Add(1) }

func presaleDraft(kind string, capacity int) model.AccountPresale {
	return model.AccountPresale{Kind: kind, Name: "Presale " + kind, PriceMinor: 1500, Currency: "USD", DodoProductID: "pdt_pre", Capacity: capacity, Active: true}
}

func TestPresaleDraftValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc = logic.New(f.store, f.clock, logic.WithAccountSales())
	if _, err := f.svc.SaveAccountPresale(ctx, f.ownerAt, presaleDraft(model.AccountKindClaude, 1), true); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("tenant could create a presale", err)
	}
	for name, mutate := range map[string]func(*model.AccountPresale){
		"other kind":     func(p *model.AccountPresale) { p.Kind = "grok" },
		"no kind":        func(p *model.AccountPresale) { p.Kind = "" },
		"no name":        func(p *model.AccountPresale) { p.Name = " " },
		"zero price":     func(p *model.AccountPresale) { p.PriceMinor = 0 },
		"too expensive":  func(p *model.AccountPresale) { p.PriceMinor = model.MaxAccountPriceMinor + 1 },
		"lower currency": func(p *model.AccountPresale) { p.Currency = "usd" },
		"not a currency": func(p *model.AccountPresale) { p.Currency = "ISO" },
		"country code":   func(p *model.AccountPresale) { p.Currency = "CN" },
		"no product":     func(p *model.AccountPresale) { p.DodoProductID = "" },
		"zero capacity":  func(p *model.AccountPresale) { p.Capacity = 0 },
		"huge capacity":  func(p *model.AccountPresale) { p.Capacity = model.MaxPresaleCapacity + 1 },
	} {
		draft := presaleDraft(model.AccountKindClaude, 1)
		mutate(&draft)
		if _, err := f.svc.SaveAccountPresale(ctx, accountOperator, draft, true); !errors.Is(err, logic.ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	created, err := f.svc.SaveAccountPresale(ctx, accountOperator, presaleDraft(model.AccountKindCodex, 3), true)
	if err != nil || created.Kind != model.AccountKindCodex || created.Capacity != 3 || created.Outstanding != 0 {
		t.Fatal(created, err)
	}
	changed := created.AccountPresale
	changed.Kind = model.AccountKindClaude
	if _, err := f.svc.SaveAccountPresale(ctx, accountOperator, changed, false); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("presale kind changed", err)
	}
	// Without a payment provider the presale is listed but cannot be bought.
	if rows, err := f.svc.ListAccountPresales(ctx, f.ownerAt); err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if _, err := f.svc.CreatePresaleCheckout(ctx, f.ownerAt, created.ID, 1500, "USD"); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("bought without a payment provider", err)
	}
}

func TestPresaleDisabled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.ListAccountPresales(ctx, f.ownerAt); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := f.svc.AssignPresaleOrder(ctx, accountOperator, "ord_x", "acc_x"); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal(err)
	}
}

// Account inventory names its kind, and archived inventory may stay
// unpriced for presale assignment.
func TestAccountSaleKindAndUnpricedInventory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc = logic.New(f.store, f.clock, logic.WithAccountSales())
	unpriced := model.AccountSale{NodeID: "agent", RuntimeID: "claude-b", Kind: model.AccountKindClaude, Name: "B"}
	for name, mutate := range map[string]func(*model.AccountSale){
		"no kind":        func(s *model.AccountSale) { s.Kind = "" },
		"other kind":     func(s *model.AccountSale) { s.Kind = "grok" },
		"listed":         func(s *model.AccountSale) { s.Active = true },
		"partial price":  func(s *model.AccountSale) { s.PriceMinor = 100 },
		"partial things": func(s *model.AccountSale) { s.DodoProductID = "pdt" },
	} {
		draft := unpriced
		mutate(&draft)
		if _, err := f.svc.SaveAccountSale(ctx, accountOperator, draft, true); !errors.Is(err, logic.ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	sale, err := f.svc.SaveAccountSale(ctx, accountOperator, unpriced, true)
	if err != nil || sale.Kind != model.AccountKindClaude || sale.PriceMinor != 0 || sale.Active {
		t.Fatal(sale, err)
	}
	// An update that omits the kind keeps it; one that changes it conflicts.
	sale.Kind, sale.Name = "", "Renamed"
	if got, err := f.svc.SaveAccountSale(ctx, accountOperator, sale, false); err != nil || got.Kind != model.AccountKindClaude || got.Name != "Renamed" {
		t.Fatal("kind lost on update", got, err)
	}
	sale.Kind = model.AccountKindCodex
	if _, err := f.svc.SaveAccountSale(ctx, accountOperator, sale, false); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("kind changed", err)
	}
	// The unpriced account is not offered to tenants.
	if rows, err := f.svc.ListAccountSales(ctx, f.ownerAt); err != nil || len(rows) != 0 {
		t.Fatal("unpriced account listed", rows, err)
	}
}

func TestPresaleCheckoutAndAssignment(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	provider := &accountTestProvider{}
	inv := &countingInvalidator{}
	f.svc = logic.New(f.store, f.clock, logic.WithInvalidator(inv),
		logic.WithBilling(logic.BillingConfig{Provider: provider, ReturnURL: "https://console.example/billing", RefundWindowDays: 30}))
	presale, err := f.svc.SaveAccountPresale(ctx, accountOperator, presaleDraft(model.AccountKindClaude, 1), true)
	if err != nil {
		t.Fatal(err)
	}
	otherTenant, otherOwner, err := f.svc.CreateTenant(ctx, "Second", "presale-second@example.com", testPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	second := logic.Actor{TenantID: otherTenant.ID, UserID: otherOwner.ID, Role: model.RoleOwner}

	if _, err := f.svc.CreatePresaleCheckout(ctx, f.actorWithRole(model.RoleMember), presale.ID, 1500, "USD"); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("member could buy", err)
	}
	if _, err := f.svc.CreatePresaleCheckout(ctx, f.ownerAt, presale.ID, 1499, "USD"); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("stale price", err)
	}
	if _, err := f.svc.CreatePresaleCheckout(ctx, f.ownerAt, "pre_missing", 1500, "USD"); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("missing presale", err)
	}
	checkout, err := f.svc.CreatePresaleCheckout(ctx, f.ownerAt, presale.ID, 1500, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got := provider.checkouts[len(provider.checkouts)-1]; got.ProductID != "pdt_pre" || got.AmountMinor != 1500 || got.OrderID != checkout.OrderID {
		t.Fatalf("checkout request: %+v", got)
	}
	// The pending checkout takes the only place.
	if _, err := f.svc.CreatePresaleCheckout(ctx, second, presale.ID, 1500, "USD"); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("capacity exceeded", err)
	}
	if rows, err := f.svc.ListAccountPresales(ctx, second); err != nil || len(rows) != 1 || logic.PresaleAvailable(rows[0]) {
		t.Fatal("full presale shown available", rows, err)
	}

	// Payment grants nothing yet and leaves the mode alone.
	before := inv.all.Load()
	if result := provider.pay(t, f.svc, f.tenant.ID, checkout.OrderID, 1500); result.Outcome != store.BillingOutcomeApplied || result.AccessChanged {
		t.Fatal(result)
	}
	if inv.all.Load() != before {
		t.Fatal("waiting payment invalidated verifications")
	}
	if overview, err := f.svc.BillingOverview(ctx, f.ownerAt); err != nil || overview.Mode == model.BillingModeAccountMonthly || overview.BalanceMicro != 0 {
		t.Fatalf("presale payment changed billing: %+v %v", overview, err)
	}
	if ledger, err := f.svc.ListBillingLedger(ctx, f.ownerAt, store.ListQuery{Limit: 10}, store.CreditLedgerFilter{}); err != nil || len(ledger.Items) != 0 {
		t.Fatalf("presale payment wrote credits: %+v %v", ledger.Items, err)
	}
	awaiting, err := f.svc.ListBillingOrders(ctx, accountOperator, store.ListQuery{Limit: 10}, store.CheckoutOrderFilter{Awaiting: true})
	if err != nil || len(awaiting.Items) != 1 || awaiting.Items[0].ID != checkout.OrderID {
		t.Fatal("awaiting orders", awaiting.Items, err)
	}

	claude, err := f.svc.SaveAccountSale(ctx, accountOperator, model.AccountSale{NodeID: "agent-pre", RuntimeID: "claude-a", Kind: model.AccountKindClaude, Name: "Claude A"}, true)
	if err != nil {
		t.Fatal(err)
	}
	codex, err := f.svc.SaveAccountSale(ctx, accountOperator, model.AccountSale{NodeID: "agent-pre", RuntimeID: "codex-a", Kind: model.AccountKindCodex, Name: "Codex A"}, true)
	if err != nil {
		t.Fatal(err)
	}
	dedicated, err := f.svc.SaveAccountSale(ctx, accountOperator, model.AccountSale{NodeID: "agent-other", RuntimeID: "claude-d", Kind: model.AccountKindClaude, Name: "Claude D"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetNodeAccess(ctx, accountOperator, "agent-other", []string{otherTenant.ID}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		actor   logic.Actor
		order   string
		account string
		want    error
	}{
		{"tenant", f.ownerAt, checkout.OrderID, claude.ID, logic.ErrForbidden},
		{"empty account", accountOperator, checkout.OrderID, "", logic.ErrInvalidInput},
		{"missing order", accountOperator, "ord_missing", claude.ID, logic.ErrNotFound},
		{"missing account", accountOperator, checkout.OrderID, "acc_missing", logic.ErrNotFound},
		{"wrong kind", accountOperator, checkout.OrderID, codex.ID, logic.ErrConflict},
		{"dedicated node", accountOperator, checkout.OrderID, dedicated.ID, logic.ErrConflict},
	} {
		if _, err := f.svc.AssignPresaleOrder(ctx, test.actor, test.order, test.account); !errors.Is(err, test.want) {
			t.Errorf("%s: %v", test.name, err)
		}
	}

	before = inv.all.Load()
	assigned, err := f.svc.AssignPresaleOrder(ctx, accountOperator, checkout.OrderID, claude.ID)
	if err != nil {
		t.Fatal(err)
	}
	if assigned.AccountID != claude.ID || assigned.AccountExpiresAt == nil || !assigned.AccountExpiresAt.Equal(accountaccess.NextMonth(f.clock.Now().UTC())) {
		t.Fatal("assigned order", assigned)
	}
	if inv.all.Load() == before {
		t.Fatal("assignment did not invalidate verifications")
	}
	policy, err := f.svc.AccountPolicy(ctx, f.tenant.ID)
	if err != nil || !policy.Only || !policy.Allows("agent-pre", "claude-a", f.clock.Now()) || policy.Allows("agent-pre", "codex-a", f.clock.Now()) {
		t.Fatal("assignment did not grant the account", policy, err)
	}
	if owned, err := f.svc.ListAccountSales(ctx, f.ownerAt); err != nil || len(owned) != 1 || owned[0].ID != claude.ID {
		t.Fatal("assigned account not shown to its tenant", owned, err)
	}
	if _, err := f.svc.AssignPresaleOrder(ctx, accountOperator, checkout.OrderID, claude.ID); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("assigned twice", err)
	}
	page, err := f.store.ListAudit(ctx, model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range page.Items {
		found = found || (entry.Action == model.ActionPresaleAssign && entry.Target == checkout.OrderID)
	}
	if !found {
		t.Fatal("assignment not audited")
	}

	// The place is free again; an order that cannot be delivered is refunded
	// by an operator, which blocks its assignment.
	refundable, err := f.svc.CreatePresaleCheckout(ctx, second, presale.ID, 1500, "USD")
	if err != nil {
		t.Fatal("assignment kept the place", err)
	}
	provider.pay(t, f.svc, otherTenant.ID, refundable.OrderID, 1500)
	if _, err := f.svc.RequestRefund(ctx, second, refundable.OrderID, 0, "too slow"); err == nil {
		t.Fatal("tenant refunded a presale")
	}
	refund, err := f.svc.CreateOperatorRefund(ctx, accountOperator, refundable.OrderID, 0, "cannot deliver")
	if err != nil || refund.AmountMinor != 1500 || provider.refunds[len(provider.refunds)-1].ProductID != "pdt_pre" {
		t.Fatalf("operator refund: %+v %v", refund, err)
	}
	if _, err := f.svc.AssignPresaleOrder(ctx, accountOperator, refundable.OrderID, dedicated.ID); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("refunding order assigned", err)
	}
}

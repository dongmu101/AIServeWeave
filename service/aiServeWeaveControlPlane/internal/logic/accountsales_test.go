package logic_test

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// accountTestProvider records checkouts and replays the next webhook event.
type accountTestProvider struct {
	billing.Provider
	checkouts []billing.CheckoutRequest
	refunds   []billing.RefundRequest
	failNext  bool
	webhooks  int
	event     store.BillingEvent
}

func (p *accountTestProvider) CreateCustomer(context.Context, string, string, string) (string, error) {
	return "cus_test", nil
}

func (p *accountTestProvider) CreateCheckout(_ context.Context, req billing.CheckoutRequest) (billing.Checkout, error) {
	if p.failNext {
		p.failNext = false
		return billing.Checkout{}, errors.New("provider down")
	}
	p.checkouts = append(p.checkouts, req)
	return billing.Checkout{SessionID: "cks_" + req.OrderID, URL: "https://checkout.example/" + req.OrderID}, nil
}

func (p *accountTestProvider) CreateRefund(_ context.Context, req billing.RefundRequest) (billing.Refund, error) {
	p.refunds = append(p.refunds, req)
	return billing.Refund{ID: "rfd_" + req.RequestID, Status: "pending"}, nil
}

func (p *accountTestProvider) ParseWebhook([]byte, http.Header) (store.BillingEvent, error) {
	return p.event, nil
}

// pay delivers a verified payment, under a new webhook id, for orderID.
func (p *accountTestProvider) pay(t *testing.T, svc *logic.Service, tenantID, orderID string, amount int64) store.BillingEventResult {
	t.Helper()
	p.webhooks++
	p.event = store.BillingEvent{
		WebhookID: fmt.Sprintf("wh_%d", p.webhooks), Type: "payment.succeeded", Kind: store.BillingEventPaymentSucceeded,
		OrderID: orderID, TenantID: tenantID, PaymentID: "pay_" + orderID, CheckoutSessionID: "cks_" + orderID,
		AmountMinor: amount, Currency: "USD",
	}
	result, err := svc.HandleBillingWebhook(context.Background(), nil, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

var accountOperator = logic.Actor{TenantID: model.PlatformScope, Role: model.RolePlatformOperator, UserID: "operator"}

func accountDraft(runtime string, price int64) model.AccountSale {
	return model.AccountSale{NodeID: "agent", RuntimeID: runtime, Kind: model.AccountKindClaude, Name: runtime, PriceMinor: price, Currency: "USD", DodoProductID: "pdt_pwyw", Active: true}
}

func TestMonthlyAccountSaleRequiresFiatPrice(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc = logic.New(f.store, f.clock, logic.WithAccountSales())
	if _, err := f.svc.SaveAccountSale(ctx, f.ownerAt, accountDraft("claude-a", 2000), true); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("tenant could sell accounts", err)
	}
	for name, mutate := range map[string]func(*model.AccountSale){
		"zero price":     func(s *model.AccountSale) { s.PriceMinor = 0 },
		"too expensive":  func(s *model.AccountSale) { s.PriceMinor = model.MaxAccountPriceMinor + 1 },
		"lower currency": func(s *model.AccountSale) { s.Currency = "usd" },
		"not a currency": func(s *model.AccountSale) { s.Currency = "ISO" },
		"no currency":    func(s *model.AccountSale) { s.Currency = "" },
		"no product":     func(s *model.AccountSale) { s.DodoProductID = " " },
	} {
		draft := accountDraft("claude-a", 2000)
		mutate(&draft)
		if _, err := f.svc.SaveAccountSale(ctx, accountOperator, draft, true); !errors.Is(err, logic.ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	sale, err := f.svc.SaveAccountSale(ctx, accountOperator, accountDraft("claude-a", 2000), true)
	if err != nil || sale.PriceMinor != 2000 || sale.Currency != "USD" || sale.DodoProductID != "pdt_pwyw" {
		t.Fatal(sale, err)
	}
	// Without a payment provider the inventory is managed but cannot be bought.
	if _, err := f.svc.CreateAccountCheckout(ctx, f.ownerAt, sale.ID, 2000, "USD", logic.Payment{}); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("bought without a payment provider", err)
	}
}

func TestMonthlyAccountFiatCheckoutLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	provider := &accountTestProvider{}
	f.svc = logic.New(f.store, f.clock, logic.WithBilling(logic.BillingConfig{Provider: provider, ReturnURL: "https://console.example/billing", RefundWindowDays: 30}))
	sale, err := f.svc.SaveAccountSale(ctx, accountOperator, accountDraft("claude-a", 2000), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateAccountCheckout(ctx, f.actorWithRole(model.RoleMember), sale.ID, 2000, "USD", logic.Payment{}); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("member could buy", err)
	}
	for _, test := range []struct {
		amount   int64
		currency string
		want     error
	}{{0, "USD", logic.ErrInvalidInput}, {2000, "usd", logic.ErrInvalidInput}, {1999, "USD", logic.ErrConflict}, {2000, "EUR", logic.ErrConflict}} {
		if _, err := f.svc.CreateAccountCheckout(ctx, f.ownerAt, sale.ID, test.amount, test.currency, logic.Payment{}); !errors.Is(err, test.want) {
			t.Errorf("%d %s: %v", test.amount, test.currency, err)
		}
	}
	checkout, err := f.svc.CreateAccountCheckout(ctx, f.ownerAt, sale.ID, 2000, "USD", logic.Payment{})
	if err != nil {
		t.Fatal(err)
	}
	if got := provider.checkouts[len(provider.checkouts)-1]; got.ProductID != "pdt_pwyw" || got.AmountMinor != 2000 || got.Currency != "USD" || got.OrderID != checkout.OrderID {
		t.Fatalf("checkout request: %+v", got)
	}
	otherTenant, otherOwner, err := f.svc.CreateTenant(ctx, "Second", "second@example.com", testPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	second := logic.Actor{TenantID: otherTenant.ID, UserID: otherOwner.ID, Role: model.RoleOwner}
	if _, err := f.svc.CreateAccountCheckout(ctx, second, sale.ID, 2000, "USD", logic.Payment{}); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("a pending checkout did not hold the account", err)
	}
	// Opening a checkout grants nothing.
	if policy, err := f.svc.AccountPolicy(ctx, f.tenant.ID); err != nil || policy.Allows("agent", "claude-a", f.clock.Now()) {
		t.Fatal("checkout granted access before payment", err)
	}
	// An underpaid payment does not grant the month.
	if result := provider.pay(t, f.svc, f.tenant.ID, checkout.OrderID, 1999); result.Outcome != store.BillingOutcomeMismatch {
		t.Fatal(result)
	}
	if result := provider.pay(t, f.svc, f.tenant.ID, checkout.OrderID, 2000); result.Outcome != store.BillingOutcomeApplied || !result.AccessChanged {
		t.Fatal(result)
	}
	policy, err := f.svc.AccountPolicy(ctx, f.tenant.ID)
	if err != nil || !policy.Only || !policy.Allows("agent", "claude-a", f.clock.Now()) {
		t.Fatal("paid month not granted", policy, err)
	}
	overview, err := f.svc.BillingOverview(ctx, f.ownerAt)
	if err != nil || overview.Mode != model.BillingModeAccountMonthly || overview.BalanceMicro != 0 {
		t.Fatalf("payment moved credits or kept mode: %+v %v", overview, err)
	}
	ledger, err := f.svc.ListBillingLedger(ctx, f.ownerAt, store.ListQuery{Limit: 10}, store.CreditLedgerFilter{})
	if err != nil || len(ledger.Items) != 0 {
		t.Fatalf("fiat purchase wrote credits: %+v %v", ledger.Items, err)
	}
	orders, err := f.svc.ListBillingOrders(ctx, f.ownerAt, store.ListQuery{Limit: 10}, store.CheckoutOrderFilter{})
	if err != nil || len(orders.Items) != 1 || orders.Items[0].Status != model.OrderStatusPaid || orders.Items[0].AccountExpiresAt == nil {
		t.Fatalf("paid order: %+v %v", orders.Items, err)
	}
	firstExpiry := *orders.Items[0].AccountExpiresAt
	// A replayed payment grants nothing more.
	if result := provider.pay(t, f.svc, f.tenant.ID, checkout.OrderID, 2000); result.Outcome != store.BillingOutcomeDuplicate {
		t.Fatal(result)
	}
	if visible, err := f.svc.ListAccountSales(ctx, second); err != nil || len(visible) != 0 {
		t.Fatalf("sold account offered to another tenant: %+v %v", visible, err)
	}
	if _, err := f.svc.CreateAccountCheckout(ctx, second, sale.ID, 2000, "USD", logic.Payment{}); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("sold account checked out by another tenant", err)
	}
	// A renewal extends from the current expiry.
	renewal, err := f.svc.CreateAccountCheckout(ctx, f.ownerAt, sale.ID, 2000, "USD", logic.Payment{})
	if err != nil {
		t.Fatal(err)
	}
	provider.pay(t, f.svc, f.tenant.ID, renewal.OrderID, 2000)
	rows, err := f.svc.ListAccountSales(ctx, f.ownerAt)
	if err != nil || len(rows) != 1 || !rows[0].ExpiresAt.After(firstExpiry) {
		t.Fatalf("renewal did not extend: %+v %v", rows, err)
	}
	// Tenants cannot self-refund a month; an operator can, without credits.
	if _, err := f.svc.RequestRefund(ctx, f.ownerAt, checkout.OrderID, 0, "changed my mind"); err == nil {
		t.Fatal("tenant refunded an account month")
	}
	refund, err := f.svc.CreateOperatorRefund(ctx, accountOperator, checkout.OrderID, 0, "support case")
	if err != nil || refund.AmountMinor != 2000 || refund.CreditsMicro != 0 || len(provider.refunds) != 1 || provider.refunds[0].ProductID != "pdt_pwyw" {
		t.Fatalf("operator refund: %+v %v %+v", refund, err, provider.refunds)
	}
	// A checkout the provider never opened releases its hold.
	f.clock.Advance(rows[0].ExpiresAt.Sub(f.clock.Now()))
	provider.failNext = true
	if _, err := f.svc.CreateAccountCheckout(ctx, second, sale.ID, 2000, "USD", logic.Payment{}); !errors.Is(err, logic.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateAccountCheckout(ctx, f.ownerAt, sale.ID, 2000, "USD", logic.Payment{}); err != nil {
		t.Fatal("failed checkout kept holding the account", err)
	}
	// A node binding still narrows account inventory and checkouts.
	if _, err := f.svc.SetNodeAccess(ctx, accountOperator, "agent", []string{f.tenant.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateAccountCheckout(ctx, second, sale.ID, 2000, "USD", logic.Payment{}); !errors.Is(err, logic.ErrNotFound) {
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
	provider := &accountTestProvider{}
	f.svc = logic.New(f.store, f.clock, logic.WithAccountSales(), logic.WithBilling(logic.BillingConfig{Provider: provider}))
	otherTenant, otherOwner, err := f.svc.CreateTenant(ctx, "Codex buyer", "codex-buyer@example.com", testPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	other := logic.Actor{TenantID: otherTenant.ID, UserID: otherOwner.ID, Role: model.RoleOwner}
	claude, err := f.svc.SaveAccountSale(ctx, accountOperator, model.AccountSale{NodeID: "agent-one", RuntimeID: "claude-a", Kind: model.AccountKindClaude, Name: "Claude Code A", PriceMinor: 100, Currency: "USD", DodoProductID: "pdt", Active: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	codex, err := f.svc.SaveAccountSale(ctx, accountOperator, model.AccountSale{NodeID: "agent-one", RuntimeID: "codex-a", Kind: model.AccountKindCodex, Name: "Codex A", PriceMinor: 200, Currency: "USD", DodoProductID: "pdt", Active: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, buy := range []struct {
		actor logic.Actor
		sale  model.AccountSale
	}{{f.ownerAt, claude}, {other, codex}} {
		checkout, err := f.svc.CreateAccountCheckout(ctx, buy.actor, buy.sale.ID, buy.sale.PriceMinor, "USD", logic.Payment{})
		if err != nil {
			t.Fatal(err)
		}
		provider.pay(t, f.svc, buy.actor.TenantID, checkout.OrderID, buy.sale.PriceMinor)
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
		if _, err := f.svc.CreateAccountCheckout(ctx, test.actor, test.closed.ID, test.closed.PriceMinor, "USD", logic.Payment{}); !errors.Is(err, logic.ErrConflict) {
			t.Fatalf("sold account repurchased: %v", err)
		}
	}
}

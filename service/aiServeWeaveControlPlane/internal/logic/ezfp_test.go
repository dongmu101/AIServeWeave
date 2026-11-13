package logic_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// fakeEzfp records what the logic asks 易支付 for and answers as each test
// sets it up.
type fakeEzfp struct {
	mu          sync.Mutex
	orders      []billing.EzfpOrderRequest
	createErr   error
	queries     int
	query       func(outTradeNo string) (billing.EzfpPayment, error)
	notify      billing.EzfpPayment
	notifyErr   error
	refunds     []billing.EzfpRefundRequest
	refund      func(billing.EzfpRefundRequest) (billing.EzfpRefund, error)
	refundQuery func(outRefundNo string) (billing.EzfpRefund, error)
}

func (f *fakeEzfp) CreateOrder(_ context.Context, req billing.EzfpOrderRequest) (billing.EzfpOrder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return billing.EzfpOrder{}, f.createErr
	}
	f.orders = append(f.orders, req)
	return billing.EzfpOrder{TradeNo: "T" + req.OutTradeNo, PayURL: "https://pay.example/" + req.OutTradeNo}, nil
}

func (f *fakeEzfp) QueryOrder(_ context.Context, outTradeNo string) (billing.EzfpPayment, error) {
	f.mu.Lock()
	f.queries++
	answer := f.query
	f.mu.Unlock()
	if answer == nil {
		return billing.EzfpPayment{OutTradeNo: outTradeNo, TradeNo: "T" + outTradeNo, Status: billing.EzfpStatusUnpaid}, nil
	}
	return answer(outTradeNo)
}

func (f *fakeEzfp) ParseNotify(url.Values) (billing.EzfpPayment, error) {
	return f.notify, f.notifyErr
}

func (f *fakeEzfp) Refund(_ context.Context, req billing.EzfpRefundRequest) (billing.EzfpRefund, error) {
	f.mu.Lock()
	f.refunds = append(f.refunds, req)
	answer := f.refund
	f.mu.Unlock()
	if answer == nil {
		return billing.EzfpRefund{RefundNo: "R" + req.OutRefundNo, OutRefundNo: req.OutRefundNo, TradeNo: req.TradeNo,
			MoneyMinor: req.MoneyMinor, Status: billing.EzfpRefundSucceeded}, nil
	}
	return answer(req)
}

func (f *fakeEzfp) QueryRefund(_ context.Context, outRefundNo string) (billing.EzfpRefund, error) {
	if f.refundQuery == nil {
		return billing.EzfpRefund{}, &billing.EzfpError{Op: "refundquery", Code: -1}
	}
	return f.refundQuery(outRefundNo)
}

func (f *fakeEzfp) paid(order string, tenant string, amount int64) billing.EzfpPayment {
	return billing.EzfpPayment{
		PID: "1001", TradeNo: "T" + order, OutTradeNo: order, Type: "wxpay", TradeStatus: billing.EzfpTradeSuccess,
		Status: billing.EzfpStatusPaid, MoneyMinor: amount, Param: tenant, Timestamp: time.Now().Unix(),
	}
}

// deliver hands the logic a verified notification for payment.
func (f *fakeEzfp) deliver(t *testing.T, svc *logic.Service, payment billing.EzfpPayment) store.BillingEventResult {
	t.Helper()
	f.notify, f.notifyErr = payment, nil
	result, err := svc.HandleEzfpNotify(context.Background(), url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func ezfpConfig(client billing.Ezfp) *logic.EzfpConfig {
	return &logic.EzfpConfig{
		Client: client, Types: []string{model.EzfpPayTypeAlipay, model.EzfpPayTypeWxpay},
		NotifyURL: "https://cp.example/billing/v1/ezfp/notify", ReturnURL: "https://console.example/console/billing",
	}
}

var (
	wxpay = logic.Payment{Provider: model.PaymentProviderEzfp, PayType: model.EzfpPayTypeWxpay}
	usdt  = logic.Payment{Provider: model.PaymentProviderEzfp, PayType: model.EzfpPayTypeUsdt}
)

func cnyPackage(t *testing.T, svc *logic.Service, product string) model.CreditPackage {
	t.Helper()
	pkg, err := svc.CreateCreditPackage(context.Background(), accountOperator, logic.CreditPackageInput{
		Name: "人民币充值包", Kind: model.PackageKindFixed, DodoProductID: product, CreditsMicro: 10 * model.MicroPerCredit,
		PriceMinor: 6800, Currency: "CNY", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func balanceOf(t *testing.T, svc *logic.Service, actor logic.Actor) int64 {
	t.Helper()
	overview, err := svc.BillingOverview(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	return overview.BalanceMicro
}

func TestEzfpUsdtCheckoutWhenOffered(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ezfp := &fakeEzfp{}
	cfg := ezfpConfig(ezfp)
	cfg.Types = []string{model.EzfpPayTypeAlipay, model.EzfpPayTypeUsdt}
	f.svc = logic.New(f.store, f.clock, logic.WithBilling(logic.BillingConfig{Ezfp: cfg, RefundWindowDays: 30}))
	if err := f.svc.SetTenantBillingMode(ctx, accountOperator, f.tenant.ID, model.BillingModePrepaid); err != nil {
		t.Fatal(err)
	}
	pkg := cnyPackage(t, f.svc, "")
	if _, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, wxpay); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatalf("wxpay not offered: %v", err)
	}
	checkout, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, usdt)
	if err != nil {
		t.Fatal(err)
	}
	if sent := ezfp.orders[len(ezfp.orders)-1]; sent.OutTradeNo != checkout.OrderID || sent.PayType != "usdt" || sent.MoneyMinor != 6800 {
		t.Fatalf("create request = %+v", sent)
	}
	order, err := f.store.GetCheckoutOrder(ctx, checkout.OrderID)
	if err != nil || order.PayType != model.EzfpPayTypeUsdt {
		t.Fatalf("order = %+v, %v", order, err)
	}
}

func TestEzfpOnlyPackageCheckoutAndNotify(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ezfp := &fakeEzfp{}
	inv := &countingInvalidator{}
	f.svc = logic.New(f.store, f.clock, logic.WithInvalidator(inv),
		logic.WithBilling(logic.BillingConfig{Ezfp: ezfpConfig(ezfp), RefundWindowDays: 30}))
	if err := f.svc.SetTenantBillingMode(ctx, accountOperator, f.tenant.ID, model.BillingModePrepaid); err != nil {
		t.Fatal(err)
	}
	// A CNY package needs no Dodo product; any other currency still does.
	if _, err := f.svc.CreateCreditPackage(ctx, accountOperator, logic.CreditPackageInput{
		Name: "USD", Kind: model.PackageKindFixed, CreditsMicro: 1, PriceMinor: 100, Currency: "USD", Active: true,
	}); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("a USD package without a product was saved", err)
	}
	pkg := cnyPackage(t, f.svc, "")
	usd, err := f.svc.CreateCreditPackage(ctx, accountOperator, logic.CreditPackageInput{
		Name: "USD", Kind: model.PackageKindFixed, DodoProductID: "pdt_usd", CreditsMicro: 1, PriceMinor: 100, Currency: "USD", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	overview, err := f.svc.BillingOverview(ctx, f.ownerAt)
	if err != nil || overview.Dodo || !overview.Ezfp || len(overview.EzfpTypes) != 2 || len(overview.Packages) != 1 || overview.Packages[0].ID != pkg.ID {
		t.Fatalf("overview = %+v, %v", overview, err)
	}
	for name, test := range map[string]struct {
		actor logic.Actor
		pkg   string
		pay   logic.Payment
		want  error
	}{
		"dodo is off":      {f.ownerAt, pkg.ID, logic.Payment{}, logic.ErrNotFound},
		"unknown type":     {f.ownerAt, pkg.ID, logic.Payment{Provider: "ezfp", PayType: "qqpay"}, logic.ErrInvalidInput},
		"usdt not offered": {f.ownerAt, pkg.ID, usdt, logic.ErrInvalidInput},
		"type on dodo":     {f.ownerAt, pkg.ID, logic.Payment{Provider: "dodo", PayType: "wxpay"}, logic.ErrInvalidInput},
		"unknown provider": {f.ownerAt, pkg.ID, logic.Payment{Provider: "stripe"}, logic.ErrInvalidInput},
		"member":           {f.actorWithRole(model.RoleMember), pkg.ID, wxpay, logic.ErrForbidden},
		"not CNY":          {f.ownerAt, usd.ID, wxpay, logic.ErrNotFound},
	} {
		if _, err := f.svc.CreateCheckout(ctx, test.actor, test.pkg, 0, test.pay); !errors.Is(err, test.want) {
			t.Errorf("%s: %v; want %v", name, err, test.want)
		}
	}

	checkout, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, wxpay)
	if err != nil || checkout.CheckoutURL != "https://pay.example/"+checkout.OrderID {
		t.Fatalf("checkout = %+v, %v", checkout, err)
	}
	sent := ezfp.orders[len(ezfp.orders)-1]
	if sent.OutTradeNo != checkout.OrderID || sent.PayType != "wxpay" || sent.MoneyMinor != 6800 || sent.Param != f.tenant.ID ||
		sent.ClientIP != "10.0.0.1" || sent.Name != "人民币充值包" || sent.NotifyURL != "https://cp.example/billing/v1/ezfp/notify" ||
		sent.ReturnURL != "https://console.example/console/billing" {
		t.Fatalf("create request = %+v", sent)
	}
	order, err := f.store.GetCheckoutOrder(ctx, checkout.OrderID)
	if err != nil || order.Provider != model.PaymentProviderEzfp || order.PayType != "wxpay" || order.DodoSessionID != "ezfp:T"+order.ID ||
		order.Status != model.OrderStatusPending || order.CreditsMicro != 10*model.MicroPerCredit {
		t.Fatalf("order = %+v, %v", order, err)
	}

	// A bad signature is refused, and nothing moves.
	ezfp.notifyErr = billing.ErrInvalidSignature
	if _, err := f.svc.HandleEzfpNotify(ctx, url.Values{}); !errors.Is(err, logic.ErrInvalidSignature) {
		t.Fatal("a bad signature was accepted", err)
	}
	// Underpaid: not credited.
	if r := ezfp.deliver(t, f.svc, ezfp.paid(order.ID, f.tenant.ID, 6799)); r.Outcome != store.BillingOutcomeMismatch {
		t.Fatalf("underpaid outcome = %s", r.Outcome)
	}
	if got := balanceOf(t, f.svc, f.ownerAt); got != 0 {
		t.Fatalf("an underpaid trade credited %d", got)
	}

	second, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, logic.Payment{Provider: "ezfp", PayType: "alipay"})
	if err != nil {
		t.Fatal(err)
	}
	// Another tenant's id in param does not settle it.
	if r := ezfp.deliver(t, f.svc, ezfp.paid(second.OrderID, "tnt_other", 6800)); r.Outcome != store.BillingOutcomeMismatch {
		t.Fatalf("foreign param outcome = %s", r.Outcome)
	}
	before := inv.all.Load()
	third, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, wxpay)
	if err != nil {
		t.Fatal(err)
	}
	if r := ezfp.deliver(t, f.svc, ezfp.paid(third.OrderID, f.tenant.ID, 6800)); r.Outcome != store.BillingOutcomeApplied {
		t.Fatalf("paid outcome = %s", r.Outcome)
	}
	if got := balanceOf(t, f.svc, f.ownerAt); got != 10*model.MicroPerCredit {
		t.Fatalf("balance = %d", got)
	}
	if inv.all.Load() == before {
		t.Fatal("a balance leaving zero did not invalidate cached verifications")
	}
	// The same trade again, by notification or query, credits nothing more.
	if r := ezfp.deliver(t, f.svc, ezfp.paid(third.OrderID, f.tenant.ID, 6800)); r.Outcome != store.BillingOutcomeDuplicate {
		t.Fatalf("replay outcome = %s", r.Outcome)
	}
	if got := balanceOf(t, f.svc, f.ownerAt); got != 10*model.MicroPerCredit {
		t.Fatalf("a replay changed the balance to %d", got)
	}
	ledger, err := f.svc.ListBillingLedger(ctx, f.ownerAt, store.ListQuery{}, store.CreditLedgerFilter{Kind: model.LedgerKindTopup})
	if err != nil || len(ledger.Items) != 1 || ledger.Items[0].IdempotencyKey == nil || *ledger.Items[0].IdempotencyKey != "topup:ezfp:T"+third.OrderID {
		t.Fatalf("ledger = %+v, %v", ledger.Items, err)
	}
	// A trade naming no known order is acknowledged and changes nothing.
	if r := ezfp.deliver(t, f.svc, ezfp.paid("ord_unknown", f.tenant.ID, 6800)); r.Outcome != store.BillingOutcomeNoOrder {
		t.Fatalf("unknown order outcome = %s", r.Outcome)
	}
	// A notification for a trade that has not succeeded is ignored.
	waiting := ezfp.paid(second.OrderID, f.tenant.ID, 6800)
	waiting.Status, waiting.TradeStatus = billing.EzfpStatusUnpaid, ""
	if r := ezfp.deliver(t, f.svc, waiting); r.Outcome != store.BillingOutcomeIgnored {
		t.Fatalf("unsucceeded outcome = %s", r.Outcome)
	}
}

func TestEzfpCheckoutFailureFailsTheOrder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ezfp := &fakeEzfp{createErr: &billing.EzfpError{Op: "create", Code: -1}}
	f.svc = logic.New(f.store, f.clock, logic.WithBilling(logic.BillingConfig{Ezfp: ezfpConfig(ezfp)}))
	pkg := cnyPackage(t, f.svc, "")
	if _, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, wxpay); !errors.Is(err, logic.ErrUnavailable) {
		t.Fatal(err)
	}
	page, err := f.svc.ListBillingOrders(ctx, f.ownerAt, store.ListQuery{}, store.CheckoutOrderFilter{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != model.OrderStatusFailed {
		t.Fatalf("orders = %+v, %v", page.Items, err)
	}
}

func TestEzfpTypesAreLimitedToTheConfiguredOnes(t *testing.T) {
	f := newFixture(t)
	ezfp := &fakeEzfp{}
	cfg := ezfpConfig(ezfp)
	cfg.Types = []string{model.EzfpPayTypeWxpay}
	f.svc = logic.New(f.store, f.clock, logic.WithBilling(logic.BillingConfig{Ezfp: cfg}))
	pkg := cnyPackage(t, f.svc, "")
	if _, err := f.svc.CreateCheckout(context.Background(), f.ownerAt, pkg.ID, 0, logic.Payment{Provider: "ezfp", PayType: "alipay"}); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("an unoffered type was accepted", err)
	}
}

func TestEzfpRefreshAndRecovery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ezfp := &fakeEzfp{}
	dodo := &accountTestProvider{}
	f.svc = logic.New(f.store, f.clock, logic.WithBilling(logic.BillingConfig{
		Provider: dodo, ReturnURL: "https://console.example/console/billing", Ezfp: ezfpConfig(ezfp),
	}))
	pkg := cnyPackage(t, f.svc, "pdt_cny")
	checkout, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, wxpay)
	if err != nil {
		t.Fatal(err)
	}
	dodoCheckout, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, logic.Payment{})
	if err != nil {
		t.Fatal(err)
	}
	if dodoOrder, _ := f.store.GetCheckoutOrder(ctx, dodoCheckout.OrderID); dodoOrder.Provider != model.PaymentProviderDodo {
		t.Fatalf("a Dodo order records provider %q", dodoOrder.Provider)
	}
	other, otherOwner, err := f.svc.CreateTenant(ctx, "Other", "other@example.com", testPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	stranger := logic.Actor{TenantID: other.ID, UserID: otherOwner.ID, Role: model.RoleOwner}
	for name, test := range map[string]struct {
		actor logic.Actor
		order string
	}{"another tenant": {stranger, checkout.OrderID}, "a Dodo order": {f.ownerAt, dodoCheckout.OrderID}, "unknown": {f.ownerAt, "ord_x"}} {
		if _, err := f.svc.RefreshEzfpOrder(ctx, test.actor, test.order); !errors.Is(err, logic.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Unpaid: still pending. A second call within the gap does not query.
	order, err := f.svc.RefreshEzfpOrder(ctx, f.actorWithRole(model.RoleMember), checkout.OrderID)
	if err != nil || order.Status != model.OrderStatusPending || ezfp.queries != 1 {
		t.Fatalf("order = %+v, %v, queries %d", order, err, ezfp.queries)
	}
	if _, err := f.svc.RefreshEzfpOrder(ctx, f.ownerAt, checkout.OrderID); err != nil || ezfp.queries != 1 {
		t.Fatalf("a refresh inside the gap queried again: %v, %d", err, ezfp.queries)
	}
	f.clock.Advance(6 * time.Second)
	ezfp.query = func(id string) (billing.EzfpPayment, error) {
		p := ezfp.paid(id, f.tenant.ID, 6800)
		p.TradeStatus = ""
		return p, nil
	}
	order, err = f.svc.RefreshEzfpOrder(ctx, f.ownerAt, checkout.OrderID)
	if err != nil || order.Status != model.OrderStatusPaid || order.DodoPaymentID == nil || *order.DodoPaymentID != "ezfp:T"+checkout.OrderID {
		t.Fatalf("refreshed order = %+v, %v", order, err)
	}
	// A Dodo webhook naming the 易支付 order cannot settle it, even with a
	// matching amount.
	third, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, wxpay)
	if err != nil {
		t.Fatal(err)
	}
	if r := dodo.pay(t, f.svc, f.tenant.ID, third.OrderID, 6800); r.Outcome != store.BillingOutcomeMismatch {
		t.Fatalf("a Dodo event settled an 易支付 order: %s", r.Outcome)
	}

	// Recovery: too young first, then settled once old enough.
	ezfp.query = nil
	fourth, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, wxpay)
	if err != nil {
		t.Fatal(err)
	}
	queries := ezfp.queries
	ezfp.query = func(id string) (billing.EzfpPayment, error) {
		p := ezfp.paid(id, f.tenant.ID, 6800)
		return p, nil
	}
	f.svc.RecoverEzfp(ctx)
	if got, _ := f.store.GetCheckoutOrder(ctx, fourth.OrderID); got.Status != model.OrderStatusPending {
		t.Fatal("recovery settled an order younger than its minimum age")
	}
	f.clock.Advance(31 * time.Second)
	f.svc.RecoverEzfp(ctx)
	if got, _ := f.store.GetCheckoutOrder(ctx, fourth.OrderID); got.Status != model.OrderStatusPaid {
		t.Fatalf("recovery left order %s", got.Status)
	}
	if ezfp.queries == queries {
		t.Fatal("recovery made no query")
	}
	// Past the window it stops asking.
	fifth, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, wxpay)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(3 * time.Hour)
	f.svc.RecoverEzfp(ctx)
	if got, _ := f.store.GetCheckoutOrder(ctx, fifth.OrderID); got.Status != model.OrderStatusPending {
		t.Fatal("recovery queried an order past its window")
	}
}

func TestEzfpRefunds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ezfp := &fakeEzfp{}
	f.svc = logic.New(f.store, f.clock, logic.WithBilling(logic.BillingConfig{Ezfp: ezfpConfig(ezfp), RefundWindowDays: 30}))
	if err := f.svc.SetTenantBillingMode(ctx, accountOperator, f.tenant.ID, model.BillingModePrepaid); err != nil {
		t.Fatal(err)
	}
	pkg := cnyPackage(t, f.svc, "")
	buy := func() string {
		t.Helper()
		checkout, err := f.svc.CreateCheckout(ctx, f.ownerAt, pkg.ID, 0, wxpay)
		if err != nil {
			t.Fatal(err)
		}
		if r := ezfp.deliver(t, f.svc, ezfp.paid(checkout.OrderID, f.tenant.ID, 6800)); r.Outcome != store.BillingOutcomeApplied {
			t.Fatalf("payment outcome = %s", r.Outcome)
		}
		return checkout.OrderID
	}

	// Answered at once: settled in the same call.
	first := buy()
	req, err := f.svc.CreateOperatorRefund(ctx, accountOperator, first, 0, "duplicate payment")
	if err != nil || req.Status != model.RefundStatusSucceeded || req.DodoRefundID == nil || *req.DodoRefundID != "ezfp:R"+req.ID {
		t.Fatalf("refund = %+v, %v", req, err)
	}
	if sent := ezfp.refunds[len(ezfp.refunds)-1]; sent.OutRefundNo != req.ID || sent.TradeNo != "T"+first || sent.MoneyMinor != 6800 {
		t.Fatalf("refund request = %+v", sent)
	}
	if got, _ := f.store.GetCheckoutOrder(ctx, first); got.Status != model.OrderStatusRefunded || got.RefundedMinor != 6800 {
		t.Fatalf("order after refund = %+v", got)
	}
	if got := balanceOf(t, f.svc, f.ownerAt); got != 0 {
		t.Fatalf("balance after refund = %d", got)
	}

	// Refused: failed, and the held credits come back.
	second := buy()
	ezfp.refund = func(billing.EzfpRefundRequest) (billing.EzfpRefund, error) {
		return billing.EzfpRefund{}, fmt.Errorf("%w: %w", billing.ErrRefundRejected, &billing.EzfpError{Op: "refund", Code: 1})
	}
	req, err = f.svc.CreateOperatorRefund(ctx, accountOperator, second, 0, "test")
	if err != nil || req.Status != model.RefundStatusFailed {
		t.Fatalf("refused refund = %+v, %v", req, err)
	}
	if got := balanceOf(t, f.svc, f.ownerAt); got != 10*model.MicroPerCredit {
		t.Fatalf("balance after a refused refund = %d", got)
	}

	// Lost answer: processing until recovery's query settles it.
	ezfp.refund = func(billing.EzfpRefundRequest) (billing.EzfpRefund, error) {
		return billing.EzfpRefund{}, errors.New("timeout")
	}
	lost, err := f.svc.CreateOperatorRefund(ctx, accountOperator, second, 0, "retry")
	if err != nil || lost.Status != model.RefundStatusProcessing {
		t.Fatalf("lost refund = %+v, %v", lost, err)
	}
	// 易支付 has no record yet: left for an operator, never resubmitted.
	refundsBefore := len(ezfp.refunds)
	f.clock.Advance(31 * time.Second)
	f.svc.RecoverEzfp(ctx)
	if got, _ := f.store.GetRefundRequest(ctx, lost.ID); got.Status != model.RefundStatusProcessing || len(ezfp.refunds) != refundsBefore {
		t.Fatalf("an unknown refund changed: %+v, resubmitted %t", got, len(ezfp.refunds) != refundsBefore)
	}
	ezfp.refundQuery = func(id string) (billing.EzfpRefund, error) {
		return billing.EzfpRefund{RefundNo: "R" + id, OutRefundNo: id, MoneyMinor: 6800, Status: billing.EzfpRefundSucceeded}, nil
	}
	f.svc.RecoverEzfp(ctx)
	if got, _ := f.store.GetRefundRequest(ctx, lost.ID); got.Status != model.RefundStatusSucceeded {
		t.Fatalf("recovered refund = %+v", got)
	}
	if got := balanceOf(t, f.svc, f.ownerAt); got != 0 {
		t.Fatalf("balance after a recovered refund = %d", got)
	}

	// A refund 易支付 reports failed is failed, and its credits return.
	third := buy()
	reportedFailed, err := f.svc.CreateOperatorRefund(ctx, accountOperator, third, 0, "retry")
	if err != nil || reportedFailed.Status != model.RefundStatusProcessing {
		t.Fatalf("lost refund = %+v, %v", reportedFailed, err)
	}
	ezfp.refundQuery = func(id string) (billing.EzfpRefund, error) {
		return billing.EzfpRefund{RefundNo: "R" + id, OutRefundNo: id, Status: billing.EzfpRefundFailed}, nil
	}
	f.clock.Advance(31 * time.Second)
	f.svc.RecoverEzfp(ctx)
	if got, _ := f.store.GetRefundRequest(ctx, reportedFailed.ID); got.Status != model.RefundStatusFailed {
		t.Fatalf("reported-failed refund = %+v", got)
	}
	if got := balanceOf(t, f.svc, f.ownerAt); got != 10*model.MicroPerCredit {
		t.Fatalf("balance after a failed refund = %d", got)
	}

	// A tenant can ask to refund an 易支付 order like any other.
	refundable, err := f.svc.RefundableOrders(ctx, f.ownerAt, []model.CheckoutOrder{mustOrder(t, f, third)})
	if err != nil || refundable[third] != 6800 {
		t.Fatalf("refundable = %v, %v", refundable, err)
	}
}

func mustOrder(t *testing.T, f *fixture, id string) model.CheckoutOrder {
	t.Helper()
	order, err := f.store.GetCheckoutOrder(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return order
}

func TestEzfpAccountAndPresaleCheckouts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ezfp := &fakeEzfp{}
	inv := &countingInvalidator{}
	f.svc = logic.New(f.store, f.clock, logic.WithInvalidator(inv), logic.WithAccountSales(),
		logic.WithBilling(logic.BillingConfig{Ezfp: ezfpConfig(ezfp)}))

	draft := accountDraft("claude-a", 9900)
	draft.Currency, draft.DodoProductID = "CNY", ""
	sale, err := f.svc.SaveAccountSale(ctx, accountOperator, draft, true)
	if err != nil {
		t.Fatal(err)
	}
	usdDraft := accountDraft("codex-a", 2000)
	usdDraft.DodoProductID = ""
	if _, err := f.svc.SaveAccountSale(ctx, accountOperator, usdDraft, true); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("a USD account without a product was saved", err)
	}
	if dodoOK, ezfpOK := f.svc.FiatOffers(sale.Currency, sale.DodoProductID); dodoOK || !ezfpOK {
		t.Fatalf("offers = %t %t", dodoOK, ezfpOK)
	}
	if _, err := f.svc.CreateAccountCheckout(ctx, f.ownerAt, sale.ID, 9900, "CNY", logic.Payment{}); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("a Dodo checkout opened without Dodo", err)
	}
	checkout, err := f.svc.CreateAccountCheckout(ctx, f.ownerAt, sale.ID, 9900, "CNY", wxpay)
	if err != nil {
		t.Fatal(err)
	}
	if policy, _ := f.svc.AccountPolicy(ctx, f.tenant.ID); policy.Allows("agent", "claude-a", f.clock.Now()) {
		t.Fatal("a checkout granted access before payment")
	}
	if r := ezfp.deliver(t, f.svc, ezfp.paid(checkout.OrderID, f.tenant.ID, 9900)); r.Outcome != store.BillingOutcomeApplied || !r.AccessChanged {
		t.Fatalf("account payment = %+v", r)
	}
	if policy, _ := f.svc.AccountPolicy(ctx, f.tenant.ID); !policy.Allows("agent", "claude-a", f.clock.Now()) {
		t.Fatal("a paid month was not granted")
	}

	presale := presaleDraft(model.AccountKindCodex, 1)
	presale.Currency, presale.DodoProductID = "CNY", ""
	saved, err := f.svc.SaveAccountPresale(ctx, accountOperator, presale, true)
	if err != nil {
		t.Fatal(err)
	}
	if logic.PresaleAvailable(saved) || !f.svc.PresaleEzfpAvailable(saved) || !f.svc.PresaleFiatAvailable(saved) {
		t.Fatalf("presale availability: dodo %t ezfp %t", logic.PresaleAvailable(saved), f.svc.PresaleEzfpAvailable(saved))
	}
	listed, err := f.svc.ListAccountPresales(ctx, f.ownerAt)
	if err != nil || len(listed) != 1 {
		t.Fatalf("tenant presales = %+v, %v", listed, err)
	}
	pre, err := f.svc.CreatePresaleCheckout(ctx, f.ownerAt, saved.ID, 1500, "CNY", logic.Payment{Provider: "ezfp", PayType: "alipay"})
	if err != nil {
		t.Fatal(err)
	}
	// The only place is taken by the pending order.
	if _, err := f.svc.CreatePresaleCheckout(ctx, f.ownerAt, saved.ID, 1500, "CNY", wxpay); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("a full presale opened another checkout", err)
	}
	if r := ezfp.deliver(t, f.svc, ezfp.paid(pre.OrderID, f.tenant.ID, 1500)); r.Outcome != store.BillingOutcomeApplied {
		t.Fatalf("presale payment = %+v", r)
	}
	if got := mustOrder(t, f, pre.OrderID); got.Status != model.OrderStatusPaid || got.AccountExpiresAt != nil || got.PayType != "alipay" {
		t.Fatalf("presale order = %+v", got)
	}

	catalog, err := f.svc.Catalog(ctx)
	if err != nil || len(catalog.Presales) != 1 {
		t.Fatalf("catalog = %+v, %v", catalog, err)
	}
}

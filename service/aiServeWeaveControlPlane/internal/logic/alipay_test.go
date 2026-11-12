package logic_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// fakeAlipay is an in-memory 一人付: a quota per user, consumptions kept by
// idempotency key as the real service keeps them, and scripted faults.
type fakeAlipay struct {
	mu        sync.Mutex
	caps      billing.AlipayCapabilities
	product   billing.AlipayProduct
	remaining map[string]int64
	consumed  map[string]billing.AlipayConsumeRequest
	purchases []billing.AlipayPurchaseRequest
	calls     map[string]int

	// loseConsumeAnswer consumes but returns a transport error, once.
	loseConsumeAnswer bool
	// unknownConsume answers UNKNOWN without consuming, once.
	unknownConsume bool
	// lookupError fails every /consumption lookup.
	lookupError error
	// mismatch makes a success report a different amount.
	mismatch bool
	// purchaseStatus overrides READY.
	purchaseStatus string
	purchaseErr    error
}

func newFakeAlipay(unit string) *fakeAlipay {
	return &fakeAlipay{
		caps: billing.AlipayCapabilities{ProviderConfigured: true, PurchaseEntry: true, EntitlementQuery: true, Consumption: true},
		product: billing.AlipayProduct{
			CapabilityStatus: billing.AlipayCapabilityEnabled, ProductName: "点数包", BillingMode: billing.AlipayModeQuantity, QuotaUnit: unit,
			Options: []billing.AlipayOption{{SkuID: "sku_100", DisplayName: "100 积分", BillingMode: billing.AlipayModeQuantity, Price: "9.90", Currency: "CNY", QuotaUnit: unit, QuotaAmount: 100}},
		},
		remaining: map[string]int64{}, consumed: map[string]billing.AlipayConsumeRequest{}, calls: map[string]int{},
	}
}

func (f *fakeAlipay) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

func (f *fakeAlipay) Capabilities(context.Context) (billing.AlipayCapabilities, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["capabilities"]++
	return f.caps, nil
}

func (f *fakeAlipay) Product(context.Context) (billing.AlipayProduct, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["product"]++
	return f.product, nil
}

func (f *fakeAlipay) Access(_ context.Context, userID string, _ int64) (billing.AlipayAccess, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["access"]++
	return billing.AlipayAccess{QuotaUnit: f.product.QuotaUnit, Remaining: f.remaining[userID], Total: f.remaining[userID]}, nil
}

func (f *fakeAlipay) Purchase(_ context.Context, req billing.AlipayPurchaseRequest) (billing.AlipayPurchase, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["purchase"]++
	f.purchases = append(f.purchases, req)
	if f.purchaseErr != nil {
		return billing.AlipayPurchase{}, f.purchaseErr
	}
	status := billing.AlipayPurchaseReady
	if f.purchaseStatus != "" {
		status = f.purchaseStatus
	}
	return billing.AlipayPurchase{Status: status, URL: "https://openapi.alipay.example/entry/" + req.IdempotencyKey}, nil
}

func (f *fakeAlipay) answer(req billing.AlipayConsumeRequest) billing.AlipayConsumption {
	consumed := req.Amount
	if f.mismatch {
		consumed++
	}
	return billing.AlipayConsumption{Status: billing.AlipayConsumeSucceeded, Confirmed: true, IdempotencyKey: req.IdempotencyKey,
		QuotaUnit: f.product.QuotaUnit, Consumed: consumed, ConsumeOrderID: "co_" + req.IdempotencyKey}
}

func (f *fakeAlipay) Consume(_ context.Context, req billing.AlipayConsumeRequest) (billing.AlipayConsumption, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["consume"]++
	if prev, ok := f.consumed[req.IdempotencyKey]; ok {
		if prev != req {
			return billing.AlipayConsumption{}, &billing.AlipayError{Status: 409, Code: "IDEMPOTENCY_CONFLICT"}
		}
		return f.answer(req), nil
	}
	if f.unknownConsume {
		f.unknownConsume = false
		return billing.AlipayConsumption{Status: billing.AlipayConsumeUnknown, IdempotencyKey: req.IdempotencyKey}, nil
	}
	if f.remaining[req.UserID] < req.Amount {
		return billing.AlipayConsumption{Status: billing.AlipayConsumeFailed, IdempotencyKey: req.IdempotencyKey, ReasonCode: "NOW_PAY_QUOTA_INSUFFICIENT"}, nil
	}
	f.remaining[req.UserID] -= req.Amount
	f.consumed[req.IdempotencyKey] = req
	if f.loseConsumeAnswer {
		f.loseConsumeAnswer = false
		return billing.AlipayConsumption{}, context.DeadlineExceeded
	}
	return f.answer(req), nil
}

func (f *fakeAlipay) Consumption(_ context.Context, userID, key string) (billing.AlipayConsumption, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["consumption"]++
	if f.lookupError != nil {
		return billing.AlipayConsumption{}, f.lookupError
	}
	req, ok := f.consumed[key]
	if !ok {
		return billing.AlipayConsumption{}, &billing.AlipayError{Status: 404, Code: "NOT_FOUND"}
	}
	if req.UserID != userID {
		return billing.AlipayConsumption{}, &billing.AlipayError{Status: 409, Code: "CONFLICT"}
	}
	return f.answer(req), nil
}

const alipayRate = 2_000_000 // two credits per unit

func alipayFixture(t *testing.T, unit string) (*fixture, *fakeAlipay, *countingInvalidator) {
	t.Helper()
	f := newFixture(t)
	fake := newFakeAlipay(unit)
	inv := &countingInvalidator{}
	f.svc = logic.New(f.store, f.clock, logic.WithInvalidator(inv), logic.WithBilling(logic.BillingConfig{
		Alipay: &logic.AlipayConfig{Client: fake, CreditsPerUnitMicro: alipayRate, ReturnURL: "https://console.example/console/billing?tab=alipay"},
	}))
	return f, fake, inv
}

func balance(t *testing.T, f *fixture) int64 {
	t.Helper()
	overview, err := f.svc.BillingOverview(context.Background(), f.ownerAt)
	if err != nil {
		t.Fatal(err)
	}
	return overview.BalanceMicro
}

func TestAlipayOnlyBillingTurnsDodoOff(t *testing.T) {
	f, _, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	overview, err := f.svc.BillingOverview(ctx, f.ownerAt)
	if err != nil || overview.Dodo || !overview.Alipay || len(overview.Packages) != 0 {
		t.Fatalf("overview %+v %v", overview, err)
	}
	if _, err := f.svc.CreateCheckout(ctx, f.ownerAt, "pkg_x", 0, logic.Payment{}); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("checkout without Dodo", err)
	}
	if _, err := f.svc.BillingPortalLink(ctx, f.ownerAt); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("portal without Dodo", err)
	}
	if _, err := f.svc.RequestRefund(ctx, f.ownerAt, "ord_x", 0, "why"); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("refund without Dodo", err)
	}
	if _, err := f.svc.HandleBillingWebhook(ctx, []byte("{}"), nil); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("webhook without Dodo", err)
	}
	if catalog, err := f.svc.Catalog(ctx); err != nil || len(catalog.Packages) != 0 {
		t.Fatal("catalog", catalog, err)
	}
}

func TestAlipayDisabledAnswersNotFound(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.AlipayOverview(context.Background(), f.ownerAt); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := f.svc.RedeemAlipay(context.Background(), f.ownerAt, "action-0001", 1); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestAlipayOverviewAndRoles(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	fake.remaining[f.tenant.ID] = 40
	got, err := f.svc.AlipayOverview(ctx, f.ownerAt)
	if err != nil || !got.Supported || !got.CanPurchase || !got.CanRedeem || !got.AccessKnown || got.Remaining != 40 ||
		len(got.Plans) != 1 || got.Plans[0].Price != "9.90" || got.MaxAmount != billing.MaxAlipayPointAmount || got.CreditsPerUnitMicro != alipayRate {
		t.Fatalf("overview %+v %v", got, err)
	}
	// The plans are cached; the quota is not.
	if _, err := f.svc.AlipayOverview(ctx, f.actorWithRole(model.RoleMember)); err != nil {
		t.Fatal("a member may read", err)
	}
	if fake.count("product") != 1 || fake.count("access") != 2 {
		t.Fatal("cache", fake.calls)
	}
	member := f.actorWithRole(model.RoleMember)
	if _, err := f.svc.RedeemAlipay(ctx, member, "action-0001", 1); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("member redeemed", err)
	}
	if _, err := f.svc.CreateAlipayPurchase(ctx, member, "action-0001", ""); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("member purchased", err)
	}
	if _, err := f.svc.AlipayOverview(ctx, logic.Actor{TenantID: model.PlatformScope, Role: model.RolePlatformOperator}); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("operator has no Alipay user", err)
	}
	for _, bad := range []string{"", "short", "has space in it", "x/../../../etc"} {
		if _, err := f.svc.RedeemAlipay(ctx, f.ownerAt, bad, 1); !errors.Is(err, logic.ErrInvalidInput) {
			t.Fatal("action id", bad, err)
		}
	}
	if _, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-0001", 0); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("zero amount", err)
	}
	if _, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-0001", billing.MaxAlipayPointAmount+1); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("amount above the limit", err)
	}
}

func TestAlipayUnsupportedModeIsNotOffered(t *testing.T) {
	f, fake, _ := alipayFixture(t, "")
	fake.product.BillingMode = billing.AlipayModeDuration
	ctx := context.Background()
	got, err := f.svc.AlipayOverview(ctx, f.ownerAt)
	if err != nil || got.Supported || got.CanPurchase || got.CanRedeem || len(got.Plans) != 0 || got.AccessKnown {
		t.Fatalf("duration package offered: %+v %v", got, err)
	}
	if _, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-0001", 1); !errors.Is(err, logic.ErrConflict) {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateAlipayPurchase(ctx, f.ownerAt, "action-0001", ""); !errors.Is(err, logic.ErrConflict) {
		t.Fatal(err)
	}
	if fake.count("consume") != 0 || fake.count("purchase") != 0 {
		t.Fatal("called 一人付 for an unavailable feature", fake.calls)
	}
}

func TestAlipayNotEnabledYetIsNotOffered(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	fake.product.CapabilityStatus = "PENDING"
	got, err := f.svc.AlipayOverview(context.Background(), f.ownerAt)
	if err != nil || !got.Supported || got.CanPurchase || got.CanRedeem {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAlipayPurchaseKeepsItsKey(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	if _, err := f.svc.CreateAlipayPurchase(ctx, f.ownerAt, "action-buy-1", "sku_unknown"); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("unlisted sku", err)
	}
	// A lost answer, then the same click again: the same request is sent.
	fake.purchaseErr = context.DeadlineExceeded
	if _, err := f.svc.CreateAlipayPurchase(ctx, f.ownerAt, "action-buy-1", "sku_100"); !errors.Is(err, logic.ErrUnavailable) {
		t.Fatal(err)
	}
	fake.purchaseErr = nil
	got, err := f.svc.CreateAlipayPurchase(ctx, f.ownerAt, "action-buy-1", "sku_100")
	if err != nil || got.PurchaseURL == "" {
		t.Fatal(got, err)
	}
	if len(fake.purchases) != 2 || fake.purchases[0] != fake.purchases[1] || fake.purchases[0].UserID != f.tenant.ID ||
		fake.purchases[0].CallbackURL != "https://console.example/console/billing?tab=alipay" {
		t.Fatalf("retry must repeat the request: %+v", fake.purchases)
	}
	if _, err := f.svc.CreateAlipayPurchase(ctx, f.ownerAt, "action-buy-1", ""); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("same action, other sku", err)
	}
	// An entry Alipay reports failed needs a new click.
	fake.purchaseStatus = billing.AlipayPurchaseFailed
	if _, err := f.svc.CreateAlipayPurchase(ctx, f.ownerAt, "action-buy-2", ""); !errors.Is(err, logic.ErrConflict) {
		t.Fatal(err)
	}
	fake.purchaseStatus = ""
	if _, err := f.svc.CreateAlipayPurchase(ctx, f.ownerAt, "action-buy-2", ""); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("a failed entry must not be reopened", err)
	}
	// A purchase entry is not a payment: nothing was credited.
	if balance(t, f) != 0 {
		t.Fatal("a purchase entry booked credits")
	}
}

func TestAlipayRedeemBooksCreditsOnce(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	fake.remaining[f.tenant.ID] = 30
	got, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-redeem-1", 10)
	if err != nil || got.Status != model.AlipayRedemptionSucceeded || got.CreditsMicro != 10*alipayRate {
		t.Fatal(got, err)
	}
	if balance(t, f) != 10*alipayRate {
		t.Fatal("balance", balance(t, f))
	}
	// The same click again returns the settled redemption, consuming nothing.
	again, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-redeem-1", 10)
	if err != nil || again.ID != got.ID || fake.count("consume") != 1 || balance(t, f) != 10*alipayRate {
		t.Fatal("replay consumed again", again, err, fake.calls)
	}
	if fake.remaining[f.tenant.ID] != 20 {
		t.Fatal("quota", fake.remaining)
	}
	// Not enough quota is a definite failure, and frees the tenant to retry.
	failed, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-redeem-2", 25)
	if err != nil || failed.Status != model.AlipayRedemptionFailed || failed.ReasonCode != "NOW_PAY_QUOTA_INSUFFICIENT" {
		t.Fatal(failed, err)
	}
	if ok, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-redeem-3", 20); err != nil || ok.Status != model.AlipayRedemptionSucceeded {
		t.Fatal(ok, err)
	}
	if balance(t, f) != 30*alipayRate {
		t.Fatal("balance", balance(t, f))
	}
	page, err := f.svc.ListAlipayRedemptions(ctx, f.actorWithRole(model.RoleMember), store.ListQuery{Limit: 10})
	if err != nil || len(page.Items) != 3 {
		t.Fatal(page.Items, err)
	}
}

func TestAlipayCountRedeemsOneAtATime(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitCount)
	fake.remaining[f.tenant.ID] = 3
	if _, err := f.svc.RedeemAlipay(context.Background(), f.ownerAt, "action-count-1", 2); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal(err)
	}
	if got, err := f.svc.RedeemAlipay(context.Background(), f.ownerAt, "action-count-1", 1); err != nil || got.Status != model.AlipayRedemptionSucceeded {
		t.Fatal(got, err)
	}
}

func TestAlipayLostAnswerIsResumedNotRepeated(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	fake.remaining[f.tenant.ID] = 10
	fake.loseConsumeAnswer = true
	got, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-lost-1", 10)
	if err != nil || got.Status != model.AlipayRedemptionPending {
		t.Fatal("a lost answer must leave the redemption pending", got, err)
	}
	if balance(t, f) != 0 {
		t.Fatal("credited before confirmation")
	}
	// Another click while it is pending may not start a second consumption.
	if _, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-lost-2", 1); !errors.Is(err, logic.ErrConflict) {
		t.Fatal(err)
	}
	overview, err := f.svc.AlipayOverview(ctx, f.ownerAt)
	if err != nil || overview.Pending == nil || overview.Pending.ID != got.ID {
		t.Fatal("pending not shown", overview.Pending, err)
	}
	// Refreshing right away is throttled; after the gap it looks up the
	// original consumption and books it.
	if row, err := f.svc.RefreshAlipayRedemption(ctx, f.ownerAt, got.ID); err != nil || row.Status != model.AlipayRedemptionPending || fake.count("consumption") != 0 {
		t.Fatal("refresh inside the gap", row, err, fake.calls)
	}
	f.clock.Advance(10 * time.Second)
	row, err := f.svc.RefreshAlipayRedemption(ctx, f.ownerAt, got.ID)
	if err != nil || row.Status != model.AlipayRedemptionSucceeded || fake.count("consume") != 1 || fake.count("consumption") != 1 {
		t.Fatal("resume", row, err, fake.calls)
	}
	if balance(t, f) != 10*alipayRate {
		t.Fatal("balance", balance(t, f))
	}
}

func TestAlipayUnknownIsResentWithTheSameKey(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	fake.remaining[f.tenant.ID] = 5
	fake.unknownConsume = true
	got, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-unknown-1", 5)
	if err != nil || got.Status != model.AlipayRedemptionPending {
		t.Fatal(got, err)
	}
	// The process "restarts": the recovery finds it, the lookup finds no
	// consumption, and the original request is sent again.
	f.clock.Advance(time.Minute)
	f.svc.RecoverAlipayRedemptions(ctx)
	row, err := f.svc.RefreshAlipayRedemption(ctx, f.ownerAt, got.ID)
	if err != nil || row.Status != model.AlipayRedemptionSucceeded || fake.count("consume") != 2 {
		t.Fatal(row, err, fake.calls)
	}
	if len(fake.consumed) != 1 || fake.consumed[got.ID].Amount != 5 {
		t.Fatal("consumed under another key", fake.consumed)
	}
	if balance(t, f) != 5*alipayRate {
		t.Fatal("balance", balance(t, f))
	}
}

func TestAlipayMismatchedSuccessIsNotBooked(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	fake.remaining[f.tenant.ID] = 5
	fake.mismatch = true
	got, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-mismatch-1", 2)
	if err != nil || got.Status != model.AlipayRedemptionPending || balance(t, f) != 0 {
		t.Fatal("a mismatched success was booked", got, err)
	}
}

func TestAlipayLookupOutageKeepsPending(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	fake.remaining[f.tenant.ID] = 5
	fake.loseConsumeAnswer = true
	got, _ := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-outage-1", 1)
	fake.lookupError = &billing.AlipayError{Status: 503, Code: "SERVICE_UNAVAILABLE", Retryable: true}
	f.clock.Advance(time.Minute)
	row, err := f.svc.RefreshAlipayRedemption(ctx, f.ownerAt, got.ID)
	if err != nil || row.Status != model.AlipayRedemptionPending || fake.count("consume") != 1 {
		t.Fatal("an outage must neither fail nor re-consume", row, err, fake.calls)
	}
	// Another tenant cannot see or resume it.
	other, otherOwner, err := f.svc.CreateTenant(ctx, "Other", "other-alipay@example.com", testPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RefreshAlipayRedemption(ctx, logic.Actor{TenantID: other.ID, UserID: otherOwner.ID, Role: model.RoleOwner}, got.ID); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("cross-tenant refresh", err)
	}
}

package logic_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// alipayPresale is an Alipay-only presale of amount units, with capacity places.
func alipayPresale(t *testing.T, f *fixture, amount int64, capacity int) store.PresaleStock {
	t.Helper()
	draft := presaleDraft(model.AccountKindClaude, capacity)
	draft.DodoProductID, draft.AlipayAmount = "", amount
	row, err := f.svc.SaveAccountPresale(context.Background(), accountOperator, draft, true)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func orderOf(t *testing.T, f *fixture, id string) model.CheckoutOrder {
	t.Helper()
	order, err := f.store.GetCheckoutOrder(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return order
}

func TestAlipayPresaleDraftValidation(t *testing.T) {
	f, _, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	for name, mutate := range map[string]func(*model.AccountPresale){
		"neither Dodo nor Alipay": func(p *model.AccountPresale) { p.DodoProductID, p.AlipayAmount = "", 0 },
		"negative amount":         func(p *model.AccountPresale) { p.AlipayAmount = -1 },
		"huge amount":             func(p *model.AccountPresale) { p.AlipayAmount = billing.MaxAlipayPointAmount + 1 },
	} {
		draft := presaleDraft(model.AccountKindClaude, 1)
		mutate(&draft)
		if _, err := f.svc.SaveAccountPresale(ctx, accountOperator, draft, true); !errors.Is(err, logic.ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	row := alipayPresale(t, f, 5, 1)
	if row.AlipayAmount != 5 || !logic.PresaleAlipayAvailable(row) || logic.PresaleAvailable(row) {
		t.Fatalf("an Alipay-only presale: %+v", row)
	}
	if rows, err := f.svc.ListAccountPresales(ctx, f.ownerAt); err != nil || len(rows) != 1 {
		t.Fatal("a tenant does not see an Alipay-only presale", rows, err)
	}
}

func TestAlipayPresalePaymentMarksTheOrderWaiting(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	presale := alipayPresale(t, f, 2, 1)
	fake.remaining[f.tenant.ID] = 3

	if _, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.actorWithRole(model.RoleMember), presale.ID, "action-pre-1", 2); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("member could pay", err)
	}
	if _, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, presale.ID, "action-pre-1", 1); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("stale amount", err)
	}
	if _, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, "pre_missing", "action-pre-1", 2); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("missing presale", err)
	}
	if fake.count("consume") != 0 {
		t.Fatal("a refused request consumed quota")
	}

	got, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, presale.ID, "action-pre-1", 2)
	if err != nil || got.Status != model.AlipayRedemptionSucceeded || got.PresaleOrderID == "" || got.Amount != 2 || got.CreditsMicro != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if fake.remaining[f.tenant.ID] != 1 {
		t.Fatal("quota not consumed", fake.remaining[f.tenant.ID])
	}
	if got := fake.consumed[got.ID]; got.Amount != 2 || got.UserID != f.tenant.ID {
		t.Fatalf("consumption %+v", got)
	}
	// Paid, waiting for an account; no credits and no mode change.
	order := orderOf(t, f, got.PresaleOrderID)
	if order.Status != model.OrderStatusPaid || order.AlipayAmount != 2 || order.PresaleID != presale.ID || !store.PresaleAwaiting(order) {
		t.Fatalf("order %+v", order)
	}
	if balance(t, f) != 0 {
		t.Fatal("paying a presale booked credits")
	}
	if ledger, err := f.svc.ListBillingLedger(ctx, f.ownerAt, store.ListQuery{Limit: 10}, store.CreditLedgerFilter{}); err != nil || len(ledger.Items) != 0 {
		t.Fatal("ledger", ledger.Items, err)
	}
	awaiting, err := f.svc.ListBillingOrders(ctx, accountOperator, store.ListQuery{Limit: 10}, store.CheckoutOrderFilter{Awaiting: true})
	if err != nil || len(awaiting.Items) != 1 || awaiting.Items[0].ID != order.ID {
		t.Fatal("awaiting orders", awaiting.Items, err)
	}

	// The same click again is the same order, consumed once.
	again, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, presale.ID, "action-pre-1", 2)
	if err != nil || again.ID != got.ID || fake.count("consume") != 1 {
		t.Fatalf("a retried click: %+v %v consumes=%d", again, err, fake.count("consume"))
	}
	// The same click cannot pay for another presale.
	other := alipayPresale(t, f, 2, 1)
	if _, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, other.ID, "action-pre-1", 2); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("an action id was reused for another presale", err)
	}
	// The place is taken until an account is assigned.
	if _, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, presale.ID, "action-pre-2", 2); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("capacity exceeded", err)
	}

	// Alipay cannot undo a consumption, so the order is never refundable.
	_, _, _, err = f.store.OpenRefundRequest(ctx, model.RefundRequest{
		ID: model.NewID(model.PrefixRefundRequest), TenantID: f.tenant.ID, OrderID: order.ID, Source: model.RefundSourceOperator,
		Status: model.RefundStatusPending, Reason: "r",
	}, store.RefundPolicy{}, f.clock.Now())
	var refused *store.RefundRefusedError
	if !errors.As(err, &refused) || refused.Reason != store.RefundRefusedOrderState {
		t.Fatal("an Alipay order was refundable", err)
	}
}

func TestAlipayPresaleFailureFreesThePlace(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	presale := alipayPresale(t, f, 2, 1)
	fake.remaining[f.tenant.ID] = 1 // less than the presale costs

	got, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, presale.ID, "action-pre-1", 2)
	if err != nil || got.Status != model.AlipayRedemptionFailed || got.ReasonCode != "NOW_PAY_QUOTA_INSUFFICIENT" {
		t.Fatalf("%+v %v", got, err)
	}
	if order := orderOf(t, f, got.PresaleOrderID); order.Status != model.OrderStatusFailed || store.PresaleAwaiting(order) {
		t.Fatalf("order %+v", order)
	}
	if rows, err := f.svc.ListAccountPresales(ctx, accountOperator); err != nil || rows[0].Outstanding != 0 {
		t.Fatal("the failed order still holds its place", rows, err)
	}
	// With enough quota, a new click gets the place.
	fake.remaining[f.tenant.ID] = 2
	if row, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, presale.ID, "action-pre-2", 2); err != nil || row.Status != model.AlipayRedemptionSucceeded {
		t.Fatalf("%+v %v", row, err)
	}
}

func TestAlipayPresaleUnknownAnswerIsResumedNotRepeated(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitPoint)
	ctx := context.Background()
	presale := alipayPresale(t, f, 2, 1)
	fake.remaining[f.tenant.ID] = 2
	fake.loseConsumeAnswer = true

	got, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, presale.ID, "action-pre-1", 2)
	if err != nil || got.Status != model.AlipayRedemptionPending {
		t.Fatalf("%+v %v", got, err)
	}
	// Pending: the order holds its place and no other redemption may start.
	if order := orderOf(t, f, got.PresaleOrderID); order.Status != model.OrderStatusPending {
		t.Fatalf("order %+v", order)
	}
	if _, err := f.svc.RedeemAlipay(ctx, f.ownerAt, "action-credit-1", 1); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("a second redemption raced a pending one", err)
	}
	if overview, err := f.svc.AlipayOverview(ctx, f.ownerAt); err != nil || overview.Pending == nil || overview.Pending.PresaleOrderID != got.PresaleOrderID {
		t.Fatalf("overview %+v %v", overview.Pending, err)
	}

	f.clock.Advance(10 * time.Second)
	row, err := f.svc.RefreshAlipayRedemption(ctx, f.ownerAt, got.ID)
	if err != nil || row.Status != model.AlipayRedemptionSucceeded {
		t.Fatalf("%+v %v", row, err)
	}
	if fake.count("consume") != 1 || fake.remaining[f.tenant.ID] != 0 {
		t.Fatalf("quota consumed twice: consumes=%d remaining=%d", fake.count("consume"), fake.remaining[f.tenant.ID])
	}
	if order := orderOf(t, f, got.PresaleOrderID); order.Status != model.OrderStatusPaid || !store.PresaleAwaiting(order) {
		t.Fatalf("order %+v", order)
	}
}

func TestAlipayPresaleNeedsMoreThanACountPackageGives(t *testing.T) {
	f, fake, _ := alipayFixture(t, billing.AlipayUnitCount)
	ctx := context.Background()
	fake.remaining[f.tenant.ID] = 5
	// A count package is consumed one at a time, so two cannot be asked for.
	two := alipayPresale(t, f, 2, 1)
	if _, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, two.ID, "action-pre-1", 2); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("two counts at once", err)
	}
	if fake.count("consume") != 0 {
		t.Fatal("a refused request consumed quota")
	}
	one := alipayPresale(t, f, 1, 1)
	if row, err := f.svc.CreatePresaleAlipayCheckout(ctx, f.ownerAt, one.ID, "action-pre-2", 1); err != nil || row.Status != model.AlipayRedemptionSucceeded {
		t.Fatalf("%+v %v", row, err)
	}
}

package storetest

import (
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// PresaleStore is what the presale scenario uses.
type PresaleStore interface {
	AccountStore
	store.AccountPresales
	OpenRefundRequest(context.Context, model.RefundRequest, store.RefundPolicy, time.Time) (model.RefundRequest, model.CheckoutOrder, bool, error)
}

// PresaleCheckouts checks presale capacity holds, payments that wait for an
// account, operator assignment by kind, renewal through assignment, and the
// states that refuse an assignment.
//
// PresaleCheckouts 检查预售名额占用、付款后等待分配、运维按类型分配、经分配续期，
// 以及拒绝分配的各种状态。
func PresaleCheckouts(t *testing.T, st PresaleStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	epoch := time.Unix(0, 0).UTC()
	presale := model.AccountPresale{ID: "pre_claude", Kind: model.AccountKindClaude, Name: "Claude presale", PriceMinor: 1500,
		Currency: "USD", DodoProductID: "pdt_pre", Capacity: 2, Active: true, UpdatedAt: now}
	if err := st.SaveAccountPresale(ctx, presale, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAccountPresale(ctx, presale, true); !errors.Is(err, store.ErrConflict) {
		t.Fatal("duplicate presale", err)
	}
	changedKind := presale
	changedKind.Kind = model.AccountKindCodex
	if err := st.SaveAccountPresale(ctx, changedKind, false); !errors.Is(err, store.ErrConflict) {
		t.Fatal("presale kind changed", err)
	}
	// An archived, unpriced claude account kept only for presales; a listed
	// claude account; a codex account; a legacy account with no kind.
	accounts := []model.AccountSale{
		{ID: "acc_pre_free", NodeID: "agent-pre", RuntimeID: "claude-free", Kind: model.AccountKindClaude, Name: "free", ExpiresAt: epoch, UpdatedAt: now},
		{ID: "acc_pre_listed", NodeID: "agent-pre", RuntimeID: "claude-listed", Kind: model.AccountKindClaude, Name: "listed", PriceMinor: 2000,
			Currency: "USD", DodoProductID: "pdt", Active: true, ExpiresAt: epoch, UpdatedAt: now},
		{ID: "acc_pre_codex", NodeID: "agent-pre", RuntimeID: "codex-a", Kind: model.AccountKindCodex, Name: "codex", ExpiresAt: epoch, UpdatedAt: now},
		{ID: "acc_pre_legacy", NodeID: "agent-pre", RuntimeID: "claude-legacy", Name: "legacy", ExpiresAt: epoch, UpdatedAt: now},
	}
	for _, account := range accounts {
		if err := st.SaveAccountSale(ctx, account, true); err != nil {
			t.Fatal(err)
		}
	}
	seq := 0
	var seqMu sync.Mutex
	next := func(prefix string) string {
		seqMu.Lock()
		defer seqMu.Unlock()
		seq++
		return fmt.Sprintf("%s%d", prefix, seq)
	}
	open := func(tenantID string, amount int64, at time.Time) (model.CheckoutOrder, error) {
		order := model.CheckoutOrder{ID: next("ord_p"), TenantID: tenantID, PresaleID: presale.ID, DodoProductID: "pdt_pre",
			AmountMinor: amount, Currency: "USD", Status: model.OrderStatusPending, CreatedBy: "owner", CreatedAt: at, UpdatedAt: at}
		return order, st.CreatePresaleCheckoutOrder(ctx, &order, at)
	}
	mustOpen := func(tenantID string, at time.Time) model.CheckoutOrder {
		t.Helper()
		order, err := open(tenantID, 1500, at)
		if err != nil {
			t.Fatal(err)
		}
		return order
	}
	pay := func(order model.CheckoutOrder, at time.Time) store.BillingEventResult {
		t.Helper()
		result, err := st.ApplyBillingEvent(ctx, store.BillingEvent{WebhookID: next("wh_p"), Type: "payment.succeeded",
			Kind: store.BillingEventPaymentSucceeded, OrderID: order.ID, TenantID: order.TenantID, PaymentID: "pay_" + order.ID,
			AmountMinor: order.AmountMinor, Currency: "USD"}, at)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	outstanding := func(at time.Time) int {
		t.Helper()
		rows, err := st.ListAccountPresales(ctx, at)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == presale.ID {
				return row.Outstanding
			}
		}
		t.Fatal("presale missing", rows)
		return 0
	}
	lease := func(id string) model.AccountSale {
		t.Helper()
		rows, err := st.ListAccountSales(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == id {
				return row
			}
		}
		t.Fatal("account missing", id)
		return model.AccountSale{}
	}
	awaiting := func() []string {
		t.Helper()
		page, err := st.ListCheckoutOrders(ctx, store.ListQuery{Limit: 100}, store.CheckoutOrderFilter{Awaiting: true})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(page.Items))
		for _, order := range page.Items {
			ids = append(ids, order.ID)
		}
		return ids
	}

	// A stale price is refused; concurrent checkouts fill exactly the capacity.
	if _, err := open("t1", 1499, now); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale price", err)
	}
	type opened struct {
		order model.CheckoutOrder
		err   error
	}
	results := make(chan opened, 6)
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			order, err := open(fmt.Sprintf("t%d", i+1), 1500, now)
			results <- opened{order, err}
		})
	}
	wg.Wait()
	close(results)
	var won []model.CheckoutOrder
	for r := range results {
		switch {
		case r.err == nil:
			won = append(won, r.order)
		case !errors.Is(r.err, store.ErrConflict):
			t.Fatal(r.err)
		}
	}
	if len(won) != 2 || outstanding(now) != 2 {
		t.Fatalf("capacity winners=%d outstanding=%d", len(won), outstanding(now))
	}
	a, b := won[0], won[1]
	// A failed checkout releases its place; an abandoned one holds it only
	// until AccountCheckoutHold passes.
	if err := st.FailCheckoutOrder(ctx, b.ID, now); err != nil {
		t.Fatal(err)
	}
	c := mustOpen("t7", now)
	if _, err := open("t8", 1500, now.Add(model.AccountCheckoutHold-time.Second)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("hold expired early", err)
	}

	// Payment marks the order paid and waiting, without granting anything.
	if got := pay(a, now); got.Outcome != store.BillingOutcomeApplied || got.AccessChanged || got.BlockedChanged {
		t.Fatal("presale payment", got)
	}
	if got := pay(a, now); got.Outcome != store.BillingOutcomeDuplicate {
		t.Fatal("replayed presale payment", got)
	}
	if row, err := st.GetTenantBilling(ctx, a.TenantID); err == nil && (row.Mode == model.BillingModeAccountMonthly || row.BalanceMicro != 0) {
		t.Fatal("presale payment changed billing", row)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if got := awaiting(); len(got) != 1 || got[0] != a.ID {
		t.Fatal("awaiting orders", got)
	}
	if outstanding(now) != 2 {
		t.Fatal("paid order released its place", outstanding(now))
	}

	at := now.Add(2 * time.Hour)
	assign := func(order model.CheckoutOrder, accountID string, when time.Time) (model.CheckoutOrder, error) {
		return st.AssignPresaleOrder(ctx, order.ID, accountID, when)
	}
	// Refusals: an unpaid order, a wrong kind, a legacy account without a
	// kind, missing rows, and an account another tenant's checkout holds.
	if _, err := assign(c, "acc_pre_free", at); !errors.Is(err, store.ErrConflict) {
		t.Fatal("unpaid order assigned", err)
	}
	if _, err := assign(a, "acc_pre_codex", at); !errors.Is(err, store.ErrConflict) {
		t.Fatal("wrong kind assigned", err)
	}
	if _, err := assign(a, "acc_pre_legacy", at); !errors.Is(err, store.ErrConflict) {
		t.Fatal("legacy account assigned", err)
	}
	if _, err := assign(a, "acc_missing", at); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("missing account", err)
	}
	if _, err := st.AssignPresaleOrder(ctx, "ord_missing", "acc_pre_free", at); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("missing order", err)
	}
	held := model.CheckoutOrder{ID: next("ord_h"), TenantID: "t9", AccountID: "acc_pre_listed", DodoProductID: "pdt",
		AmountMinor: 2000, Currency: "USD", Status: model.OrderStatusPending, CreatedBy: "owner", CreatedAt: at, UpdatedAt: at}
	if err := st.CreateAccountCheckoutOrder(ctx, &held, at); err != nil {
		t.Fatal(err)
	}
	if _, err := assign(a, "acc_pre_listed", at); !errors.Is(err, store.ErrConflict) {
		t.Fatal("held account assigned", err)
	}
	if lease("acc_pre_listed").TenantID != "" {
		t.Fatal("refused assignment granted")
	}

	// A refund in progress blocks the assignment.
	pay(c, now)
	if _, _, _, err := st.OpenRefundRequest(ctx, model.RefundRequest{ID: next("rfd_p"), OrderID: c.ID, Source: model.RefundSourceOperator,
		Status: model.RefundStatusProcessing, Reason: "cannot deliver", RequestedBy: "op", ReviewedBy: "op"}, store.RefundPolicy{}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := assign(c, "acc_pre_free", at); !errors.Is(err, store.ErrConflict) {
		t.Fatal("refunding order assigned", err)
	}

	// Assignment starts the month at once and switches the tenant.
	got, err := assign(a, "acc_pre_free", at)
	if err != nil {
		t.Fatal(err)
	}
	firstEnd := accountaccess.NextMonth(at)
	if got.AccountID != "acc_pre_free" || got.AccountExpiresAt == nil || !got.AccountExpiresAt.Equal(firstEnd) {
		t.Fatal("assigned order", got)
	}
	if row := lease("acc_pre_free"); row.TenantID != a.TenantID || !row.ExpiresAt.Equal(firstEnd) || row.Active {
		t.Fatal("assigned lease", row)
	}
	if row, err := st.GetTenantBilling(ctx, a.TenantID); err != nil || row.Mode != model.BillingModeAccountMonthly {
		t.Fatal("assigned tenant billing", row, err)
	}
	if _, err := assign(a, "acc_pre_free", at); !errors.Is(err, store.ErrConflict) {
		t.Fatal("order assigned twice", err)
	}
	if got := awaiting(); len(got) != 1 || got[0] != c.ID {
		t.Fatal("awaiting after assignment", got)
	}
	if outstanding(at) != 1 {
		t.Fatal("assignment kept its place", outstanding(at))
	}
	orders, err := st.ListCheckoutOrders(ctx, store.ListQuery{Limit: 100}, store.CheckoutOrderFilter{TenantID: a.TenantID})
	if err != nil || len(orders.Items) != 1 || orders.Items[0].AccountID != "acc_pre_free" || orders.Items[0].AccountExpiresAt == nil {
		t.Fatal("stored assignment", orders.Items, err)
	}

	// Another tenant cannot be given the account while it is held; the same
	// tenant's next presale month extends from its expiry.
	bigger := presale
	bigger.Capacity, bigger.UpdatedAt = 10, at
	if err := st.SaveAccountPresale(ctx, bigger, false); err != nil {
		t.Fatal(err)
	}
	other := mustOpen("t20", at)
	pay(other, at)
	if _, err := assign(other, "acc_pre_free", at); !errors.Is(err, store.ErrConflict) {
		t.Fatal("occupied account assigned", err)
	}
	renewal := mustOpen(a.TenantID, at)
	pay(renewal, at)
	if got, err := assign(renewal, "acc_pre_free", at.Add(time.Hour)); err != nil || !got.AccountExpiresAt.Equal(accountaccess.NextMonth(firstEnd)) {
		t.Fatal("renewal by assignment", got, err)
	}

	// Archiving stops new checkouts; a checkout opened before still waits
	// once paid.
	late := mustOpen("t30", at)
	archived := bigger
	archived.Active = false
	if err := st.SaveAccountPresale(ctx, archived, false); err != nil {
		t.Fatal(err)
	}
	if _, err := open("t30", 1500, at); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("archived presale checked out", err)
	}
	if got := pay(late, at.Add(time.Hour)); got.Outcome != store.BillingOutcomeApplied {
		t.Fatal("payment after archive", got)
	}
	if got := awaiting(); len(got) != 3 {
		t.Fatal("awaiting after archive", got)
	}
}

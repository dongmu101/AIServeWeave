// Package storetest holds store scenarios that the in-memory and database
// stores must both pass.
//
// storetest 包含内存 store 与数据库 store 都必须通过的场景。
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

// AccountStore is what the monthly account scenario uses.
type AccountStore interface {
	store.AccountSales
	ApplyBillingEvent(context.Context, store.BillingEvent, time.Time) (store.BillingEventResult, error)
	GetTenantBilling(context.Context, string) (model.TenantBilling, error)
	ListCheckoutOrders(context.Context, store.ListQuery, store.CheckoutOrderFilter) (store.Page[model.CheckoutOrder], error)
}

// AccountCheckouts checks fiat checkout holds, payment grants, renewal,
// replays, archived accounts and a late conflicting payment.
//
// AccountCheckouts 检查法币结账占用、付款开通、续费、重放、下架账号与迟到的冲突付款。
func AccountCheckouts(t *testing.T, st AccountStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	sale := model.AccountSale{ID: "acc_fiat", NodeID: "agent-fiat", RuntimeID: "claude-a", Name: "A", PriceMinor: 2000, Currency: "USD",
		DodoProductID: "pdt", Active: true, ExpiresAt: time.Unix(0, 0).UTC(), UpdatedAt: now}
	if err := st.SaveAccountSale(ctx, sale, true); err != nil {
		t.Fatal(err)
	}
	seq := 0
	open := func(tenantID string, amount int64, at time.Time) (model.CheckoutOrder, error) {
		seq++
		order := model.CheckoutOrder{ID: fmt.Sprintf("ord_%d", seq), TenantID: tenantID, AccountID: sale.ID, DodoProductID: "pdt",
			AmountMinor: amount, Currency: "USD", Status: model.OrderStatusPending, CreatedBy: "owner", CreatedAt: at, UpdatedAt: at}
		return order, st.CreateAccountCheckoutOrder(ctx, &order, at)
	}
	pay := func(order model.CheckoutOrder, at time.Time) store.BillingEventResult {
		t.Helper()
		seq++
		result, err := st.ApplyBillingEvent(ctx, store.BillingEvent{WebhookID: fmt.Sprintf("wh_%d", seq), Type: "payment.succeeded",
			Kind: store.BillingEventPaymentSucceeded, OrderID: order.ID, TenantID: order.TenantID, PaymentID: "pay_" + order.ID,
			AmountMinor: order.AmountMinor, Currency: "USD"}, at)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	lease := func() model.AccountSale {
		t.Helper()
		rows, err := st.ListAccountSales(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == sale.ID {
				return row
			}
		}
		t.Fatal("account missing", rows)
		return model.AccountSale{}
	}

	// Concurrent checkouts: one tenant's pending order holds the account.
	type result struct {
		order model.CheckoutOrder
		err   error
	}
	results := make(chan result, 8)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			mu.Lock()
			seq++
			id := fmt.Sprintf("ord_c%d", seq)
			mu.Unlock()
			order := model.CheckoutOrder{ID: id, TenantID: fmt.Sprintf("tenant-%d", i), AccountID: sale.ID, DodoProductID: "pdt",
				AmountMinor: 2000, Currency: "USD", Status: model.OrderStatusPending, CreatedBy: "owner", CreatedAt: now, UpdatedAt: now}
			results <- result{order, st.CreateAccountCheckoutOrder(ctx, &order, now)}
		})
	}
	wg.Wait()
	close(results)
	var winner model.CheckoutOrder
	wins := 0
	for r := range results {
		switch {
		case r.err == nil:
			winner, wins = r.order, wins+1
		case !errors.Is(r.err, store.ErrConflict):
			t.Fatal(r.err)
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent checkout winners=%d", wins)
	}
	if _, err := open(winner.TenantID, 1999, now); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale price", err)
	}
	if got := pay(winner, now); got.Outcome != store.BillingOutcomeApplied || !got.AccessChanged {
		t.Fatal(got)
	}
	first := lease()
	if first.TenantID != winner.TenantID || !first.ExpiresAt.Equal(accountaccess.NextMonth(now)) {
		t.Fatal("grant", first)
	}
	if row, err := st.GetTenantBilling(ctx, winner.TenantID); err != nil || row.Mode != model.BillingModeAccountMonthly || row.BalanceMicro != 0 {
		t.Fatal("tenant billing", row, err)
	}
	if got := pay(winner, now); got.Outcome != store.BillingOutcomeDuplicate || !lease().ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatal("replayed payment", got)
	}
	if _, err := open("intruder", 2000, now); !errors.Is(err, store.ErrConflict) {
		t.Fatal("occupied account checked out", err)
	}
	// Renewal extends from the current expiry, across a short month.
	renewal, err := open(winner.TenantID, 2000, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	pay(renewal, now.Add(time.Hour))
	if got := lease().ExpiresAt.UTC().Format(time.RFC3339); got != "2026-03-28T12:00:00Z" {
		t.Fatal("renewal expiry", got)
	}

	// After expiry: a failed checkout releases its hold; an abandoned one
	// holds only until AccountCheckoutHold passes.
	later := lease().ExpiresAt.Add(time.Minute)
	failed, err := open("two", 2000, later)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FailCheckoutOrder(ctx, failed.ID, later); err != nil {
		t.Fatal(err)
	}
	abandoned, err := open("three", 2000, later)
	if err != nil {
		t.Fatal("failed checkout kept its hold", err)
	}
	if _, err := open("four", 2000, later.Add(model.AccountCheckoutHold-time.Second)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("hold expired early", err)
	}
	afterHold := later.Add(model.AccountCheckoutHold + time.Second)
	taken, err := open("four", 2000, afterHold)
	if err != nil {
		t.Fatal(err)
	}
	pay(taken, afterHold)
	// The abandoned checkout is paid late: kept as paid, not granted.
	if got := pay(abandoned, afterHold); got.Outcome != store.BillingOutcomeMismatch {
		t.Fatal("late conflicting payment", got)
	}
	if lease().TenantID != "four" {
		t.Fatal("late payment took the account", lease())
	}
	orders, err := st.ListCheckoutOrders(ctx, store.ListQuery{Limit: 100}, store.CheckoutOrderFilter{TenantID: "three"})
	if err != nil || len(orders.Items) != 1 || orders.Items[0].Status != model.OrderStatusPaid || orders.Items[0].AccountExpiresAt != nil {
		t.Fatal("ungranted order", orders.Items, err)
	}

	// Archiving keeps the lease and stops new checkouts, even the holder's.
	archived := lease()
	archived.Active, archived.UpdatedAt = false, afterHold
	if err := st.SaveAccountSale(ctx, archived, false); err != nil {
		t.Fatal(err)
	}
	if got := lease(); got.TenantID != "four" || got.Active {
		t.Fatal("archive lost lease", got)
	}
	if _, err := open("four", 2000, afterHold); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("archived account checked out", err)
	}
}

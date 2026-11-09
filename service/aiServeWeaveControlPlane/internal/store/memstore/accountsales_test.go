package memstore_test

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAccountPurchaseAtomicExclusiveRenewal(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	now := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)
	sale := model.AccountSale{ID: "account", NodeID: "agent", RuntimeID: "claude-a", Name: "A", PriceMicro: 100, Active: true}
	if err := st.SaveAccountSale(ctx, sale, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if err := st.InitTenantBilling(ctx, model.TenantBilling{TenantID: id, Mode: model.BillingModePrepaid, BalanceMicro: 500}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	type result struct {
		tenant  string
		receipt model.AccountPurchase
		err     error
	}
	results := make(chan result, 2)
	for i, id := range []string{"one", "two"} {
		wg.Go(func() {
			receipt, err := st.BuyAccount(ctx, "account", id, "owner", fmt.Sprintf("%032d", i), 100, now)
			results <- result{id, receipt, err}
		})
	}
	wg.Wait()
	close(results)
	var winner result
	wins := 0
	for r := range results {
		if r.err == nil {
			winner = r
			wins++
		} else if !errors.Is(r.err, store.ErrConflict) {
			t.Fatal(r.err)
		}
	}
	if wins != 1 {
		t.Fatalf("sold %d times", wins)
	}
	if winner.receipt.ExpiresAt.Format(time.RFC3339) != "2026-02-28T10:00:00Z" {
		t.Fatal(winner.receipt)
	}
	duplicate, err := st.BuyAccount(ctx, "account", winner.tenant, "owner", winner.receipt.ID, 100, now)
	if err != nil || duplicate != winner.receipt {
		t.Fatalf("replay: %+v %v", duplicate, err)
	}
	b, _ := st.GetTenantBilling(ctx, winner.tenant)
	if b.BalanceMicro != 400 || b.Mode != model.BillingModeAccountMonthly {
		t.Fatalf("balance/mode: %+v", b)
	}
	renewal, err := st.BuyAccount(ctx, "account", winner.tenant, "owner", "renewal", 100, now.Add(time.Hour))
	if err != nil || renewal.ExpiresAt.Format(time.RFC3339) != "2026-03-28T10:00:00Z" {
		t.Fatalf("renewal: %+v %v", renewal, err)
	}
	if _, err := st.BuyAccount(ctx, "account", winner.tenant, "owner", "stale-price", 99, now); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	b, _ = st.GetTenantBilling(ctx, winner.tenant)
	if b.BalanceMicro != 300 {
		t.Fatalf("failed purchase debited: %+v", b)
	}
	// Editing or archiving must retain ownership and the paid expiry.
	sale.Name = "renamed"
	sale.Active = false
	if err := st.SaveAccountSale(ctx, sale, false); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.ListAccountSales(ctx)
	if rows[0].TenantID != winner.tenant || !rows[0].ExpiresAt.Equal(renewal.ExpiresAt) {
		t.Fatal("edit lost paid access")
	}
	sale.RuntimeID = "different"
	if err := st.SaveAccountSale(ctx, sale, false); !errors.Is(err, store.ErrConflict) {
		t.Fatal("target was mutable")
	}
}

func TestAccountPurchaseInsufficientBalanceAndReallocation(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	row := model.AccountSale{ID: "a", NodeID: "n", RuntimeID: "r", Name: "A", PriceMicro: 100, Active: true, TenantID: "old", ExpiresAt: now}
	if err := st.SaveAccountSale(ctx, row, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BuyAccount(ctx, "a", "new", "owner", "intent", 100, now); !errors.Is(err, store.ErrInsufficientCredit) {
		t.Fatal(err)
	}
	rows, _ := st.ListAccountSales(ctx)
	if rows[0].TenantID != "old" {
		t.Fatal("failed payment changed owner")
	}
	if err := st.InitTenantBilling(ctx, model.TenantBilling{TenantID: "new", BalanceMicro: 100, Mode: model.BillingModeExempt}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BuyAccount(ctx, "a", "new", "owner", "intent", 100, now); err != nil {
		t.Fatal(err)
	}
	rows, _ = st.ListAccountSales(ctx)
	if rows[0].TenantID != "new" {
		t.Fatal("expired account not reallocated")
	}
	if _, err := st.BuyAccount(ctx, "a", "intruder", "owner", "intent", 100, now); !errors.Is(err, store.ErrConflict) {
		t.Fatal("idempotency key crossed tenant boundary")
	}
}

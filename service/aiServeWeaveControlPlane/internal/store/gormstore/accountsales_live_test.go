package gormstore_test

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

// Both engines use an isolated database through the existing migration helper.
func TestLiveAccountPurchases(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		sale := model.AccountSale{ID: "account", NodeID: "agent", RuntimeID: "claude-a", Name: "A", PriceMicro: 100, Active: true, ExpiresAt: time.Unix(0, 0).UTC(), UpdatedAt: now}
		if err := st.SaveAccountSale(ctx, sale, true); err != nil {
			t.Fatal(err)
		}
		duplicate := sale
		duplicate.ID = "duplicate"
		if err := st.SaveAccountSale(ctx, duplicate, true); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("target uniqueness: %v", err)
		}
		for _, id := range []string{"one", "two"} {
			if err := st.InitTenantBilling(ctx, model.TenantBilling{TenantID: id, Mode: model.BillingModePrepaid, BalanceMicro: 200, CreatedAt: now, UpdatedAt: now}, nil); err != nil {
				t.Fatal(err)
			}
		}
		type result struct {
			tenant  string
			receipt model.AccountPurchase
			err     error
		}
		var wg sync.WaitGroup
		results := make(chan result, 2)
		for i, id := range []string{"one", "two"} {
			wg.Go(func() {
				receipt, err := st.BuyAccount(ctx, sale.ID, id, "owner", fmt.Sprintf("%032d", i), 100, now)
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
			t.Fatalf("concurrent sale winners=%d", wins)
		}
		before, _ := st.GetTenantBilling(ctx, winner.tenant)
		replay, err := st.BuyAccount(ctx, sale.ID, winner.tenant, "owner", winner.receipt.ID, 100, now)
		if err != nil || !replay.ExpiresAt.Equal(winner.receipt.ExpiresAt) {
			t.Fatal("replay", replay, err)
		}
		after, _ := st.GetTenantBilling(ctx, winner.tenant)
		if before.BalanceMicro != 100 || after.BalanceMicro != 100 || after.Mode != model.BillingModeAccountMonthly {
			t.Fatal("duplicate debit", before, after)
		}
		if _, err := st.BuyAccount(ctx, sale.ID, winner.tenant, "owner", "renewal", 100, now); err != nil {
			t.Fatal(err)
		}
		if _, err := st.BuyAccount(ctx, sale.ID, winner.tenant, "owner", "unfunded", 100, now); !errors.Is(err, store.ErrInsufficientCredit) {
			t.Fatal(err)
		}
		var count int64
		if err := db.Model(&model.AccountPurchase{}).Count(&count).Error; err != nil || count != 2 {
			t.Fatalf("receipt count %d: %v", count, err)
		}
		sale.Active = false
		sale.UpdatedAt = now.Add(time.Second)
		if err := st.SaveAccountSale(ctx, sale, false); err != nil {
			t.Fatal(err)
		}
		rows, err := st.ListAccountSales(ctx)
		if err != nil || len(rows) != 1 || rows[0].TenantID != winner.tenant || rows[0].ExpiresAt.Format(time.RFC3339) != "2026-03-28T12:00:00Z" {
			t.Fatal("archive lost lease", rows, err)
		}
		if _, err := st.BuyAccount(ctx, sale.ID, winner.tenant, "owner", "archived", 100, now); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		if generation, _, err := st.RevocationOutboxLag(ctx); err != nil || generation < 4 {
			t.Fatal("missing durable invalidation", generation, err)
		}
	})
}

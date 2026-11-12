package gormstore_test

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
	"context"
	"errors"
	"gorm.io/gorm"
	"testing"
	"time"
)

// Both engines use an isolated database through the existing migration helper.
func TestLiveAccountCheckouts(t *testing.T) {
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
		sale := model.AccountSale{ID: "account", NodeID: "agent", RuntimeID: "claude-a", Name: "A", PriceMinor: 100, Currency: "USD", DodoProductID: "pdt", Active: true, ExpiresAt: time.Unix(0, 0).UTC(), UpdatedAt: now}
		if err := st.SaveAccountSale(ctx, sale, true); err != nil {
			t.Fatal(err)
		}
		duplicate := sale
		duplicate.ID = "duplicate"
		if err := st.SaveAccountSale(ctx, duplicate, true); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("target uniqueness: %v", err)
		}
		storetest.AccountCheckouts(t, st)
		if generation, _, err := st.RevocationOutboxLag(ctx); err != nil || generation < 4 {
			t.Fatal("missing durable invalidation", generation, err)
		}
	})
}

// Presales share the account scenario's isolated databases.
func TestLivePresaleCheckouts(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		storetest.PresaleCheckouts(t, st)
	})
}

// Alipay redemptions share the account scenario's isolated databases.
func TestLiveAlipayRedemptions(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		storetest.AlipayRedemptions(t, st)
	})
}

// 易支付 orders share the account scenario's isolated databases.
func TestLiveEzfpOrders(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		storetest.EzfpOrders(t, st)
	})
}

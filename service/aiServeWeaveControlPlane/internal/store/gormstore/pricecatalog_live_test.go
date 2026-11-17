package gormstore_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

// TestLivePriceCatalog runs the shared price catalog scenario on both
// engines, then imports the built-in snapshot to check the batched inserts
// and the schema at full size.
//
// TestLivePriceCatalog 在两种数据库上运行共享的价目表场景，然后导入内置快照，检查完整
// 规模下的批量插入与表结构。
func TestLivePriceCatalog(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		storetest.PriceCatalog(t, st)
	})
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		snap, err := pricecatalog.Parse(pricecatalog.Embedded())
		if err != nil {
			t.Fatal(err)
		}
		result, err := st.ApplyCatalogSnapshot(ctx, snap, store.CatalogApplyOptions{Source: pricecatalog.EmbeddedLabel, Embedded: true, MaxChangePercent: 30}, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
		if err != nil || len(result.Added) != len(snap.Entries) {
			t.Fatalf("import = %d added of %d, %v", len(result.Added), len(snap.Entries), err)
		}
		set, err := st.CurrentCatalogPrices(ctx)
		if err != nil || len(set.Prices) != len(snap.Entries) || set.Version != 1 {
			t.Fatalf("current prices = %d entries, version %d, %v", len(set.Prices), set.Version, err)
		}
	})
}

package gormstore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

func TestLivePricingConfig(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		// The migration seeds the version row.
		var state model.PricingConfigState
		if err := db.Where("id = ?", model.PricingConfigStateID).Take(&state).Error; err != nil || state.Version != 0 {
			t.Fatal("seeded version row", state, err)
		}
		storetest.PricingConfig(t, st)

		// A document larger than MySQL's TEXT (64 KiB) must fit.
		latest, err := st.LatestPricingRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		big := `{"v":1,"pad":"` + strings.Repeat("x", 100_000) + `"}`
		rev, err := st.AppendPricingRevision(ctx, store.AppendPricingRevision{
			BaseRevisionID: latest.ID, Document: big, CreatedBy: "operator", Summary: "big",
			Now: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatal("large document", err)
		}
		if got, err := st.GetPricingRevision(ctx, rev.ID); err != nil || got.Document != big {
			t.Fatal("large document round trip", len(got.Document), err)
		}
	})
}

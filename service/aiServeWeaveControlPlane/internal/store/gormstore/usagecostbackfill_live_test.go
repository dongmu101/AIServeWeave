package gormstore_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

// TestLiveBackfillUsageCost checks that the backfill copies pre-upgrade
// costs from usage_charges, leaves records without a charge row or with a
// price source alone, and can run again without changing anything.
func TestLiveBackfillUsageCost(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
		records := []model.UsageRecord{
			{ID: "req_old_charged", TenantID: "tnt_b", Model: "m", Endpoint: "chat", PromptTokens: 1, TotalTokens: 1, CreatedAt: at},
			{ID: "req_old_exempt", TenantID: "tnt_b", Model: "m", Endpoint: "chat", PromptTokens: 1, TotalTokens: 1, CreatedAt: at.Add(time.Second)},
			{ID: "req_no_charge", TenantID: "tnt_b", Model: "m", Endpoint: "chat", PromptTokens: 1, TotalTokens: 1, CreatedAt: at.AddDate(0, 1, 0)},
			{ID: "req_priced", TenantID: "tnt_b", Model: "m", Endpoint: "chat", PromptTokens: 1, TotalTokens: 1, CreatedAt: at.AddDate(0, 1, 0),
				CostMicro: 7, BaseCostMicro: 7, RatePPM: pricing.RateOne, PriceSource: model.PriceSourceCatalog},
		}
		if err := st.CreateUsageRecords(ctx, records); err != nil {
			t.Fatal(err)
		}
		for _, c := range []model.UsageCharge{
			{RequestID: "req_old_charged", TenantID: "tnt_b", Model: "m", CostMicro: 120, Charged: true, CreatedAt: at},
			{RequestID: "req_old_exempt", TenantID: "tnt_b", Model: "m", CostMicro: 50, Charged: false, CreatedAt: at},
			{RequestID: "req_priced", TenantID: "tnt_b", Model: "m", CostMicro: 999, Charged: true, CreatedAt: at},
		} {
			if err := db.Create(&c).Error; err != nil {
				t.Fatal(err)
			}
		}
		for run := 0; run < 2; run++ {
			updated, err := st.BackfillUsageCost(ctx, nil)
			want := int64(2)
			if run == 1 {
				want = 0
			}
			if err != nil || updated != want {
				t.Fatalf("run %d: updated %d (%v), want %d", run, updated, err, want)
			}
		}
		read := func(id string) model.UsageRecord {
			var r model.UsageRecord
			if err := db.Where("id = ?", id).Take(&r).Error; err != nil {
				t.Fatal(err)
			}
			return r
		}
		if r := read("req_old_charged"); r.CostMicro != 120 || r.BaseCostMicro != 120 || r.Charged == nil || !*r.Charged ||
			r.PriceSource != model.PriceSourceLegacy || r.RatePPM != pricing.RateOne {
			t.Errorf("charged record = %+v", r)
		}
		if r := read("req_old_exempt"); r.CostMicro != 50 || r.Charged == nil || *r.Charged {
			t.Errorf("exempt record = %+v", r)
		}
		if r := read("req_no_charge"); r.PriceSource != "" || r.Charged != nil {
			t.Errorf("record without a charge row = %+v, want it untouched", r)
		}
		if r := read("req_priced"); r.CostMicro != 7 || r.PriceSource != model.PriceSourceCatalog {
			t.Errorf("already priced record = %+v, want it untouched", r)
		}
	})
}

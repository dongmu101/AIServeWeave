package gormstore_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

// TestLiveUsageLedger runs the shared usage ledger scenario on both
// databases, then checks a tenant from before the upgrade: no
// last_ledger_at yet, so sealing reads the latest entry's time from the
// ledger and still comes after it.
func TestLiveUsageLedger(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		storetest.UsageLedger(t, st)

		const tenant = "tnt_legacy"
		at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		late := at.Add(48 * time.Hour)
		if err := db.Create(&model.TenantBilling{TenantID: tenant, Mode: model.BillingModePrepaid, BalanceMicro: 900, CreatedAt: at, UpdatedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
		for _, e := range []model.CreditLedgerEntry{
			{ID: model.NewID(model.PrefixCreditLedger), TenantID: tenant, Kind: model.LedgerKindTopup, AmountMicro: 1000, BalanceAfterMicro: 1000, Ref: "old", CreatedAt: at},
			{ID: model.NewID(model.PrefixCreditLedger), TenantID: tenant, Kind: model.LedgerKindUsage, AmountMicro: -100, BalanceAfterMicro: 900, Ref: "1 requests", CreatedAt: late},
		} {
			if err := db.Create(&e).Error; err != nil {
				t.Fatal(err)
			}
		}
		// A charge stamped before the old entry still seals after it.
		// 时间早于旧账目的扣费，封账后仍排在它之后。
		rec := model.UsageRecord{ID: "req_legacy", TenantID: tenant, Model: "m", Endpoint: "chat", PromptTokens: 1, TotalTokens: 1, CreatedAt: at, CostMicro: 40}
		if _, err := st.ChargeUsage(ctx, []model.UsageRecord{rec}, at.Add(time.Hour), at.Add(2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if sealed, err := st.SealDueUsage(ctx, at.Add(3*time.Hour)); err != nil || sealed != 1 {
			t.Fatalf("seal = %d, %v", sealed, err)
		}
		entries := storetest.CheckLedgerChain(t, st, tenant)
		if len(entries) != 3 || !entries[2].CreatedAt.After(late) || entries[2].AmountMicro != -40 {
			t.Fatalf("ledger = %+v, want the sealed usage after the old entries", entries)
		}
	})
}

package gormstore_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

// TestLiveCompactUsageLedger seeds a pre-upgrade ledger of per-batch usage
// entries and checks that compaction merges same-day runs in the ledger's
// time zone, splits them at any other entry and at midnight, keeps the
// balance chain, and changes nothing when run again.
func TestLiveCompactUsageLedger(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		taipei, err := time.LoadLocation("Asia/Taipei")
		if err != nil {
			t.Fatal(err)
		}
		const tenant = "tnt_compact"
		// 2026-11-15 16:00 UTC is midnight in Taipei (UTC+8).
		midnight := time.Date(2026, 11, 15, 16, 0, 0, 0, time.UTC)
		balance := int64(0)
		var seq int
		add := func(kind string, amount int64, at time.Time) {
			t.Helper()
			seq++
			balance += amount
			ref := "x"
			if kind == model.LedgerKindUsage {
				ref = "2 requests"
			}
			e := model.CreditLedgerEntry{ID: model.NewID(model.PrefixCreditLedger), TenantID: tenant, Kind: kind, AmountMicro: amount, BalanceAfterMicro: balance, Ref: ref, CreatedAt: at}
			if err := db.Create(&e).Error; err != nil {
				t.Fatal(err)
			}
		}
		add(model.LedgerKindTopup, 10_000, midnight.Add(-10*time.Hour))
		add(model.LedgerKindUsage, -10, midnight.Add(-3*time.Hour))
		add(model.LedgerKindUsage, -20, midnight.Add(-2*time.Hour))
		add(model.LedgerKindUsage, -30, midnight.Add(-time.Hour)) // run 1: three entries / 第一串：三条
		add(model.LedgerKindAdjust, 5, midnight.Add(-50*time.Minute))
		add(model.LedgerKindUsage, -40, midnight.Add(-40*time.Minute)) // alone after the adjust / 调整之后单独一条
		add(model.LedgerKindUsage, -1, midnight.Add(time.Minute))
		add(model.LedgerKindUsage, -2, midnight.Add(2*time.Minute)) // next day: run 2 / 次日：第二串
		if err := db.Create(&model.TenantBilling{TenantID: tenant, Mode: model.BillingModePrepaid, BalanceMicro: balance, CreatedAt: midnight, UpdatedAt: midnight}).Error; err != nil {
			t.Fatal(err)
		}

		for run := 0; run < 2; run++ {
			merged, err := st.CompactUsageLedger(ctx, taipei, nil)
			want := int64(3)
			if run == 1 {
				want = 0
			}
			if err != nil || merged != want {
				t.Fatalf("run %d: merged %d (%v), want %d", run, merged, err, want)
			}
		}
		entries := storetest.CheckLedgerChain(t, st, tenant)
		got := ""
		for _, e := range entries {
			got += e.Kind + ":" + itoaLive(e.AmountMicro) + " "
		}
		if want := "topup:10000 usage:-60 adjust:5 usage:-40 usage:-3 "; got != want {
			t.Fatalf("ledger = %s, want %s", got, want)
		}
		if first := entries[1]; first.RequestCount != 6 || first.Ref != "6 requests" || first.PeriodStart == nil || first.PeriodEnd == nil ||
			!first.PeriodEnd.Equal(midnight.Add(-time.Hour)) {
			t.Fatalf("merged entry = %+v", first)
		}
	})
}

func itoaLive(n int64) string { return strconv.FormatInt(n, 10) }

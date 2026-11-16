package storetest

import (
	"context"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ChargeUsageStore is what the ChargeUsage scenario needs.
//
// ChargeUsageStore 是 ChargeUsage 场景所需的接口。
type ChargeUsageStore interface {
	store.Billing
	store.UsageRecords
}

// ChargeUsage checks that usage_records keeps charging exactly once
// (.scratch/token-pricing/spec.md「扣费防重并入 usage_records」): a resend is
// neither recorded nor charged again even when its created_at lands in
// another month, only prepaid tenants are charged, every charged batch
// writes one usage ledger entry per tenant, and concurrent pushes of the
// same batch charge once.
//
// ChargeUsage 检查 usage_records 能保证恰好扣费一次（.scratch/token-pricing/spec.md
// 「扣费防重并入 usage_records」）：重发的记录即使 created_at 落在另一个月，也不会再次
// 入账或扣费；只有 prepaid 租户被扣费；每个扣费批次为每个租户写一条用量账目；同一批的
// 并发推送只扣费一次。
func ChargeUsage(t *testing.T, st ChargeUsageStore) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	for _, b := range []model.TenantBilling{
		{TenantID: "tnt_prepaid", Mode: model.BillingModePrepaid, BalanceMicro: 10_000, CreatedAt: t0, UpdatedAt: t0},
		{TenantID: "tnt_exempt", Mode: model.BillingModeExempt, BalanceMicro: 10_000, CreatedAt: t0, UpdatedAt: t0},
		{TenantID: "tnt_small", Mode: model.BillingModePrepaid, BalanceMicro: 50, CreatedAt: t0, UpdatedAt: t0},
	} {
		if err := st.InitTenantBilling(ctx, b, nil); err != nil {
			t.Fatal(err)
		}
	}
	record := func(id, tenant string, cost int64, at time.Time) model.UsageRecord {
		return model.UsageRecord{ID: id, TenantID: tenant, Model: "m", Endpoint: "chat", PromptTokens: 10, TotalTokens: 10, CreatedAt: at, CostMicro: cost}
	}
	balance := func(tenant string) int64 {
		t.Helper()
		b, err := st.GetTenantBilling(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		return b.BalanceMicro
	}
	usageEntries := func(tenant string) int {
		t.Helper()
		page, err := st.ListCreditLedger(ctx, store.ListQuery{Limit: 100}, store.CreditLedgerFilter{TenantID: tenant, Kind: model.LedgerKindUsage})
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Items)
	}
	requests := func(tenant string) int64 {
		t.Helper()
		summary, err := st.SummarizeUsage(ctx, store.UsageRecordFilter{TenantID: tenant})
		if err != nil {
			t.Fatal(err)
		}
		total := int64(0)
		for _, s := range summary {
			total += s.RequestCount
		}
		return total
	}

	// One batch: two prepaid records, an exempt one, a tenant without a
	// billing row, and a zero-cost record.
	// 一批：两条 prepaid 记录、一条 exempt、一个没有计费行的租户，以及一条零费用记录。
	batch := []model.UsageRecord{
		record("req_a", "tnt_prepaid", 300, t0),
		record("req_b", "tnt_prepaid", 200, t0),
		record("req_c", "tnt_exempt", 400, t0),
		record("req_d", "tnt_nobilling", 400, t0),
		record("req_e", "tnt_prepaid", 0, t0),
		record("req_a", "tnt_prepaid", 300, t0), // duplicate inside the batch / 批内重复
	}
	if _, err := st.ChargeUsage(ctx, batch, t0); err != nil {
		t.Fatal(err)
	}
	if got := balance("tnt_prepaid"); got != 9_500 {
		t.Fatalf("prepaid balance = %d, want 9500", got)
	}
	if got := balance("tnt_exempt"); got != 10_000 {
		t.Fatalf("exempt balance = %d, want it untouched", got)
	}
	if usageEntries("tnt_prepaid") != 1 || usageEntries("tnt_exempt") != 0 {
		t.Fatal("a charged batch writes one usage ledger entry for the prepaid tenant only")
	}
	if requests("tnt_prepaid") != 3 || requests("tnt_exempt") != 1 || requests("tnt_nobilling") != 1 {
		t.Fatal("every distinct record is recorded once, charged or not")
	}

	// Resends, one in another month's partition, change nothing.
	// 重发（其中一条落在另一个月的分区）不改变任何东西。
	resend := []model.UsageRecord{record("req_a", "tnt_prepaid", 300, t0), record("req_b", "tnt_prepaid", 200, t0.AddDate(0, 1, 0))}
	if _, err := st.ChargeUsage(ctx, resend, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if balance("tnt_prepaid") != 9_500 || usageEntries("tnt_prepaid") != 1 || requests("tnt_prepaid") != 3 {
		t.Fatal("a resend must be neither recorded nor charged again")
	}

	// Running out of balance reports the blocked change.
	// 余额耗尽时报告拦截状态的变化。
	changed, err := st.ChargeUsage(ctx, []model.UsageRecord{record("req_f", "tnt_small", 80, t0)}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || balance("tnt_small") != -30 {
		t.Fatalf("changed = %t, balance = %d; want a blocked change and -30", changed, balance("tnt_small"))
	}

	// Concurrent pushes of one batch charge it once.
	// 同一批的并发推送只扣费一次。
	same := []model.UsageRecord{record("req_g", "tnt_prepaid", 100, t0), record("req_h", "tnt_prepaid", 100, t0)}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = st.ChargeUsage(ctx, append([]model.UsageRecord(nil), same...), t0)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if balance("tnt_prepaid") != 9_300 || usageEntries("tnt_prepaid") != 2 {
		t.Fatalf("balance = %d after concurrent pushes, want 9300 and one more ledger entry", balance("tnt_prepaid"))
	}
}

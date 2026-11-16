package storetest

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// UsageLedgerStore is what the UsageLedger scenario needs.
//
// UsageLedgerStore 是 UsageLedger 场景所需的接口。
type UsageLedgerStore interface {
	store.Billing
	store.UsageLedger
}

// CheckLedgerChain fails t unless tenant's ledger is a sound balance chain
// (.scratch/token-pricing/spec.md「余额账目」): created_at strictly
// increases, each balance_after is the previous one plus the entry's amount,
// and the latest balance_after less the pending accumulation is the
// balance. It returns the entries oldest first.
//
// CheckLedgerChain 在租户的账目不是健全的余额链时让 t 失败（.scratch/token-pricing/
// spec.md「余额账目」）：created_at 严格递增，每条 balance_after 等于上一条加上本条金额，
// 最新一条的 balance_after 减去尚未封账的累计等于余额。它按从旧到新的顺序返回账目。
func CheckLedgerChain(t *testing.T, st UsageLedgerStore, tenant string) []model.CreditLedgerEntry {
	t.Helper()
	ctx := context.Background()
	var entries []model.CreditLedgerEntry
	query := store.ListQuery{Limit: 200}
	for {
		page, err := st.ListCreditLedger(ctx, query, store.CreditLedgerFilter{TenantID: tenant})
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, page.Items...)
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt.Before(entries[j].CreatedAt) })
	balance := int64(0)
	for i, e := range entries {
		if i > 0 && !e.CreatedAt.After(entries[i-1].CreatedAt) {
			t.Fatalf("ledger of %s: entry %d at %v does not come after %v", tenant, i, e.CreatedAt, entries[i-1].CreatedAt)
		}
		balance += e.AmountMicro
		if e.BalanceAfterMicro != balance {
			t.Fatalf("ledger of %s: entry %d (%s %d) balance_after = %d, want %d", tenant, i, e.Kind, e.AmountMicro, e.BalanceAfterMicro, balance)
		}
	}
	row, err := st.GetTenantBilling(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if balance-row.UsageAccMicro != row.BalanceMicro {
		t.Fatalf("ledger of %s ends at %d with %d pending, but the balance is %d", tenant, balance, row.UsageAccMicro, row.BalanceMicro)
	}
	return entries
}

// UsageLedger checks the usage ledger rules: charges accumulate instead of
// writing entries; any other entry seals the accumulation first; a day's
// accumulation is sealed by the next charge after its deadline or by
// SealDueUsage; entry times strictly increase even at the same moment or
// with a clock running backwards; and concurrent charges, credits and seals
// keep the chain sound.
//
// UsageLedger 检查用量账目规则：扣费改为累计而不写账目；任何其他账目都先封账；一天的
// 累计由期限之后的下一次扣费或 SealDueUsage 封账；账目时间在同一时刻、甚至时钟倒退时
// 也严格递增；并发的扣费、入账与封账保持余额链健全。
func UsageLedger(t *testing.T, st UsageLedgerStore) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	day1End, day2End, day3End := t0.Add(23*time.Hour), t0.Add(47*time.Hour), t0.Add(71*time.Hour)
	const tenant = "tnt_ledger"
	grant := &model.CreditLedgerEntry{ID: model.NewID(model.PrefixCreditLedger), TenantID: tenant, Kind: model.LedgerKindGrant, AmountMicro: 10_000, Ref: "signup", CreatedAt: t0}
	if err := st.InitTenantBilling(ctx, model.TenantBilling{TenantID: tenant, Mode: model.BillingModePrepaid, CreatedAt: t0, UpdatedAt: t0}, grant); err != nil {
		t.Fatal(err)
	}
	n := 0
	charge := func(at, sealAt time.Time, costs ...int64) {
		t.Helper()
		var records []model.UsageRecord
		for _, cost := range costs {
			n++
			records = append(records, model.UsageRecord{ID: "req_ledger_" + itoa(n), TenantID: tenant, Model: "m", Endpoint: "chat", PromptTokens: 1, TotalTokens: 1, CreatedAt: at, CostMicro: cost})
		}
		if _, err := st.ChargeUsage(ctx, records, at, sealAt); err != nil {
			t.Fatal(err)
		}
	}
	adjust := func(at time.Time, amount int64) model.CreditLedgerEntry {
		t.Helper()
		entry, _, err := st.AdjustCredit(ctx, model.CreditLedgerEntry{ID: model.NewID(model.PrefixCreditLedger), TenantID: tenant, Kind: model.LedgerKindAdjust, AmountMicro: amount, Ref: "test", ActorID: "operator", CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return entry
	}
	kinds := func(entries []model.CreditLedgerEntry) string {
		out := ""
		for _, e := range entries {
			out += e.Kind + ":" + itoa64(e.AmountMicro) + " "
		}
		return out
	}

	// Two batches accumulate; nothing but the grant is in the ledger.
	// 两批扣费累计起来；账目里只有赠送那一条。
	charge(t0.Add(time.Hour), day1End, 300, 200)
	charge(t0.Add(2*time.Hour), day1End, 100)
	if entries := CheckLedgerChain(t, st, tenant); len(entries) != 1 {
		t.Fatalf("ledger = %s, want only the grant", kinds(entries))
	}

	// A credit seals the day so far first.
	// 一笔入账会先为当天至今的用量封账。
	adjust(t0.Add(3*time.Hour), 1000)
	entries := CheckLedgerChain(t, st, tenant)
	if got := kinds(entries); got != "grant:10000 usage:-600 adjust:1000 " {
		t.Fatalf("ledger = %s", got)
	}
	usage := entries[1]
	if usage.RequestCount != 3 || usage.PeriodStart == nil || !usage.PeriodStart.Equal(t0.Add(time.Hour)) ||
		usage.PeriodEnd == nil || !usage.PeriodEnd.Equal(t0.Add(2*time.Hour)) || usage.BalanceAfterMicro != 9_400 {
		t.Fatalf("usage entry = %+v", usage)
	}

	// SealDueUsage leaves an accumulation alone before its deadline and
	// seals it after.
	// SealDueUsage 在期限之前不动累计，之后才封账。
	charge(t0.Add(4*time.Hour), day1End, 50)
	if sealed, err := st.SealDueUsage(ctx, day1End.Add(-time.Second)); err != nil || sealed != 0 {
		t.Fatalf("early seal = %d, %v", sealed, err)
	}
	if sealed, err := st.SealDueUsage(ctx, day1End.Add(time.Minute)); err != nil || sealed != 1 {
		t.Fatalf("seal after the deadline = %d, %v", sealed, err)
	}
	if sealed, err := st.SealDueUsage(ctx, day1End.Add(2*time.Minute)); err != nil || sealed != 0 {
		t.Fatalf("second seal = %d, %v; want nothing left", sealed, err)
	}

	// A charge after the deadline seals the previous day before accruing.
	// 期限之后的扣费先为前一天封账，再累计。
	charge(day1End.Add(time.Hour), day2End, 25)
	charge(day2End.Add(time.Hour), day3End, 10)
	entries = CheckLedgerChain(t, st, tenant)
	if got := kinds(entries); got != "grant:10000 usage:-600 adjust:1000 usage:-50 usage:-25 " {
		t.Fatalf("ledger = %s", got)
	}

	// A credit at the very moment of the pending charge, and one dated
	// before the latest entry, still come strictly later.
	// 与尚未封账的扣费同一时刻的入账，以及时间早于最新账目的入账，仍然严格排在其后。
	same := adjust(day2End.Add(time.Hour), 5)
	back := adjust(t0, 5)
	if !back.CreatedAt.After(same.CreatedAt) {
		t.Fatalf("an entry dated in the past = %v, want it after %v", back.CreatedAt, same.CreatedAt)
	}
	CheckLedgerChain(t, st, tenant)

	// Concurrent charges, credits and seals keep the chain sound.
	// 并发的扣费、入账与封账保持余额链健全。
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	base := day3End.Add(time.Hour)
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			at := base.Add(time.Duration(i) * time.Minute)
			rec := model.UsageRecord{ID: "req_ledger_c" + itoa(i), TenantID: tenant, Model: "m", Endpoint: "chat", PromptTokens: 1, TotalTokens: 1, CreatedAt: at, CostMicro: 7}
			if _, err := st.ChargeUsage(ctx, []model.UsageRecord{rec}, at, at.Add(time.Duration(i%2)*time.Minute)); err != nil {
				errs <- err
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			if _, _, err := st.AdjustCredit(ctx, model.CreditLedgerEntry{ID: model.NewID(model.PrefixCreditLedger), TenantID: tenant, Kind: model.LedgerKindAdjust, AmountMicro: 3, Ref: "c", ActorID: "operator", CreatedAt: base}); err != nil {
				errs <- err
			}
		}(i)
		go func() {
			defer wg.Done()
			if _, err := st.SealDueUsage(ctx, base.Add(time.Hour)); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	CheckLedgerChain(t, st, tenant)
}

func itoa(n int) string { return strconv.Itoa(n) }

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

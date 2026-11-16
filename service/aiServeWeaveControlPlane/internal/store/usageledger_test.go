package store_test

import (
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

func TestNextLocalMidnight(t *testing.T) {
	taipei, _ := time.LoadLocation("Asia/Taipei")
	newYork, _ := time.LoadLocation("America/New_York")
	cases := []struct {
		name string
		now  time.Time
		loc  *time.Location
		want time.Time
	}{
		{"UTC", time.Date(2026, 11, 16, 23, 59, 0, 0, time.UTC), nil, time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)},
		{"exactly midnight starts a new day", time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), time.UTC, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)},
		// 2026-11-16 15:59 UTC is 23:59 in Taipei; its day ends at 16:00 UTC.
		{"Taipei", time.Date(2026, 11, 16, 15, 59, 0, 0, time.UTC), taipei, time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC)},
		{"Taipei after its midnight", time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC), taipei, time.Date(2026, 11, 17, 16, 0, 0, 0, time.UTC)},
		// New York leaves DST on 2026-11-01: that day is 25 hours long.
		{"DST end", time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC), newYork, time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC)},
		// and enters it on 2026-03-08: 23 hours.
		{"DST start", time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), newYork, time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := store.NextLocalMidnight(c.now, c.loc); !got.Equal(c.want) {
			t.Errorf("%s: NextLocalMidnight(%v) = %v, want %v", c.name, c.now, got, c.want)
		}
	}
}

func TestNextLedgerTime(t *testing.T) {
	now := time.Date(2026, 11, 16, 8, 0, 0, 123_456_789, time.UTC)
	if got := store.NextLedgerTime(nil, now); !got.Equal(now.Truncate(time.Microsecond)) {
		t.Errorf("first entry = %v, want now at microsecond precision", got)
	}
	earlier := now.Add(-time.Hour)
	if got := store.NextLedgerTime(&earlier, now); !got.Equal(now.Truncate(time.Microsecond)) {
		t.Errorf("after an older entry = %v, want now", got)
	}
	same := now.Truncate(time.Microsecond)
	if got := store.NextLedgerTime(&same, now); !got.Equal(same.Add(time.Microsecond)) {
		t.Errorf("same moment = %v, want one microsecond later", got)
	}
	later := now.Add(time.Hour)
	if got := store.NextLedgerTime(&later, now); !got.Equal(later.Truncate(time.Microsecond).Add(time.Microsecond)) {
		t.Errorf("clock behind the latest entry = %v, want just after it", got)
	}
}

func TestAccrueAndSealUsage(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	sealAt := t0.Add(16 * time.Hour)
	row := model.TenantBilling{TenantID: "tnt", BalanceMicro: 9_500}
	if store.SealUsage(&row, t0) != nil {
		t.Fatal("nothing accumulated, nothing to seal")
	}
	store.AccrueUsage(&row, 300, 2, t0, sealAt)
	store.AccrueUsage(&row, 200, 1, t0.Add(time.Hour), sealAt.Add(24*time.Hour))
	if row.UsageAccMicro != 500 || row.UsageAccCount != 3 || !row.UsageAccSince.Equal(t0) || !row.UsageAccLast.Equal(t0.Add(time.Hour)) || !row.UsageAccUntil.Equal(sealAt) {
		t.Fatalf("accumulation = %+v; the deadline must stay the first one", row)
	}
	if store.SealDue(row, sealAt.Add(-time.Second)) || !store.SealDue(row, sealAt) {
		t.Fatal("SealDue must turn true at the deadline")
	}
	entry := store.SealUsage(&row, sealAt)
	if entry == nil || entry.AmountMicro != -500 || entry.BalanceAfterMicro != 9_500 || entry.RequestCount != 3 ||
		entry.Ref != "3 requests" || !entry.PeriodStart.Equal(t0) || !entry.PeriodEnd.Equal(t0.Add(time.Hour)) || entry.Kind != model.LedgerKindUsage {
		t.Fatalf("sealed entry = %+v", entry)
	}
	if row.UsageAccCount != 0 || row.UsageAccUntil != nil || row.LastLedgerAt == nil || !row.LastLedgerAt.Equal(entry.CreatedAt) {
		t.Fatalf("row after sealing = %+v", row)
	}
}

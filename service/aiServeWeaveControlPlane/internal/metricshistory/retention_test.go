package metricshistory_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/metricshistory"
)

// fakeClock is a minimal runtime.Clock fixed at a known instant — RunOnce
// only ever calls Now(), so NewTimer is never exercised and left unused.
//
// fakeClock 是一个固定在已知时刻的最小 runtime.Clock——RunOnce 只调用 Now()，
// NewTimer 从未被用到，故留空未实现。
type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }
func (c fakeClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	panic("not used by RunOnce")
}

type fakeRetentionStore struct{ deletedBefore []time.Time }

func (f *fakeRetentionStore) DeleteRollupBefore(ctx context.Context, before time.Time) (int64, error) {
	f.deletedBefore = append(f.deletedBefore, before)
	return 3, nil
}

func TestRetentionDeletesOlderThanWindow(t *testing.T) {
	clock := fakeClock{now: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)}
	store := &fakeRetentionStore{}
	r := metricshistory.NewRetention(store, 90*24*time.Hour, clock, nil)

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(store.deletedBefore) != 1 {
		t.Fatalf("DeleteRollupBefore called %d times, want 1", len(store.deletedBefore))
	}
	want := clock.now.Add(-90 * 24 * time.Hour)
	if !store.deletedBefore[0].Equal(want) {
		t.Errorf("DeleteRollupBefore(%v), want %v", store.deletedBefore[0], want)
	}
}

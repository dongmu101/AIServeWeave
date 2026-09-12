package requestlogretention_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/requestlogretention"
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

type fakeStore struct {
	deletedBefore time.Time
	toReturn      int64
	err           error
}

func (f *fakeStore) DeleteRequestLogsBefore(_ context.Context, before time.Time) (int64, error) {
	f.deletedBefore = before
	return f.toReturn, f.err
}

func TestRunOnceDeletesRowsOlderThanRetention(t *testing.T) {
	clock := fakeClock{now: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)}
	store := &fakeStore{toReturn: 3}
	r := requestlogretention.New(store, 30*24*time.Hour, clock, nil)

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	want := clock.now.Add(-30 * 24 * time.Hour)
	if !store.deletedBefore.Equal(want) {
		t.Fatalf("DeleteRequestLogsBefore called with before = %v, want %v", store.deletedBefore, want)
	}
}

func TestRunOnceReturnsTheStoreError(t *testing.T) {
	store := &fakeStore{err: context.DeadlineExceeded}
	r := requestlogretention.New(store, time.Hour, nil, nil)
	if err := r.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() error = nil, want the store's error to propagate")
	}
}

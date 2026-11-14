package responseturnretention_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/responseturnretention"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// fakeClock is a minimal runtime.Clock fixed at a known instant — RunOnce
// only ever calls Now(), so NewTimer is never exercised and left unused.
type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }
func (c fakeClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	panic("not used by RunOnce")
}

type fakeStore struct {
	deletedBefore time.Time
	toReturn      store.RetentionResult
	err           error
}

func (f *fakeStore) DeleteResponseTurnsBefore(_ context.Context, before time.Time) (store.RetentionResult, error) {
	f.deletedBefore = before
	return f.toReturn, f.err
}

func TestRunOnceDeletesRowsOlderThanRetention(t *testing.T) {
	clock := fakeClock{now: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)}
	store := &fakeStore{toReturn: store.RetentionResult{RowsDeleted: 3}}
	r := responseturnretention.New(store, 30*24*time.Hour, clock, nil)

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	want := clock.now.Add(-30 * 24 * time.Hour)
	if !store.deletedBefore.Equal(want) {
		t.Fatalf("DeleteResponseTurnsBefore called with before = %v, want %v", store.deletedBefore, want)
	}
}

func TestRunOnceReturnsTheStoreError(t *testing.T) {
	store := &fakeStore{err: context.DeadlineExceeded}
	r := responseturnretention.New(store, time.Hour, nil, nil)
	if err := r.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() error = nil, want the store's error to propagate")
	}
}

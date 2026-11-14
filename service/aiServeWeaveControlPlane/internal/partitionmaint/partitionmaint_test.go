package partitionmaint_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/partitionmaint"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }
func (c fakeClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	panic("not used")
}

type fakeStore struct {
	mu     sync.Mutex
	calls  []time.Time
	result store.PartitionMaintenanceResult
	err    error
}

func (f *fakeStore) MaintainPartitions(_ context.Context, now time.Time) (store.PartitionMaintenanceResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, now)
	return f.result, f.err
}

func (f *fakeStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestRunOncePassesTheClockAndLogsCreatedPartitions(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	st := &fakeStore{result: store.PartitionMaintenanceResult{Created: 3}}
	m := partitionmaint.New(st, fakeClock{now: now}, slog.New(slog.NewTextHandler(&buf, nil)))
	if err := m.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.calls) != 1 || !st.calls[0].Equal(now) {
		t.Errorf("calls = %v, want one at %v", st.calls, now)
	}
	if !strings.Contains(buf.String(), "partitions_created=3") {
		t.Errorf("log = %q", buf.String())
	}
}

func TestRunOnceTreatsASkippedPassAsSuccess(t *testing.T) {
	var buf bytes.Buffer
	st := &fakeStore{result: store.PartitionMaintenanceResult{Skipped: true}}
	m := partitionmaint.New(st, nil, slog.New(slog.NewTextHandler(&buf, nil)))
	if err := m.RunOnce(context.Background()); err != nil || buf.Len() != 0 {
		t.Errorf("RunOnce = %v, info log %q; want nil and quiet", err, buf.String())
	}
}

func TestRunOnceReturnsTheStoreError(t *testing.T) {
	m := partitionmaint.New(&fakeStore{err: errors.New("boom")}, nil, nil)
	if err := m.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() error = nil, want the store's error")
	}
}

func TestRunKeepsRetryingAfterFailuresAndStopsWithContext(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, nil))
	st := &fakeStore{err: errors.New("lock wait timeout")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		partitionmaint.New(st, nil, logger).Run(ctx, time.Millisecond)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for st.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if st.count() < 3 {
		t.Fatalf("Run made %d passes, want it to keep retrying", st.count())
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(buf.String(), "partition maintenance failed") {
		t.Errorf("log = %q, want failures logged", buf.String())
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

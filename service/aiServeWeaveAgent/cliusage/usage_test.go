package cliusage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

// TestMonitorCachesAndExpires exercises real refreshes without advancing wall time.
// TestMonitorCachesAndExpires 不推进真实时间，验证刷新缓存与过期处理。
func TestMonitorCachesAndExpires(t *testing.T) {
	now := time.Unix(1800000000, 0)
	clock := &usageClock{now: now}
	var calls atomic.Int32
	m := NewMonitor(clock, []Provider{{Kind: runtime.KindCodex, Read: func(context.Context) (Account, error) {
		calls.Add(1)
		return Account{Windows: []Window{{Name: "primary", UsedPercent: 25, ResetsAt: now.Add(time.Hour)}}}, nil
	}}})
	if got := m.Snapshot(); len(got) != 1 || got[0].Status != "pending" {
		t.Fatalf("snapshot=%+v, want one pending account", got)
	}
	m.Refresh(t.Context())
	m.Refresh(t.Context())
	got := m.Snapshot()
	if calls.Load() != 1 || got[0].Status != "ready" || got[0].Windows[0].UsedPercent != 25 {
		t.Fatalf("calls=%d snapshot=%+v, want one query and 25%% used", calls.Load(), got)
	}
	got[0].Windows[0].UsedPercent = 99
	if got := m.Snapshot()[0].Windows[0].UsedPercent; got != 25 {
		t.Fatalf("cached use=%v, want 25 after modifying returned snapshot", got)
	}
	clock.now = now.Add(3 * time.Minute)
	if got := m.Snapshot()[0]; !got.Stale {
		t.Fatalf("snapshot=%+v, want stale after three minutes", got)
	}
	m.Refresh(t.Context())
	if calls.Load() != 2 || m.Snapshot()[0].Stale {
		t.Fatalf("calls=%d snapshot=%+v, want second query and fresh data", calls.Load(), m.Snapshot())
	}
}

// TestMonitorFailuresClearPreviousQuota prevents stale successes appearing current.
// TestMonitorFailuresClearPreviousQuota 防止查询失败后旧额度仍被显示为当前值。
func TestMonitorFailuresClearPreviousQuota(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"login required", ErrUnauthorized, "unauthorized"},
		{"unavailable quota", ErrUnavailable, "unavailable"},
		{"safe failure", errors.New("SECRET raw CLI error"), "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &usageClock{now: time.Unix(1800000000, 0)}
			m := NewMonitor(clock, []Provider{{Kind: runtime.KindClaude, Read: func(context.Context) (Account, error) {
				return Account{Windows: []Window{{UsedPercent: 50}}}, tc.err
			}}})
			m.Refresh(t.Context())
			got := m.Snapshot()[0]
			if got.Status != tc.want || len(got.Windows) != 0 || got.CheckedAt.IsZero() {
				t.Fatalf("snapshot=%+v, want status=%s, no windows and checked timestamp", got, tc.want)
			}
		})
	}
}

type usageClock struct {
	runtime.Clock
	now time.Time
}

func (c *usageClock) Now() time.Time { return c.now }

// TestMonitorCoalescesConcurrentQueries rejects overlapping and duplicate-provider queries.
// TestMonitorCoalescesConcurrentQueries 阻止并发重入及重复供应方查询。
func TestMonitorCoalescesConcurrentQueries(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p := Provider{Kind: runtime.KindCodex, Read: func(context.Context) (Account, error) {
		calls.Add(1)
		close(entered)
		<-release
		return Account{}, ErrUnavailable
	}}
	m := NewMonitor(runtime.NewSystemClock(), []Provider{p, p})
	go func() { defer close(done); m.Refresh(t.Context()) }()
	<-entered
	m.Refresh(t.Context())
	close(release)
	<-done
	if calls.Load() != 1 || len(m.Snapshot()) != 1 {
		t.Fatalf("queries=%d accounts=%d, want one query for one shared CLI account", calls.Load(), len(m.Snapshot()))
	}
}

// TestMonitorExpiredWindowsRemainUnknown waits for no real time and preserves reset semantics.
// TestMonitorExpiredWindowsRemainUnknown 不等待真实时间，验证到期窗口保持未知。
func TestMonitorExpiredWindowsRemainUnknown(t *testing.T) {
	clock := &usageClock{now: time.Unix(1800000000, 0)}
	m := NewMonitor(clock, []Provider{{Kind: runtime.KindCodex, Read: func(context.Context) (Account, error) {
		return Account{Windows: []Window{{UsedPercent: 25, ResetsAt: clock.now.Add(time.Hour)}}}, nil
	}}})
	m.Refresh(t.Context())
	clock.now = clock.now.Add(time.Hour)
	if got := m.Snapshot()[0].Windows[0]; !got.Expired || got.UsedPercent != 25 {
		t.Fatalf("window=%+v, want expired with last reported usage preserved", got)
	}
}

package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/runtimetest"
)

func revisionsOf(t *testing.T, m runtime.Manager, id string) uint64 {
	t.Helper()
	src, ok := m.(runtime.RevisionSource)
	if !ok {
		t.Fatal("the Manager does not implement RevisionSource")
	}
	rev, ok := src.Revisions()[id]
	if !ok || rev == 0 {
		t.Fatalf("Revisions()[%q] = %d, %v, want a non-zero revision", id, rev, ok)
	}
	snaps := m.Snapshot()
	for _, s := range snaps {
		if s.Descriptor.ID == id && s.Revision < rev {
			t.Fatalf("Snapshot().Revision = %d is older than Revisions() = %d", s.Revision, rev)
		}
	}
	return rev
}

// advanceUntil advances the fake clock in step increments until counter has
// reached want. The scheduling goroutine applies call n's result before it
// issues call n+1, so seeing n+1 proves n has been applied.
func advanceUntil(t *testing.T, counter *int32, clock *runtimetest.Clock, step time.Duration, want int32) {
	t.Helper()
	waitForHealthCalls(t, counter, clock, step, want)
}

func TestManagerRevisionIgnoresTimestampAndLatencyOnlyHealthChecks(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	var calls int32
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
		HealthFunc: func(context.Context) (runtime.HealthReport, error) {
			n := atomic.AddInt32(&calls, 1)
			// Latency and CheckedAt differ on every call, as in production.
			return runtime.HealthReport{
				State:     runtime.StateHealthy,
				Latency:   time.Duration(n) * time.Millisecond,
				CheckedAt: clock.Now(),
			}, nil
		},
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())
	cfg := baseCfg("r1")
	cfg.HealthInterval = 10 * time.Millisecond
	if err := m.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	before := revisionsOf(t, m, "r1")
	advanceUntil(t, &calls, clock, cfg.HealthInterval, 5)
	if after := revisionsOf(t, m, "r1"); after != before {
		t.Fatalf("revision moved from %d to %d across health checks that only changed timestamps and latency", before, after)
	}
	if got := m.Snapshot()[0].Health.Latency; got < 4*time.Millisecond {
		t.Fatalf("health latency = %s: the health checks did not run, so the test proves nothing", got)
	}
}

func TestManagerRevisionMovesOnMaterialHealthChange(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	var calls, queue int32
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
		HealthFunc: func(context.Context) (runtime.HealthReport, error) {
			atomic.AddInt32(&calls, 1)
			return runtime.HealthReport{State: runtime.StateHealthy, QueueRunning: int(atomic.LoadInt32(&queue))}, nil
		},
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())
	cfg := baseCfg("r1")
	cfg.HealthInterval = 10 * time.Millisecond
	if err := m.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	before := revisionsOf(t, m, "r1")
	atomic.StoreInt32(&queue, 3)
	n := atomic.LoadInt32(&calls)
	// +2: the call that may already have been in flight, then one that
	// certainly saw the new queue depth and has been applied.
	advanceUntil(t, &calls, clock, cfg.HealthInterval, n+3)
	after := revisionsOf(t, m, "r1")
	if after == before {
		t.Fatal("revision did not move although the reported queue depth changed")
	}
	if got := m.Snapshot()[0].Health.QueueRunning; got != 3 {
		t.Fatalf("QueueRunning = %d, want 3", got)
	}
}

func TestManagerRevisionMovesWhenStateFlipsUnhealthy(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	var calls int32
	var down atomic.Bool
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
		HealthFunc: func(context.Context) (runtime.HealthReport, error) {
			atomic.AddInt32(&calls, 1)
			if down.Load() {
				return runtime.HealthReport{State: runtime.StateUnhealthy}, nil
			}
			return runtime.HealthReport{State: runtime.StateHealthy}, nil
		},
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())
	cfg := baseCfg("r1")
	cfg.HealthInterval = 10 * time.Millisecond
	if err := m.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	before := revisionsOf(t, m, "r1")
	down.Store(true)
	n := atomic.LoadInt32(&calls)
	advanceUntil(t, &calls, clock, cfg.HealthInterval, n+6)
	if m.Snapshot()[0].State != runtime.StateUnhealthy {
		t.Fatal("the instance did not turn unhealthy")
	}
	if after := revisionsOf(t, m, "r1"); after == before {
		t.Fatal("revision did not move although the instance turned unhealthy")
	}
}

func TestManagerRevisionTracksDiscoveryContentNotItsTimestamp(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	var calls int32
	var generation atomic.Int32
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(context.Context) (runtime.Discovery, error) {
			atomic.AddInt32(&calls, 1)
			d := runtime.Discovery{Version: "1.0", DiscoveredAt: clock.Now()}
			d.Models = []runtime.Model{{ID: "base"}}
			if generation.Load() > 0 {
				d.Models = append(d.Models, runtime.Model{ID: "added"})
			}
			return d, nil
		},
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())
	cfg := baseCfg("r1")
	cfg.HealthInterval = time.Hour
	cfg.DiscoveryInterval = 10 * time.Millisecond
	if err := m.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	before := revisionsOf(t, m, "r1")
	n := atomic.LoadInt32(&calls)
	advanceUntil(t, &calls, clock, cfg.DiscoveryInterval, n+3)
	if after := revisionsOf(t, m, "r1"); after != before {
		t.Fatalf("revision moved from %d to %d on a refresh that returned the same models", before, after)
	}

	generation.Store(1)
	n = atomic.LoadInt32(&calls)
	advanceUntil(t, &calls, clock, cfg.DiscoveryInterval, n+3)
	if after := revisionsOf(t, m, "r1"); after == before {
		t.Fatal("revision did not move although the model catalogue changed")
	}
	if got := len(m.Snapshot()[0].Discovery.Models); got != 2 {
		t.Fatalf("models = %d, want 2", got)
	}
}

func TestManagerRevisionNeverRepeatsAcrossReplaceAndReAdd(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())
	if err := m.Add(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}
	seen := map[uint64]string{revisionsOf(t, m, "r1"): "add"}

	if err := m.Replace(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}
	rev := revisionsOf(t, m, "r1")
	if prev, dup := seen[rev]; dup {
		t.Fatalf("revision %d after Replace repeats the one from %s", rev, prev)
	}
	seen[rev] = "replace"

	if err := m.Remove(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}
	rev = revisionsOf(t, m, "r1")
	if prev, dup := seen[rev]; dup {
		t.Fatalf("revision %d after Remove+Add repeats the one from %s", rev, prev)
	}

	if err := m.Add(context.Background(), baseCfg("r2")); err != nil {
		t.Fatal(err)
	}
	revs := m.(runtime.RevisionSource).Revisions()
	if len(revs) != 2 || revs["r1"] == revs["r2"] {
		t.Fatalf("Revisions() = %v, want two distinct entries", revs)
	}
}

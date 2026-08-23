package runtime_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/internal/runtimetest"
)

func testDeps(clock *runtimetest.Clock) runtime.Dependencies {
	d := validDeps()
	d.Clock = clock
	return d
}

func healthyDiscovery() runtime.Discovery {
	return runtime.Discovery{Version: "1.0", DiscoveredAt: time.Now()}
}

func newManagerWithRegistry(t *testing.T, clock *runtimetest.Clock, rt *runtimetest.Runtime) (runtime.Manager, runtime.Registry) {
	t.Helper()
	reg := runtime.NewRegistry()
	if err := reg.Register(runtime.KindVLLM, fakeFactory(rt)); err != nil {
		t.Fatal(err)
	}
	return runtime.NewManager(reg, testDeps(clock)), reg
}

func baseCfg(id string) runtime.Config {
	return runtime.Config{
		ID:                id,
		Kind:              runtime.KindVLLM,
		BaseURL:           "http://example.com",
		HealthInterval:    100 * time.Millisecond,
		DiscoveryInterval: time.Hour, // kept out of the way unless a test wants it
	}
}

func TestManagerAddRegistersHealthyInstance(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	rt := &runtimetest.Runtime{
		DescriptorFunc: func() runtime.Descriptor { return runtime.Descriptor{ID: "r1", Kind: runtime.KindVLLM} },
		DiscoverFunc:   func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())

	if err := m.Add(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}

	snaps := m.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("Snapshot() has %d entries, want 1", len(snaps))
	}
	if snaps[0].State != runtime.StateHealthy {
		t.Fatalf("State = %s, want %s", snaps[0].State, runtime.StateHealthy)
	}
	if snaps[0].Descriptor.ID != "r1" {
		t.Fatalf("Descriptor.ID = %q, want r1", snaps[0].Descriptor.ID)
	}

	got, ok := m.Get("r1")
	if !ok || got != runtime.Runtime(rt) {
		t.Fatalf("Get(\"r1\") = %v, %v, want the registered fake", got, ok)
	}
}

func TestManagerAddProbeFailureDoesNotRegisterAndClosesRuntime(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	probeErr := errors.New("probe failed")
	rt := &runtimetest.Runtime{
		ProbeFunc: func(ctx context.Context) (runtime.ProbeResult, error) { return runtime.ProbeResult{}, probeErr },
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())

	err := m.Add(context.Background(), baseCfg("r1"))
	if !errors.Is(err, probeErr) {
		t.Fatalf("Add() error = %v, want wrapping %v", err, probeErr)
	}
	if len(m.Snapshot()) != 0 {
		t.Fatalf("Snapshot() has entries after a failed Probe: %v", m.Snapshot())
	}
	if rt.CloseCalls() != 1 {
		t.Fatalf("CloseCalls() = %d, want 1 (failed Probe must close the runtime)", rt.CloseCalls())
	}
}

func TestManagerAddDiscoverFailureDoesNotRegisterAndClosesRuntime(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	discoverErr := errors.New("discover failed")
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) { return runtime.Discovery{}, discoverErr },
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())

	err := m.Add(context.Background(), baseCfg("r1"))
	if !errors.Is(err, discoverErr) {
		t.Fatalf("Add() error = %v, want wrapping %v", err, discoverErr)
	}
	if len(m.Snapshot()) != 0 {
		t.Fatal("Snapshot() has entries after a failed Discover")
	}
	if rt.CloseCalls() != 1 {
		t.Fatalf("CloseCalls() = %d, want 1 (failed Discover must close the runtime)", rt.CloseCalls())
	}
}

func TestManagerAddDuplicateIDReturnsError(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	rt := &runtimetest.Runtime{DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil }}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())

	if err := m.Add(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}
	err := m.Add(context.Background(), baseCfg("r1"))
	if !errors.Is(err, runtime.ErrRuntimeIDDuplicated) {
		t.Fatalf("error = %v, want wrapping ErrRuntimeIDDuplicated", err)
	}
}

func TestManagerAddAfterCloseReturnsError(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	rt := &runtimetest.Runtime{DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil }}
	m, _ := newManagerWithRegistry(t, clock, rt)

	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := m.Add(context.Background(), baseCfg("r1"))
	if !errors.Is(err, runtime.ErrRuntimeClosed) {
		t.Fatalf("error = %v, want wrapping ErrRuntimeClosed", err)
	}
}

func TestManagerGetUnknownIDReturnsFalse(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	m, _ := newManagerWithRegistry(t, clock, &runtimetest.Runtime{})
	defer m.Close(context.Background())

	if _, ok := m.Get("missing"); ok {
		t.Fatal("Get() = true for an unregistered id")
	}
}

func TestManagerRemoveClosesRuntimeAndRemovesFromSnapshot(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	rt := &runtimetest.Runtime{DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil }}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())

	if err := m.Add(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}
	if len(m.Snapshot()) != 0 {
		t.Fatal("Snapshot() still has entries after Remove")
	}
	if rt.CloseCalls() != 1 {
		t.Fatalf("CloseCalls() = %d, want 1", rt.CloseCalls())
	}
	if _, ok := m.Get("r1"); ok {
		t.Fatal("Get() = true after Remove")
	}
}

func TestManagerRemoveUnknownIDReturnsError(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	m, _ := newManagerWithRegistry(t, clock, &runtimetest.Runtime{})
	defer m.Close(context.Background())

	err := m.Remove(context.Background(), "missing")
	if !errors.Is(err, runtime.ErrRuntimeNotFound) {
		t.Fatalf("error = %v, want wrapping ErrRuntimeNotFound", err)
	}
}

func TestManagerReplaceSwapsAtomicallyThenClosesOld(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	var trace []string
	var traceMu sync.Mutex
	record := func(s string) {
		traceMu.Lock()
		trace = append(trace, s)
		traceMu.Unlock()
	}

	oldRt := &runtimetest.Runtime{
		DescriptorFunc: func() runtime.Descriptor { return runtime.Descriptor{ID: "r1", BaseURL: "old"} },
		DiscoverFunc:   func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
		CloseFunc:      func() error { record("old-closed"); return nil },
	}
	newRt := &runtimetest.Runtime{
		DescriptorFunc: func() runtime.Descriptor { return runtime.Descriptor{ID: "r1", BaseURL: "new"} },
		DiscoverFunc:   func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
	}

	reg := runtime.NewRegistry()
	first := true
	reg.Register(runtime.KindVLLM, func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		if first {
			first = false
			return oldRt, nil
		}
		return newRt, nil
	})
	m := runtime.NewManager(reg, testDeps(clock))
	defer m.Close(context.Background())

	if err := m.Add(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}
	record("registered-old")

	if err := m.Replace(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}
	record("replace-returned")

	traceMu.Lock()
	defer traceMu.Unlock()
	// The old runtime must not be closed until after the new one has
	// already taken its place, so a concurrent Get/Snapshot never sees a
	// window with neither instance registered.
	oldClosedIdx, replaceReturnedIdx := -1, -1
	for i, e := range trace {
		if e == "old-closed" {
			oldClosedIdx = i
		}
		if e == "replace-returned" {
			replaceReturnedIdx = i
		}
	}
	if oldClosedIdx == -1 || replaceReturnedIdx == -1 || oldClosedIdx >= replaceReturnedIdx {
		t.Fatalf("unexpected event order: %v", trace)
	}

	got, _ := m.Get("r1")
	if got != runtime.Runtime(newRt) {
		t.Fatal("Get() after Replace does not return the new runtime")
	}
	snaps := m.Snapshot()
	if len(snaps) != 1 || snaps[0].Descriptor.BaseURL != "new" {
		t.Fatalf("Snapshot() = %+v, want exactly one entry from the new runtime", snaps)
	}
}

func TestManagerHealthTransitionsToUnhealthyAfterThreeFailures(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	var healthCalls int32
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
		HealthFunc: func(ctx context.Context) (runtime.HealthReport, error) {
			atomic.AddInt32(&healthCalls, 1)
			return runtime.HealthReport{}, errors.New("down")
		},
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())

	cfg := baseCfg("r1")
	cfg.HealthInterval = 10 * time.Millisecond
	if err := m.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	waitForHealthCalls(t, &healthCalls, clock, cfg.HealthInterval, 3)

	snaps := m.Snapshot()
	if snaps[0].State != runtime.StateUnhealthy {
		t.Fatalf("State = %s, want %s after 3 consecutive failures", snaps[0].State, runtime.StateUnhealthy)
	}
}

func TestManagerHealthRecoversAfterTwoSuccessesAndRefreshesDiscovery(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	var healthCalls int32
	var discoverCalls int32
	failing := true
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) {
			atomic.AddInt32(&discoverCalls, 1)
			return healthyDiscovery(), nil
		},
		HealthFunc: func(ctx context.Context) (runtime.HealthReport, error) {
			n := atomic.AddInt32(&healthCalls, 1)
			if failing && n <= 3 {
				return runtime.HealthReport{}, errors.New("down")
			}
			failing = false
			return runtime.HealthReport{State: runtime.StateHealthy}, nil
		},
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())

	cfg := baseCfg("r1")
	cfg.HealthInterval = 10 * time.Millisecond
	cfg.DiscoveryInterval = time.Hour
	if err := m.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	initialDiscoverCalls := atomic.LoadInt32(&discoverCalls)

	waitForHealthCalls(t, &healthCalls, clock, cfg.HealthInterval, 5)

	snaps := m.Snapshot()
	if snaps[0].State != runtime.StateHealthy {
		t.Fatalf("State = %s, want %s after 2 consecutive successes", snaps[0].State, runtime.StateHealthy)
	}

	// Recovery arms the Discover timer with NewTimer(0); the fake Clock
	// only fires a timer on the next Advance after it was created, so keep
	// advancing (a real DiscoveryInterval-sized jump would also work) until
	// the refresh has actually happened.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&discoverCalls) <= initialDiscoverCalls {
		clock.Advance(cfg.HealthInterval)
		time.Sleep(time.Millisecond)
		if time.Now().After(deadline) {
			t.Fatal("recovery did not trigger an immediate Discover refresh")
		}
	}
}

func TestManagerHealthChecksNeverOverlap(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	var inFlight int32
	var maxInFlight int32
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
		HealthFunc: func(ctx context.Context) (runtime.HealthReport, error) {
			n := atomic.AddInt32(&inFlight, 1)
			for {
				old := atomic.LoadInt32(&maxInFlight)
				if n <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return runtime.HealthReport{State: runtime.StateHealthy}, nil
		},
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())

	cfg := baseCfg("r1")
	cfg.HealthInterval = time.Millisecond
	if err := m.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 50; i++ {
		clock.Advance(time.Millisecond)
		time.Sleep(time.Millisecond)
	}

	if got := atomic.LoadInt32(&maxInFlight); got > 1 {
		t.Fatalf("max concurrent Health calls = %d, want at most 1", got)
	}
}

func TestManagerCloseStopsSchedulingAndClosesAllInstances(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	rt1 := &runtimetest.Runtime{DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil }}
	reg := runtime.NewRegistry()
	reg.Register(runtime.KindVLLM, fakeFactory(rt1))
	m := runtime.NewManager(reg, testDeps(clock))

	if err := m.Add(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt1.CloseCalls() != 1 {
		t.Fatalf("CloseCalls() = %d, want 1", rt1.CloseCalls())
	}
	if len(m.Snapshot()) != 0 {
		t.Fatal("Snapshot() has entries after Close")
	}
}

func TestManagerCloseCancelsInFlightHealthCheck(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	started := make(chan struct{})
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
		HealthFunc: func(ctx context.Context) (runtime.HealthReport, error) {
			close(started)
			<-ctx.Done()
			return runtime.HealthReport{}, ctx.Err()
		},
	}
	m, _ := newManagerWithRegistry(t, clock, rt)

	cfg := baseCfg("r1")
	cfg.HealthInterval = 10 * time.Millisecond
	cfg.ProbeTimeout = time.Hour // must not be what unblocks Health; Close's cancel must
	if err := m.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	// The scheduling goroutine registers its initial timer asynchronously
	// after Add returns, and the timer itself carries up to 10% jitter, so
	// interleave Advance with real sleeps rather than assuming a fixed
	// number of Advance calls lands after the timer exists.
	deadline := time.Now().Add(time.Second)
	for {
		select {
		case <-started:
			goto healthStarted
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Health was never called")
		}
		clock.Advance(cfg.HealthInterval)
		time.Sleep(time.Millisecond)
	}
healthStarted:

	done := make(chan error, 1)
	go func() { done <- m.Close(context.Background()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return; an in-flight Health check was not cancelled")
	}
}

func TestManagerCloseIsIdempotent(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	m, _ := newManagerWithRegistry(t, clock, &runtimetest.Runtime{})

	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("second Close() = %v, want nil", err)
	}
}

func TestManagerCloseAggregatesMultipleCloseErrors(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	err1 := errors.New("close failed 1")
	err2 := errors.New("close failed 2")
	rt1 := &runtimetest.Runtime{
		DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
		CloseFunc:    func() error { return err1 },
	}
	rt2 := &runtimetest.Runtime{
		DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) { return healthyDiscovery(), nil },
		CloseFunc:    func() error { return err2 },
	}
	reg := runtime.NewRegistry()
	which := 0
	reg.Register(runtime.KindVLLM, func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		which++
		if which == 1 {
			return rt1, nil
		}
		return rt2, nil
	})
	m := runtime.NewManager(reg, testDeps(clock))

	if err := m.Add(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(context.Background(), baseCfg("r2")); err != nil {
		t.Fatal(err)
	}

	err := m.Close(context.Background())
	if !errors.Is(err, err1) || !errors.Is(err, err2) {
		t.Fatalf("Close() error = %v, want it to wrap both %v and %v", err, err1, err2)
	}
}

func TestManagerSnapshotIsolation(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	rt := &runtimetest.Runtime{
		DiscoverFunc: func(ctx context.Context) (runtime.Discovery, error) {
			return runtime.Discovery{
				Warnings: []string{"w1"},
				Capabilities: runtime.CapabilitySet{
					runtime.CapabilityChat: {Capability: runtime.CapabilityChat, Level: runtime.SupportSupported},
				},
			}, nil
		},
	}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())

	if err := m.Add(context.Background(), baseCfg("r1")); err != nil {
		t.Fatal(err)
	}

	snap1 := m.Snapshot()
	snap1[0].Discovery.Warnings[0] = "mutated"
	snap1[0].Discovery.Capabilities[runtime.CapabilityChat] = runtime.CapabilityEvidence{Level: runtime.SupportUnsupported}
	snap1[0].Degraded = append(snap1[0].Degraded, "injected")

	snap2 := m.Snapshot()
	if snap2[0].Discovery.Warnings[0] != "w1" {
		t.Fatalf("mutating one Snapshot's Warnings affected a later Snapshot: %v", snap2[0].Discovery.Warnings)
	}
	if snap2[0].Discovery.Capabilities[runtime.CapabilityChat].Level != runtime.SupportSupported {
		t.Fatalf("mutating one Snapshot's Capabilities affected a later Snapshot: %v", snap2[0].Discovery.Capabilities)
	}
	if len(snap2[0].Degraded) != 1 {
		t.Fatalf("mutating one Snapshot's Degraded affected a later Snapshot: %v", snap2[0].Degraded)
	}
}

// waitForHealthCalls advances the fake clock one HealthInterval at a time,
// giving the manager's scheduling goroutine a chance to run between each
// advance, until at least want Health calls have been observed.
func waitForHealthCalls(t *testing.T, counter *int32, clock *runtimetest.Clock, interval time.Duration, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(counter) < want {
		clock.Advance(interval)
		time.Sleep(time.Millisecond)
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d Health calls, got %d", want, atomic.LoadInt32(counter))
		}
	}
}

package runtime_test

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/runtimetest"
)

// TestManagerUnavailableBackendRemainsVisibleAndRecovers checks deferred initialization.
// TestManagerUnavailableBackendRemainsVisibleAndRecovers 验证未就绪实例可见且能自动恢复。
func TestManagerUnavailableBackendRemainsVisibleAndRecovers(t *testing.T) {
	for _, name := range []string{"probe unavailable", "discovery unavailable"} {
		t.Run(name, func(t *testing.T) {
			clock := runtimetest.NewClock(time.Now())
			var phase, discoveries atomic.Int32
			rt := &runtimetest.Runtime{
				DescriptorFunc: func() runtime.Descriptor { return runtime.Descriptor{ID: "r1", Kind: runtime.KindVLLM} },
				ProbeFunc: func(context.Context) (runtime.ProbeResult, error) {
					if phase.Load() == 0 && name == "probe unavailable" {
						return runtime.ProbeResult{}, errors.New("SECRET backend unavailable")
					}
					return runtime.ProbeResult{Kind: runtime.KindVLLM, IdentityVerified: true}, nil
				},
				DiscoverFunc: func(context.Context) (runtime.Discovery, error) {
					stage := phase.Load()
					discoveries.Add(1)
					result := runtime.Discovery{Models: []runtime.Model{{ID: "ready-model"}}}
					if stage < 2 {
						return result, errors.New("SECRET incomplete discovery")
					}
					return result, nil
				},
			}
			m, _ := newManagerWithRegistry(t, clock, rt)
			defer m.Close(context.Background())
			cfg := baseCfg("r1")
			cfg.AllowUnavailable = true
			if err := m.Add(t.Context(), cfg); err != nil {
				t.Fatalf("Add = %v, want nil for unavailable backend", err)
			}
			snap := m.Snapshot()[0]
			if snap.State != runtime.StateUnhealthy || snap.Health.State != runtime.StateUnhealthy || len(snap.Discovery.Models) != 0 || snap.Probe.IdentityVerified || rt.CloseCalls() != 0 {
				t.Fatalf("snapshot=%+v closes=%d, want unhealthy, no models/evidence, and open runtime", snap, rt.CloseCalls())
			}
			if snap.Health.ErrorSummary == "" || strings.Contains(snap.Health.ErrorSummary, "SECRET") {
				t.Fatalf("summary=%q, want fixed nonempty readiness message", snap.Health.ErrorSummary)
			}
			if err := m.Add(t.Context(), cfg); !errors.Is(err, runtime.ErrRuntimeIDDuplicated) {
				t.Fatalf("duplicate Add = %v, want ErrRuntimeIDDuplicated", err)
			}
			advanceUntil := func(ready func() bool) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for !ready() {
					if time.Now().After(deadline) {
						t.Fatalf("snapshot=%+v, want completed readiness retry", m.Snapshot())
					}
					clock.Advance(cfg.HealthInterval * 2)
					goruntime.Gosched()
				}
			}
			before := discoveries.Load()
			phase.Store(1)
			advanceUntil(func() bool { return discoveries.Load() > before })
			if snap := m.Snapshot()[0]; snap.State != runtime.StateUnhealthy || len(snap.Discovery.Models) != 0 {
				t.Fatalf("snapshot=%+v, want unhealthy until discovery succeeds", snap)
			}
			phase.Store(2)
			advanceUntil(func() bool { return m.Snapshot()[0].State == runtime.StateHealthy })
			snap = m.Snapshot()[0]
			if !snap.Probe.IdentityVerified || len(snap.Discovery.Models) != 1 || snap.Discovery.Models[0].ID != "ready-model" || snap.Health.ErrorSummary != "" {
				t.Fatalf("snapshot=%+v, want verified ready-model and cleared error", snap)
			}
			if err := m.Remove(t.Context(), "r1"); err != nil || rt.CloseCalls() != 1 {
				t.Fatalf("Remove=%v closes=%d, want nil and one close", err, rt.CloseCalls())
			}
		})
	}
}

// TestManagerUnavailablePolicyPreservesStartupErrors rejects invalid configuration and cancellation.
// TestManagerUnavailablePolicyPreservesStartupErrors 保留无效配置和调用方取消的错误。
func TestManagerUnavailablePolicyPreservesStartupErrors(t *testing.T) {
	for _, name := range []string{"invalid config", "factory failure", "canceled caller"} {
		t.Run(name, func(t *testing.T) {
			clock := runtimetest.NewClock(time.Now())
			rt := &runtimetest.Runtime{ProbeFunc: func(ctx context.Context) (runtime.ProbeResult, error) { return runtime.ProbeResult{}, ctx.Err() }}
			m, reg := newManagerWithRegistry(t, clock, rt)
			defer m.Close(context.Background())
			cfg := baseCfg("r1")
			cfg.AllowUnavailable = true
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch name {
			case "invalid config":
				cfg.BaseURL = ""
			case "factory failure":
				cfg.Kind = runtime.KindOllama
				if err := reg.Register(cfg.Kind, func(runtime.Config, runtime.Dependencies) (runtime.Runtime, error) {
					return nil, errors.New("factory rejected config")
				}); err != nil {
					t.Fatal(err)
				}
			case "canceled caller":
				cancel()
			}
			if err := m.Add(ctx, cfg); err == nil || len(m.Snapshot()) != 0 {
				t.Fatalf("Add=%v snapshot=%+v, want error and no registered instance", err, m.Snapshot())
			}
		})
	}
}

// TestManagerCloseCancelsPendingInitialization verifies recovery attempts are owned by Manager.
// TestManagerCloseCancelsPendingInitialization 验证关闭 Manager 会取消正在进行的初始化重试。
func TestManagerCloseCancelsPendingInitialization(t *testing.T) {
	clock := runtimetest.NewClock(time.Now())
	var probes atomic.Int32
	started := make(chan struct{})
	rt := &runtimetest.Runtime{ProbeFunc: func(ctx context.Context) (runtime.ProbeResult, error) {
		if probes.Add(1) == 1 {
			return runtime.ProbeResult{}, errors.New("backend down")
		}
		close(started)
		<-ctx.Done()
		return runtime.ProbeResult{}, ctx.Err()
	}}
	m, _ := newManagerWithRegistry(t, clock, rt)
	defer m.Close(context.Background())
	cfg := baseCfg("r1")
	cfg.AllowUnavailable = true
	if err := m.Add(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for clock.PendingTimers() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("timers=%d, want two scheduled checks", clock.PendingTimers())
		}
		goruntime.Gosched()
	}
	clock.Advance(cfg.HealthInterval * 2)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("no initialization retry started, want one blocked probe")
	}
	if err := m.Close(ctx); err != nil || rt.CloseCalls() != 1 {
		t.Fatalf("Close=%v runtime closes=%d, want nil and one close", err, rt.CloseCalls())
	}
}

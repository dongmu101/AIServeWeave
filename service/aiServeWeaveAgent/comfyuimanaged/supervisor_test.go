package comfyuimanaged

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	aiswruntime "AIServeWeave/common/runtime"

	"AIServeWeave/common/comfyuimanagedstatus"
)

// fakeManager is a minimal in-memory runtime.Manager a test fully controls,
// so Supervisor's Add/Remove/Get behavior can be asserted without a real
// registry or a fake ComfyUI HTTP server.
type fakeManager struct {
	mu          sync.Mutex
	registered  map[string]bool
	addErr      error
	addCalls    int
	removeErr   error
	removeCalls int
}

func newFakeManager() *fakeManager {
	return &fakeManager{registered: map[string]bool{}}
}

func (m *fakeManager) Add(ctx context.Context, cfg aiswruntime.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addCalls++
	if m.addErr != nil {
		return m.addErr
	}
	m.registered[cfg.ID] = true
	return nil
}

func (m *fakeManager) Replace(ctx context.Context, cfg aiswruntime.Config) error {
	return m.Add(ctx, cfg)
}

func (m *fakeManager) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeCalls++
	if m.removeErr != nil {
		return m.removeErr
	}
	delete(m.registered, id)
	return nil
}

func (m *fakeManager) Get(id string) (aiswruntime.Runtime, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.registered[id] {
		return nil, true
	}
	return nil, false
}

func (m *fakeManager) Snapshot() []aiswruntime.Snapshot { return nil }

func (m *fakeManager) Close(ctx context.Context) error { return nil }

func (m *fakeManager) addCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addCalls
}

func (m *fakeManager) removeCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.removeCalls
}

func (m *fakeManager) isRegistered(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.registered[id]
}

func testSupervisor(t *testing.T, clock aiswruntime.Clock, manager aiswruntime.Manager) *Supervisor {
	t.Helper()
	return NewSupervisor(context.Background(), testLauncher(clock), manager, testSpec(), 5*time.Second, clock, slog.New(slog.DiscardHandler))
}

// listenOnTestSpecPort binds a real, empty listener on testSpec()'s port, so
// Supervisor.Start's call into Launcher.WaitReady succeeds on its first
// dial — Start does not fake or skip WaitReady, so exercising Start in a
// test needs something real to dial, the same way WaitReady's own tests use
// a real net.Listen rather than a mock.
func listenOnTestSpecPort(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", testSpec().Port))
	if err != nil {
		t.Fatalf("listen on test spec port: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
}

func TestSupervisorStartRegistersWhenNotAlreadyRegistered(t *testing.T) {
	newFakeDocker(t, fakeDockerConfig{})
	listenOnTestSpecPort(t)
	mgr := newFakeManager()
	sup := testSupervisor(t, newFakeClock(), mgr)

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !mgr.isRegistered(testSpec().ContainerName) {
		t.Errorf("Start() did not register the runtime")
	}
	if got := mgr.addCallCount(); got != 1 {
		t.Errorf("Add called %d times, want 1", got)
	}
}

func TestSupervisorStartSkipsAddWhenAlreadyRegistered(t *testing.T) {
	newFakeDocker(t, fakeDockerConfig{inspectOutput: "running|0"})
	listenOnTestSpecPort(t)
	mgr := newFakeManager()
	mgr.registered[testSpec().ContainerName] = true
	sup := testSupervisor(t, newFakeClock(), mgr)

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := mgr.addCallCount(); got != 0 {
		t.Errorf("Add called %d times, want 0 (already registered)", got)
	}
}

func TestSupervisorStopDeregistersThenStopsContainer(t *testing.T) {
	getCalls := newFakeDocker(t, fakeDockerConfig{inspectOutput: "running|0"})
	mgr := newFakeManager()
	mgr.registered[testSpec().ContainerName] = true
	sup := testSupervisor(t, newFakeClock(), mgr)

	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if mgr.isRegistered(testSpec().ContainerName) {
		t.Errorf("Stop() left the runtime registered")
	}
	if got := mgr.removeCallCount(); got != 1 {
		t.Errorf("Remove called %d times, want 1", got)
	}
	calls := getCalls()
	foundStop := false
	for _, c := range calls {
		if len(c) >= 4 && c[:4] == "stop" {
			foundStop = true
		}
	}
	if !foundStop {
		t.Errorf("Stop() never called docker stop, calls = %v", calls)
	}
}

func TestSupervisorStopToleratesDeregisterError(t *testing.T) {
	newFakeDocker(t, fakeDockerConfig{})
	mgr := newFakeManager()
	mgr.removeErr = errors.New("boom")
	sup := testSupervisor(t, newFakeClock(), mgr)

	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v, want nil (Remove failure is logged, not fatal)", err)
	}
}

func TestSupervisorTriggerRestartStopsThenStarts(t *testing.T) {
	newFakeDocker(t, fakeDockerConfig{inspectOutput: "running|0"})
	listenOnTestSpecPort(t)
	mgr := newFakeManager()
	mgr.registered[testSpec().ContainerName] = true
	sup := testSupervisor(t, newFakeClock(), mgr)

	done := make(chan struct{})
	sup.onIdle = func() { close(done) }
	sup.Trigger(comfyuimanagedstatus.ActionRestart)
	<-done

	// Restart deregisters (Stop) then re-adds (Start), so Add fires once and
	// the runtime ends up registered again.
	if got := mgr.removeCallCount(); got != 1 {
		t.Errorf("Remove called %d times, want 1", got)
	}
	if got := mgr.addCallCount(); got != 1 {
		t.Errorf("Add called %d times, want 1", got)
	}
	if !mgr.isRegistered(testSpec().ContainerName) {
		t.Errorf("Restart did not leave the runtime registered")
	}
}

func TestSupervisorTriggerDropsOverlappingAction(t *testing.T) {
	newFakeDocker(t, fakeDockerConfig{})
	mgr := newFakeManager()
	sup := testSupervisor(t, newFakeClock(), mgr)

	sup.mu.Lock()
	sup.busy = true // simulate an action already in flight
	sup.mu.Unlock()

	sup.Trigger(comfyuimanagedstatus.ActionStart)

	if addCalls := mgr.addCallCount(); addCalls != 0 {
		t.Errorf("Trigger() while busy should be dropped, but Add was called %d times", addCalls)
	}
}

func TestSupervisorSnapshotReflectsDockerState(t *testing.T) {
	tests := []struct {
		name string
		cfg  fakeDockerConfig
		want comfyuimanagedstatus.State
	}{
		{"pending", fakeDockerConfig{}, comfyuimanagedstatus.StatePending},
		{"running", fakeDockerConfig{inspectOutput: "running|0"}, comfyuimanagedstatus.StateRunning},
		{"stopped", fakeDockerConfig{inspectOutput: "exited|0"}, comfyuimanagedstatus.StateStopped},
		{"failed", fakeDockerConfig{inspectOutput: "exited|1"}, comfyuimanagedstatus.StateFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newFakeDocker(t, tt.cfg)
			sup := testSupervisor(t, newFakeClock(), newFakeManager())

			got := sup.Snapshot()
			if len(got) != 1 {
				t.Fatalf("Snapshot() returned %d entries, want 1", len(got))
			}
			if got[0].ContainerName != testSpec().ContainerName {
				t.Errorf("Snapshot() container name = %q, want %q", got[0].ContainerName, testSpec().ContainerName)
			}
			if got[0].State != tt.want {
				t.Errorf("Snapshot() state = %v, want %v", got[0].State, tt.want)
			}
		})
	}
}

package tunneltest

import (
	"context"
	"sync"

	"AIServeWeave/common/runtime"
)

// Manager is a scriptable runtime.Manager. The tunnel only ever reads
// Snapshot and applies Add/Replace/Remove, so this fake records what the
// control plane asked for and replays whatever state a test installed —
// no real runtime, no backend, no network.
type Manager struct {
	mu       sync.Mutex
	snaps    []runtime.Snapshot
	runtimes map[string]runtime.Runtime

	addErr     error
	replaceErr error
	removeErr  error

	adds     []runtime.Config
	replaces []runtime.Config
	removes  []string
}

// NewManager returns a Manager reporting snaps.
func NewManager(snaps ...runtime.Snapshot) *Manager {
	return &Manager{snaps: snaps}
}

// SetSnapshots replaces the reported state, standing in for a health check
// that changed a runtime's state or a discovery that found new models.
func (m *Manager) SetSnapshots(snaps ...runtime.Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snaps = snaps
}

// SetErrors makes the next configuration calls fail, so a test can check that
// a rejected configuration is reported rather than swallowed.
func (m *Manager) SetErrors(add, replace, remove error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addErr, m.replaceErr, m.removeErr = add, replace, remove
}

// Adds, Replaces and Removes return what the control plane asked for.
func (m *Manager) Adds() []runtime.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]runtime.Config(nil), m.adds...)
}

func (m *Manager) Replaces() []runtime.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]runtime.Config(nil), m.replaces...)
}

func (m *Manager) Removes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.removes...)
}

// Add implements runtime.Manager.
func (m *Manager) Add(_ context.Context, cfg runtime.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.adds = append(m.adds, cfg)
	return m.addErr
}

// Replace implements runtime.Manager.
func (m *Manager) Replace(_ context.Context, cfg runtime.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replaces = append(m.replaces, cfg)
	return m.replaceErr
}

// Remove implements runtime.Manager.
func (m *Manager) Remove(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removes = append(m.removes, id)
	return m.removeErr
}

// SetRuntime installs the instance Get returns for id, standing in for a
// runtime the control plane added. Passing nil removes it.
func (m *Manager) SetRuntime(id string, rt runtime.Runtime) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt == nil {
		delete(m.runtimes, id)
		return
	}
	if m.runtimes == nil {
		m.runtimes = map[string]runtime.Runtime{}
	}
	m.runtimes[id] = rt
}

// Get implements runtime.Manager.
func (m *Manager) Get(id string) (runtime.Runtime, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.runtimes[id]
	return rt, ok
}

// Snapshot implements runtime.Manager.
func (m *Manager) Snapshot() []runtime.Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]runtime.Snapshot(nil), m.snaps...)
}

// Close implements runtime.Manager.
func (m *Manager) Close(context.Context) error { return nil }

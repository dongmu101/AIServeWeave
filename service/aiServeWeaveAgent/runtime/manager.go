package runtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Manager owns a set of Runtime instances: it creates them through a
// Registry, validates them with Probe and Discover before they become
// visible, and keeps them refreshed with periodic Health and Discover
// calls. Manager does not select instances by capability or proxy
// inference/workflow requests — that is the upper-layer scheduler's job.
type Manager interface {
	Add(ctx context.Context, cfg Config) error
	Replace(ctx context.Context, cfg Config) error
	Remove(ctx context.Context, id string) error
	Get(id string) (Runtime, bool)
	Snapshot() []Snapshot
	Close(ctx context.Context) error
}

// Snapshot is a single instance's immutable state view: every slice and
// CapabilitySet is deep-copied before being returned, so mutating a
// Snapshot can never affect Manager's internal state or a later Snapshot.
type Snapshot struct {
	Descriptor Descriptor
	State      State
	Probe      ProbeResult
	Health     HealthReport
	Discovery  Discovery
	// Inflight is reserved for request-path concurrency reporting once an
	// upper-layer scheduler wires Limiter acquisitions through Manager; it
	// is always 0 for now, since Manager itself never proxies requests.
	Inflight  int
	Degraded  []string // endpoint degradation notes, e.g. "SGLang missing /health"
	UpdatedAt time.Time
}

const (
	healthFailureThreshold = 3
	healthSuccessThreshold = 2
)

type managedInstance struct {
	mu sync.Mutex

	runtime    Runtime
	cfg        Config
	descriptor Descriptor
	state      State
	probe      ProbeResult
	health     HealthReport
	discovery  Discovery
	degraded   []string
	updatedAt  time.Time

	consecutiveFailures  int
	consecutiveSuccesses int
	closed               bool

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

type manager struct {
	registry Registry
	deps     Dependencies
	clock    Clock

	mu        sync.Mutex
	instances map[string]*managedInstance
	closed    bool
	wg        sync.WaitGroup
}

// NewManager creates a Manager that constructs Runtime instances through
// registry, injecting deps into every Factory call. If deps.Clock is nil,
// NewManager defaults it to the real wall clock.
func NewManager(registry Registry, deps Dependencies) Manager {
	if deps.Clock == nil {
		deps.Clock = NewSystemClock()
	}
	return &manager{
		registry:  registry,
		deps:      deps,
		clock:     deps.Clock,
		instances: make(map[string]*managedInstance),
	}
}

// Add normalizes and validates cfg, creates a Runtime through the Registry,
// and runs Probe then Discover before the instance becomes visible through
// Get/Snapshot. If Probe or Discover fails, the Runtime is closed and never
// registered — Add does not leave a "registering" placeholder behind.
func (m *manager) Add(ctx context.Context, cfg Config) error {
	normalized := cfg.Normalize()
	if err := normalized.Validate(); err != nil {
		return err
	}

	if err := m.reserveID(normalized); err != nil {
		return err
	}

	inst, err := m.createAndValidate(ctx, normalized)
	if err != nil {
		return err
	}

	if err := m.register(normalized.ID, inst); err != nil {
		inst.runtime.Close()
		return err
	}

	m.startScheduling(normalized.ID, inst)
	return nil
}

// reserveID fails fast if cfg.ID is already taken or the Manager is closed,
// before any network I/O happens. Add still re-checks under lock after
// Probe/Discover, since another Add for the same ID could race in between.
func (m *manager) reserveID(cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return closedError(cfg)
	}
	if _, exists := m.instances[cfg.ID]; exists {
		return duplicateIDError(cfg)
	}
	return nil
}

// createAndValidate builds a Runtime and runs Probe then Discover. On any
// failure it closes the Runtime and returns that failure as-is (Probe and
// Discover already return *RuntimeError).
func (m *manager) createAndValidate(ctx context.Context, cfg Config) (*managedInstance, error) {
	rt, err := m.registry.Create(cfg, m.deps)
	if err != nil {
		return nil, err
	}

	probeResult, err := rt.Probe(ctx)
	if err != nil {
		rt.Close()
		return nil, err
	}

	discovery, err := rt.Discover(ctx)
	if err != nil {
		rt.Close()
		return nil, err
	}

	now := m.clock.Now()
	instCtx, cancel := context.WithCancel(context.Background())
	return &managedInstance{
		runtime:    rt,
		cfg:        cfg,
		descriptor: rt.Descriptor(),
		state:      StateHealthy,
		probe:      probeResult,
		health:     HealthReport{State: StateHealthy, CheckedAt: now},
		discovery:  discovery,
		degraded:   append([]string(nil), discovery.Warnings...),
		updatedAt:  now,
		ctx:        instCtx,
		cancel:     cancel,
		done:       make(chan struct{}),
	}, nil
}

// register inserts inst into the instance table, re-checking for a closed
// Manager or a duplicate ID that raced in during Probe/Discover.
func (m *manager) register(id string, inst *managedInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return closedError(inst.cfg)
	}
	if _, exists := m.instances[id]; exists {
		return duplicateIDError(inst.cfg)
	}
	m.instances[id] = inst
	m.wg.Add(1)
	return nil
}

func (m *manager) startScheduling(id string, inst *managedInstance) {
	go m.scheduleLoop(id, inst)
}

// Replace builds and validates a new Runtime exactly like Add, then swaps
// it into the instance table atomically: the new instance is visible to
// Get/Snapshot before the old one is stopped and closed, so a concurrent
// reader never observes neither instance registered.
func (m *manager) Replace(ctx context.Context, cfg Config) error {
	normalized := cfg.Normalize()
	if err := normalized.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return closedError(normalized)
	}

	inst, err := m.createAndValidate(ctx, normalized)
	if err != nil {
		return err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		inst.runtime.Close()
		return closedError(normalized)
	}
	old := m.instances[normalized.ID]
	m.instances[normalized.ID] = inst
	m.wg.Add(1)
	m.mu.Unlock()

	m.startScheduling(normalized.ID, inst)

	if old != nil {
		m.stopInstance(old)
		old.runtime.Close()
	}
	return nil
}

// Remove stops and closes the instance registered under id.
func (m *manager) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	inst, ok := m.instances[id]
	if ok {
		delete(m.instances, id)
	}
	m.mu.Unlock()
	if !ok {
		return &RuntimeError{
			Code:      ErrorInvalidConfig,
			RuntimeID: id,
			Operation: "remove_runtime",
			Message:   fmt.Sprintf("runtime id %q not found", id),
			Cause:     ErrRuntimeNotFound,
		}
	}

	m.stopInstance(inst)
	return inst.runtime.Close()
}

// stopInstance cancels an instance's scheduling context — aborting any
// in-flight Health or Discover call along with it — and waits for its
// scheduling goroutine to exit before returning.
func (m *manager) stopInstance(inst *managedInstance) {
	inst.mu.Lock()
	inst.closed = true
	inst.mu.Unlock()
	inst.cancel()
	<-inst.done
}

// Get returns the Runtime registered under id. The caller type-asserts the
// result to InferenceRuntime or WorkflowRuntime as needed.
func (m *manager) Get(id string) (Runtime, bool) {
	m.mu.Lock()
	inst, ok := m.instances[id]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	return inst.runtime, true
}

// Snapshot returns an immutable view of every registered instance, sorted
// by ID for deterministic output.
func (m *manager) Snapshot() []Snapshot {
	m.mu.Lock()
	insts := make([]*managedInstance, 0, len(m.instances))
	for _, inst := range m.instances {
		insts = append(insts, inst)
	}
	m.mu.Unlock()

	snapshots := make([]Snapshot, 0, len(insts))
	for _, inst := range insts {
		snapshots = append(snapshots, inst.snapshot())
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Descriptor.ID < snapshots[j].Descriptor.ID })
	return snapshots
}

func (inst *managedInstance) snapshot() Snapshot {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return Snapshot{
		Descriptor: inst.descriptor,
		State:      inst.state,
		Probe:      inst.probe,
		Health:     inst.health,
		Discovery:  deepCopyDiscovery(inst.discovery),
		Inflight:   0,
		Degraded:   append([]string(nil), inst.degraded...),
		UpdatedAt:  inst.updatedAt,
	}
}

func deepCopyDiscovery(d Discovery) Discovery {
	cp := d
	if d.Models != nil {
		cp.Models = make([]Model, len(d.Models))
		for i, model := range d.Models {
			cp.Models[i] = Model{ID: model.ID, Capabilities: copyCapabilitySet(model.Capabilities)}
		}
	}
	cp.NodeTypes = append([]string(nil), d.NodeTypes...)
	cp.Capabilities = copyCapabilitySet(d.Capabilities)
	cp.Warnings = append([]string(nil), d.Warnings...)
	return cp
}

func copyCapabilitySet(s CapabilitySet) CapabilitySet {
	if s == nil {
		return nil
	}
	cp := make(CapabilitySet, len(s))
	for k, v := range s {
		cp[k] = v
	}
	return cp
}

// Close stops all scheduling, cancels every in-flight Health/Discover call,
// closes every instance, and aggregates their Close errors. Close is
// idempotent: a second call returns nil without touching already-closed
// instances again.
func (m *manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	insts := make([]*managedInstance, 0, len(m.instances))
	for _, inst := range m.instances {
		insts = append(insts, inst)
	}
	m.instances = make(map[string]*managedInstance)
	m.mu.Unlock()

	for _, inst := range insts {
		inst.mu.Lock()
		inst.closed = true
		inst.mu.Unlock()
		inst.cancel()
	}
	for _, inst := range insts {
		<-inst.done
	}

	var errs []error
	for _, inst := range insts {
		if err := inst.runtime.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// scheduleLoop runs an instance's periodic Health and Discover checks until
// inst.ctx is cancelled (by Remove, Replace or Close). Health and Discover
// calls happen synchronously within this loop, so at most one of each is
// ever in flight for a given instance, and a slow call simply delays when
// the next timer is armed rather than letting ticks queue up.
func (m *manager) scheduleLoop(id string, inst *managedInstance) {
	defer close(inst.done)
	defer m.wg.Done()

	healthCh, healthStop := m.clock.NewTimer(jitteredInterval(inst.cfg.HealthInterval))
	discoverCh, discoverStop := m.clock.NewTimer(inst.cfg.DiscoveryInterval)
	defer healthStop()
	defer discoverStop()

	for {
		select {
		case <-inst.ctx.Done():
			return
		case <-healthCh:
			recovered := m.doHealthCheck(inst)
			healthCh, healthStop = m.clock.NewTimer(jitteredInterval(inst.cfg.HealthInterval))
			if recovered {
				discoverStop()
				discoverCh, discoverStop = m.clock.NewTimer(0)
			}
		case <-discoverCh:
			m.doDiscoverRefresh(inst)
			discoverCh, discoverStop = m.clock.NewTimer(inst.cfg.DiscoveryInterval)
		}
	}
}

// doHealthCheck runs one Health call and applies the failure/recovery
// threshold state machine. It reports whether this call just brought the
// instance back from unhealthy to healthy, so the caller can trigger an
// immediate Discover refresh.
func (m *manager) doHealthCheck(inst *managedInstance) (recovered bool) {
	ctx, cancel := context.WithTimeout(inst.ctx, inst.cfg.ProbeTimeout)
	defer cancel()
	report, err := inst.runtime.Health(ctx)

	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.closed {
		return false
	}

	healthy := err == nil && report.State == StateHealthy
	if healthy {
		inst.consecutiveSuccesses++
		inst.consecutiveFailures = 0
		if inst.state == StateUnhealthy && inst.consecutiveSuccesses >= healthSuccessThreshold {
			inst.state = StateHealthy
			recovered = true
		}
		inst.health = report
	} else {
		inst.consecutiveFailures++
		inst.consecutiveSuccesses = 0
		if inst.state == StateHealthy && inst.consecutiveFailures >= healthFailureThreshold {
			inst.state = StateUnhealthy
		}
		summary := ""
		if err != nil {
			summary = err.Error()
		}
		inst.health = HealthReport{State: inst.state, CheckedAt: m.clock.Now(), ErrorSummary: summary}
	}
	inst.updatedAt = m.clock.Now()
	return recovered
}

func (m *manager) doDiscoverRefresh(inst *managedInstance) {
	timeout := inst.cfg.RequestTimeout
	ctx, cancel := context.WithTimeout(inst.ctx, timeout)
	defer cancel()
	discovery, err := inst.runtime.Discover(ctx)
	if err != nil {
		return
	}

	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.closed {
		return
	}
	inst.discovery = discovery
	inst.degraded = append([]string(nil), discovery.Warnings...)
	inst.updatedAt = m.clock.Now()
}

// jitteredInterval adds up to 10% random jitter to base, so many instances
// on the same interval do not all poll at once. base <= 0 is returned
// unchanged (an explicit "no periodic check" opt-out).
func jitteredInterval(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	maxJitter := base / 10
	if maxJitter <= 0 {
		return base
	}
	return base + time.Duration(rand.Int63n(int64(maxJitter)+1))
}

func closedError(cfg Config) error {
	return &RuntimeError{
		Code:      ErrorClosed,
		RuntimeID: cfg.ID,
		Kind:      cfg.Kind,
		Operation: "manager",
		Message:   "manager is closed",
		Cause:     ErrRuntimeClosed,
	}
}

func duplicateIDError(cfg Config) error {
	return &RuntimeError{
		Code:      ErrorInvalidConfig,
		RuntimeID: cfg.ID,
		Kind:      cfg.Kind,
		Operation: "add_runtime",
		Message:   fmt.Sprintf("runtime id %q is already registered", cfg.ID),
		Cause:     ErrRuntimeIDDuplicated,
	}
}

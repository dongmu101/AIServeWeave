package runtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Manager owns a set of Runtime instances: it creates them through a
// Registry, validates them with Probe and Discover before they become
// visible, and keeps them refreshed with periodic Health and Discover
// calls. Manager does not select instances by capability or proxy
// inference/workflow requests — that is the upper-layer scheduler's job.
// With Config.AllowUnavailable, failed initial checks remain visible as unhealthy.
//
// Manager 通过 Registry 创建并探测运行时，维护周期健康检查和模型发现；不代理或调度请求。
// AllowUnavailable 使初始检查失败的实例保留为未就绪。
type Manager interface {
	Add(ctx context.Context, cfg Config) error
	Replace(ctx context.Context, cfg Config) error
	Remove(ctx context.Context, id string) error
	Get(id string) (Runtime, bool)
	Snapshot() []Snapshot
	Close(ctx context.Context) error
}

// RevisionSource is implemented by a Manager that can report, without copying
// any state, which revision of each instance's material state it currently
// holds. A caller that polls Snapshot to detect changes (the Agent tunnel's
// status report) calls Revisions first and skips the Snapshot copy entirely
// while every revision is unchanged. Revisions is optional: a Manager that
// does not implement it, or reports Revision 0, is simply polled the slow way.
//
// RevisionSource 由能够在不拷贝任何状态的前提下报告各实例当前「实质状态」版本的
// Manager 实现。靠轮询 Snapshot 探测变化的调用方（Agent 隧道的状态上报）先调用
// Revisions，只要所有版本号都没变就完全跳过 Snapshot 的拷贝。该接口可选：未实现，
// 或版本号为 0 的 Manager 按原来的慢路径轮询即可。
type RevisionSource interface {
	// Revisions maps every registered instance id to its current Revision.
	Revisions() map[string]uint64
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

	// Revision identifies the material state this Snapshot carries. It is
	// drawn from one Manager-wide counter, so it never repeats for a given
	// id even across Replace or Remove+Add, and it changes whenever anything
	// other than a timestamp or the health-check latency changes: State,
	// Descriptor, Probe verdict, Health state/queue/error, or Discovery
	// content (including Degraded). Two Snapshots of the same id with equal
	// non-zero Revision therefore differ at most in timestamps and latency.
	// Zero means "unversioned" (a Snapshot not produced by Manager) and
	// must be treated as always possibly changed.
	//
	// Revision 标识该 Snapshot 所携带的实质状态。它取自 Manager 范围内的单一计数器，
	// 因此同一 id 即使经历 Replace 或 Remove+Add 也不会重复；除时间戳与健康检查
	// 延迟之外的任何内容变化（State、Descriptor、Probe 结论、Health 的状态/队列/
	// 错误，或 Discovery 内容，含 Degraded）都会使其改变。同一 id 的两个 Snapshot
	// 若 Revision 相等且非零，则至多只在时间戳与延迟上有差异。零表示「未版本化」
	//（不是 Manager 产出的 Snapshot），必须视为随时可能已变化。
	Revision uint64
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
	// rev is the instance's material-state revision (see Snapshot.Revision).
	// It is only written while holding mu.
	//
	// rev 是实例的实质状态版本（见 Snapshot.Revision），仅在持有 mu 时写入。
	rev uint64

	consecutiveFailures  int
	consecutiveSuccesses int
	closed               bool
	pending              bool
	// notReadyError and notReadyLoggedAt record the last logged reason a
	// pending instance failed initialization; see logNotReady.
	// notReadyError 与 notReadyLoggedAt 记录未就绪实例最近一次记入日志的初始化失败
	// 原因；见 logNotReady。
	notReadyError    string
	notReadyLoggedAt time.Time

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

	// revSeq is the Manager-wide source of instance revisions; see
	// Snapshot.Revision.
	//
	// revSeq 是 Manager 范围内实例版本号的来源，见 Snapshot.Revision。
	revSeq atomic.Uint64
}

// nextRev returns a revision no instance of this Manager has used before.
func (m *manager) nextRev() uint64 { return m.revSeq.Add(1) }

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
// Get/Snapshot. By default, a failed check closes the Runtime without registering it.
// AllowUnavailable retains it as unhealthy and retries full initialization instead.
//
// Add 先校验配置并完成探测与发现；默认失败会关闭实例。
// AllowUnavailable 开启时保留未就绪实例，并定期重试完整初始化。
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
// failure it closes the Runtime unless local startup allows unavailable backends.
//
// createAndValidate 创建并探测运行时；本地允许未就绪时保留实例，其他失败仍关闭。
func (m *manager) createAndValidate(ctx context.Context, cfg Config) (*managedInstance, error) {
	rt, err := m.registry.Create(cfg, m.deps)
	if err != nil {
		return nil, err
	}

	probeResult, discovery, err := initializeRuntime(ctx, rt)
	var runtimeErr *RuntimeError
	invalid := errors.As(err, &runtimeErr) && (runtimeErr.Code == ErrorInvalidConfig || runtimeErr.Code == ErrorClosed)
	if err != nil && (!cfg.AllowUnavailable || ctx.Err() != nil || invalid) {
		rt.Close()
		return nil, err
	}

	now := m.clock.Now()
	state := StateHealthy
	health := HealthReport{State: state, CheckedAt: now}
	if err != nil {
		state = StateUnhealthy
		health.State, health.ErrorSummary = state, "runtime not ready; retrying probe and discovery"
		// Do not publish partial evidence or models before both checks succeed.
		// 两项检查都成功前，不发布部分证据或模型。
		probeResult, discovery = ProbeResult{Kind: cfg.Kind}, Discovery{}
	}
	instCtx, cancel := context.WithCancel(context.Background())
	inst := &managedInstance{
		runtime:    rt,
		cfg:        cfg,
		descriptor: rt.Descriptor(),
		state:      state,
		probe:      probeResult,
		health:     health,
		discovery:  discovery,
		degraded:   append([]string(nil), discovery.Warnings...),
		updatedAt:  now,
		rev:        m.nextRev(),
		ctx:        instCtx,
		cancel:     cancel,
		done:       make(chan struct{}),
		pending:    err != nil,
	}
	if err != nil {
		m.logNotReady(inst, err)
	}
	return inst, nil
}

// notReadyRelogInterval is how often an unchanged initialization failure is
// logged again, so the reason stays in a rotated log without one line per
// retry.
// notReadyRelogInterval 是相同的初始化失败重新记入日志的间隔，使原因留在轮转后的
// 日志里，又不必每次重试都记一行。
const notReadyRelogInterval = 10 * time.Minute

// logNotReady logs why inst could not initialize: the first failure, a
// changed reason, or the same reason again after notReadyRelogInterval. The
// caller holds inst.mu or owns inst exclusively.
//
// logNotReady 记录 inst 无法初始化的原因：首次失败、原因变化，或相同原因在
// notReadyRelogInterval 之后再记一次。调用方须持有 inst.mu 或独占 inst。
func (m *manager) logNotReady(inst *managedInstance, err error) {
	logger := m.deps.Logger
	if logger == nil {
		return
	}
	now, reason := m.clock.Now(), err.Error()
	if reason == inst.notReadyError && now.Sub(inst.notReadyLoggedAt) < notReadyRelogInterval {
		return
	}
	inst.notReadyError, inst.notReadyLoggedAt = reason, now
	args := []any{"runtime_id", inst.cfg.ID, "kind", inst.cfg.Kind, "error", reason, "probe_timeout", inst.cfg.ProbeTimeout}
	if errors.Is(err, &RuntimeError{Code: ErrorTimeout}) || errors.Is(err, context.DeadlineExceeded) {
		args = append(args, "hint", "the backend did not answer within probe_timeout; a slow-starting backend needs a larger probe_timeout")
	}
	logger.Warn("runtime not ready; retrying / 运行时未就绪，将定期重试", args...)
}

// initializeRuntime requires both identity probing and model discovery to succeed.
// initializeRuntime 要求身份探测和模型发现都成功。
func initializeRuntime(ctx context.Context, rt Runtime) (ProbeResult, Discovery, error) {
	probe, err := rt.Probe(ctx)
	if err != nil {
		return ProbeResult{}, Discovery{}, err
	}
	discovery, err := rt.Discover(ctx)
	return probe, discovery, err
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

// Revisions returns every registered instance's current material-state
// revision without copying any instance state. See RevisionSource.
func (m *manager) Revisions() map[string]uint64 {
	m.mu.Lock()
	insts := make(map[string]*managedInstance, len(m.instances))
	for id, inst := range m.instances {
		insts[id] = inst
	}
	m.mu.Unlock()

	revs := make(map[string]uint64, len(insts))
	for id, inst := range insts {
		inst.mu.Lock()
		revs[id] = inst.rev
		inst.mu.Unlock()
	}
	return revs
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
		Revision:   inst.rev,
	}
}

// healthMaterialEqual reports whether two health reports differ in nothing a
// status consumer cares about: CheckedAt and Latency move on every check even
// when the runtime did not change.
//
// healthMaterialEqual 判断两份健康报告是否在状态消费者关心的内容上完全一致：
// CheckedAt 与 Latency 在每次检查时都会变，即使运行时并没有变化。
func healthMaterialEqual(a, b HealthReport) bool {
	a.CheckedAt, a.Latency = time.Time{}, 0
	b.CheckedAt, b.Latency = time.Time{}, 0
	return a == b
}

// discoveryMaterialEqual reports whether two Discoveries differ only in
// DiscoveredAt. It is only called when a Discover result arrives (minutes
// apart), never on the status-poll path.
//
// discoveryMaterialEqual 判断两份 Discovery 是否只有 DiscoveredAt 不同。它只在
// Discover 结果到达时调用（间隔数分钟），不会出现在状态轮询路径上。
func discoveryMaterialEqual(a, b Discovery) bool {
	a.DiscoveredAt, b.DiscoveredAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func deepCopyDiscovery(d Discovery) Discovery {
	cp := d
	if d.Models != nil {
		cp.Models = make([]Model, len(d.Models))
		for i, model := range d.Models {
			cp.Models[i] = model
			cp.Models[i].Capabilities = copyCapabilitySet(model.Capabilities)
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
// instances again. Cancellation bounds the wait while cleanup finishes in the background.
// Close 停止调度并清理运行时；取消上下文限制等待时长，清理可继续完成；重复调用返回 nil。
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
	// Cleanup continues after a canceled wait; process owners enforce the final exit budget.
	// 等待取消后清理继续执行；进程所有者负责最终退出期限。
	done := make(chan error, 1)
	go func() {
		var errs []error
		for _, inst := range insts {
			<-inst.done
			if err := inst.runtime.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		done <- errors.Join(errs...)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
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
// Pending local backends retry Probe and Discover before becoming healthy.
//
// doHealthCheck 更新健康状态；本地未就绪实例先重新完成 Probe 与 Discover。
func (m *manager) doHealthCheck(inst *managedInstance) (recovered bool) {
	inst.mu.Lock()
	pending := inst.pending
	inst.mu.Unlock()
	if pending {
		m.retryInitialization(inst)
		return false
	}
	ctx, cancel := context.WithTimeout(inst.ctx, inst.cfg.ProbeTimeout)
	defer cancel()
	report, err := inst.runtime.Health(ctx)

	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.closed {
		return false
	}

	prevState, prevHealth := inst.state, inst.health
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
	if inst.state != prevState || !healthMaterialEqual(prevHealth, inst.health) {
		inst.rev = m.nextRev()
	}
	if logger := m.deps.Logger; logger != nil && inst.state != prevState {
		if inst.state == StateUnhealthy {
			logger.Warn("runtime became unhealthy / 运行时变为不健康", "runtime_id", inst.cfg.ID, "kind", inst.cfg.Kind,
				"error", inst.health.ErrorSummary, "probe_timeout", inst.cfg.ProbeTimeout)
		} else {
			logger.Info("runtime recovered / 运行时已恢复", "runtime_id", inst.cfg.ID, "kind", inst.cfg.Kind)
		}
	}
	return recovered
}

// retryInitialization keeps a backend unschedulable until full initialization succeeds.
// retryInitialization 在完整初始化成功前保持后端不可调度。
func (m *manager) retryInitialization(inst *managedInstance) {
	ctx, cancel := context.WithTimeout(inst.ctx, inst.cfg.RequestTimeout)
	defer cancel()
	probe, discovery, err := initializeRuntime(ctx, inst.runtime)
	inst.mu.Lock()
	if inst.closed {
		inst.mu.Unlock()
		return
	}
	now := m.clock.Now()
	inst.updatedAt = now
	inst.health.CheckedAt = now
	if err != nil {
		m.logNotReady(inst, err)
		inst.mu.Unlock()
		return
	}
	inst.notReadyError, inst.notReadyLoggedAt = "", time.Time{}
	inst.pending, inst.state = false, StateHealthy
	inst.probe, inst.discovery = probe, discovery
	inst.descriptor = inst.runtime.Descriptor()
	inst.degraded = append([]string(nil), discovery.Warnings...)
	inst.health = HealthReport{State: StateHealthy, CheckedAt: now}
	inst.rev = m.nextRev()
	inst.mu.Unlock()
	if m.deps.Logger != nil {
		m.deps.Logger.Info("runtime ready / 运行时已就绪", "runtime_id", inst.cfg.ID, "kind", inst.cfg.Kind)
	}
}

// doDiscoverRefresh updates models only after initial identity verification succeeds.
// doDiscoverRefresh 只在首次身份验证成功后更新模型目录。
func (m *manager) doDiscoverRefresh(inst *managedInstance) {
	inst.mu.Lock()
	pending := inst.pending
	inst.mu.Unlock()
	if pending {
		return
	}
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
	if !discoveryMaterialEqual(inst.discovery, discovery) {
		inst.rev = m.nextRev()
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

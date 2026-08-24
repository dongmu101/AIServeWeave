package tunnel

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// This file is the Agent-side slot pool of one tunnel: it keeps a few Serve
// streams parked and ready so a dispatched request costs no stream setup,
// opens more when the idle ones run out, and closes the surplus again when
// the burst is over.
//
// Everything here is per-replica. A slot belongs to one tunnel and never
// migrates: borrowing a sibling's slot would need cross-tunnel coordination,
// and the replica on the other end cannot see the other pools anyway. The
// consequence — that the slot ceilings of N replicas can add up to more than
// the node can really run — is deliberate. Slots are the soft quota that
// avoids obvious overload; runtime's limiter is the hard quota that
// guarantees the node is never overloaded, and it answers with
// ErrorBackpressure so the replica simply picks another node.
//
// All slot bookkeeping happens on one goroutine. Slots report their
// transitions through slotIdle/slotBusy/slotFinished, which only update
// counters and poke the maintenance loop; the loop alone opens and reaps
// slots, so no two goroutines ever race to satisfy the same watermark.

// poolOperation is the Operation recorded on errors the pool itself raises.
const poolOperation = "tunnel_pool"

// Slot pool defaults, all from README.md's 槽池 section. Each is per replica.
const (
	defaultMinSlots           = 2
	defaultLowWatermark       = 1
	defaultBulkSlots          = 1
	defaultNodeTotalSlots     = 32
	defaultSlotIdleTimeout    = 5 * time.Minute
	defaultMaxRequestsPerSlot = 200
	defaultMaxSlotAge         = time.Hour
)

// SlotConfig is one replica's slot-pool configuration, mirroring the `slots`
// section of the Agent configuration. The zero value is valid and means "use
// every default".
type SlotConfig struct {
	// MinSlots is how many inference slots stay parked at all times, so a
	// burst of requests costs no stream setup. Default 2.
	MinSlots int
	// LowWatermark is the idle count below which the pool opens more slots.
	// Default 1.
	LowWatermark int
	// BulkSlots is this replica's SLOT_CLASS_BULK quota, kept parked and
	// separate so an artifact transfer cannot crowd out inference. Default 1;
	// a negative value means this node serves no artifacts at all.
	BulkSlots int
	// NodeTotalSlots is the node's whole slot budget, shared out across the
	// active replicas by PerReplicaMax. Default 32; the caller should pass
	// the sum of the runtimes' MaxConcurrent when it knows it.
	NodeTotalSlots int
	// IdleTimeout is how long a surplus slot may sit idle before it is
	// closed. MinSlots slots are never reaped. Default 5m.
	IdleTimeout time.Duration
	// MaxRequestsPerSlot and MaxSlotAge rotate a slot before hidden state can
	// accumulate on a long-lived stream. Defaults 200 and 1h; a value of zero
	// takes the default and a negative value disables that limit.
	MaxRequestsPerSlot int
	MaxSlotAge         time.Duration
}

func (c *SlotConfig) applyDefaults() {
	setDefaultInt(&c.MinSlots, defaultMinSlots)
	setDefaultInt(&c.LowWatermark, defaultLowWatermark)
	setDefaultInt(&c.BulkSlots, defaultBulkSlots)
	// A negative bulk quota is how a node that serves no artifacts says so;
	// zero would be indistinguishable from "unset" and take the default.
	if c.BulkSlots < 0 {
		c.BulkSlots = 0
	}
	setDefaultInt(&c.NodeTotalSlots, defaultNodeTotalSlots)
	setDefaultInt(&c.MaxRequestsPerSlot, defaultMaxRequestsPerSlot)
	// The two rotation limits and the idle timeout each take their default
	// only when unset: a negative value is how a deployment says "never
	// rotate on this axis", which a <= 0 default would silently overwrite.
	if c.IdleTimeout == 0 {
		c.IdleTimeout = defaultSlotIdleTimeout
	}
	if c.MaxSlotAge == 0 {
		c.MaxSlotAge = defaultMaxSlotAge
	}

	// The watermark cannot ask for more idle slots than the floor guarantees;
	// otherwise the pool would open a slot, reap it and open it again.
	if c.LowWatermark > c.MinSlots {
		c.LowWatermark = c.MinSlots
	}
}

// PerReplicaMax returns one replica's slot ceiling: the node's budget divided
// among the active replicas, but never less than the resident minimum.
//
//	per_replica_max = max(min_slots, ceil(node_total_slots / active_replicas))
//
// The floor is what keeps a node with many replicas usable at all, and it is
// why the ceilings can sum to more than nodeTotalSlots. See the file comment:
// that overshoot is the point, not a bug.
func PerReplicaMax(nodeTotalSlots, minSlots, activeReplicas int) int {
	if activeReplicas < 1 {
		activeReplicas = 1
	}
	if nodeTotalSlots < 0 {
		nodeTotalSlots = 0
	}
	share := (nodeTotalSlots + activeReplicas - 1) / activeReplicas
	return max(minSlots, share)
}

// PoolConfig configures one replica's slot pool.
type PoolConfig struct {
	// NodeID is echoed in every Ready frame so the replica can line the slot
	// up with the node without consulting the Control stream.
	NodeID string
	// ReplicaID names the replica in logs and metrics; it may be empty before
	// the handshake has supplied it.
	ReplicaID string
	// Slots holds the watermarks and rotation limits.
	Slots SlotConfig
	// Handler executes the requests dispatched into the slots. It is
	// required: a pool with nothing to dispatch to would park slots that
	// answer nothing.
	Handler Handler
	// ActiveReplicas is how many replicas the roster currently marks active,
	// which decides this replica's share of the node budget. Zero means one.
	ActiveReplicas int
	// OnServing is called when the pool gains its first parked slot and again
	// when it loses its last one, which is what moves a tunnel between
	// StateConnected and StateServing. It must not block.
	OnServing func(bool)
	// Metrics receives this pool's slot instruments. Nil discards them.
	Metrics runtime.Metrics
	// Clock defaults to runtime.NewSystemClock; every timer runs on it.
	Clock runtime.Clock
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// ClassStats is one slot class's occupancy.
type ClassStats struct {
	// Idle slots are parked and waiting for a request; Busy ones are serving
	// one; Opening ones have been created but have not sent Ready yet.
	Idle    int
	Busy    int
	Opening int
	// Max is the ceiling currently in force for the class.
	Max int
}

// Total reports how many slots of the class exist, in any state.
func (s ClassStats) Total() int { return s.Idle + s.Busy + s.Opening }

// PoolStats is a point-in-time view of a pool, for tests and for the metrics
// of 阶段 7.
type PoolStats struct {
	Inference ClassStats
	Bulk      ClassStats
}

// Pool keeps one replica's Serve streams at their watermarks.
type Pool struct {
	cfg       PoolConfig
	transport Transport
	clock     runtime.Clock
	logger    *slog.Logger
	handler   Handler
	metrics   *recorder

	// wake carries a "something changed, reconsider the watermarks" signal to
	// the maintenance loop. It holds one token: a second change arriving
	// before the loop wakes is covered by the reconcile the first one causes.
	wake chan struct{}

	wg     sync.WaitGroup
	cancel context.CancelFunc

	mu    sync.Mutex
	slots map[string]*slot
	// openFailedAt is when a slot last failed to reach Ready. Opening is
	// paused for slotOpenBackoff afterwards: a replica that accepts Control
	// but refuses Serve — a half-restarted process, a stream quota, a broken
	// proxy — would otherwise be answered with an unbounded open/fail loop,
	// which burns CPU on both sides and hides the real fault in a flood of
	// identical log lines. It is cleared as soon as a slot parks.
	openFailedAt   time.Time
	nextID         int
	activeReplicas int
	hint           *tunnelv1.SlotHint
	draining       bool
	serving        bool
	started        bool
	stopped        bool
}

// NewPool validates cfg and returns a pool with no slots open yet. Start
// opens them.
func NewPool(cfg PoolConfig, transport Transport) (*Pool, error) {
	switch {
	case cfg.NodeID == "":
		return nil, poolConfigError("node_id is required")
	case cfg.Handler == nil:
		return nil, poolConfigError("a request handler is required")
	case transport == nil:
		return nil, poolConfigError("transport is required")
	}
	cfg.Slots.applyDefaults()
	if cfg.Clock == nil {
		cfg.Clock = runtime.NewSystemClock()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ActiveReplicas < 1 {
		cfg.ActiveReplicas = 1
	}

	return &Pool{
		cfg:       cfg,
		transport: transport,
		clock:     cfg.Clock,
		logger: cfg.Logger.With(
			slog.String("node_id", cfg.NodeID),
			slog.String("replica_id", cfg.ReplicaID)),
		handler:        cfg.Handler,
		metrics:        newRecorder(cfg.Metrics, cfg.NodeID, cfg.ReplicaID),
		wake:           make(chan struct{}, 1),
		slots:          map[string]*slot{},
		activeReplicas: cfg.ActiveReplicas,
	}, nil
}

// Start warms the pool and keeps it at its watermarks until ctx is cancelled
// or Stop is called. The slots hang off ctx, so a caller that ties ctx to one
// connection gets the README's rule for free: a reconnect voids every slot of
// the old connection, and its in-flight requests end with a definite error
// rather than being silently retried.
func (p *Pool) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return poolConfigError("the slot pool has already been started")
	}
	p.started = true
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.maintain(runCtx)
	}()
	return nil
}

// Stop closes every slot and waits for the goroutines to finish. In-flight
// requests are cancelled: a caller that wants them to finish drains first and
// stops afterwards.
func (p *Pool) Stop() {
	p.mu.Lock()
	if !p.started || p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	cancel := p.cancel
	p.mu.Unlock()

	cancel()
	p.wg.Wait()
	p.setServing(false)
}

// InFlight reports how many requests are running across all classes. Client
// uses it to decide when draining is done.
func (p *Pool) InFlight() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, s := range p.slots {
		if s.poolParked && !s.poolIdle {
			n++
		}
	}
	return n
}

// Stats reports the pool's current occupancy.
func (p *Pool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	inference := p.statsLocked(tunnelv1.SlotClass_SLOT_CLASS_INFERENCE)
	bulk := p.statsLocked(tunnelv1.SlotClass_SLOT_CLASS_BULK)
	return PoolStats{Inference: inference, Bulk: bulk}
}

// SetActiveReplicas records how many replicas the roster marks active and
// re-shares the node budget accordingly. The roster handling of 阶段 6 calls
// it whenever the replica set changes; shrinking a ceiling never closes a
// busy slot, so a rescale cannot interrupt a request.
func (p *Pool) SetActiveReplicas(n int) {
	if n < 1 {
		n = 1
	}
	p.mu.Lock()
	changed := p.activeReplicas != n
	p.activeReplicas = n
	p.mu.Unlock()
	if changed {
		p.poke()
	}
}

// SetDraining stops or resumes refilling. A draining replica dispatches
// nothing new, so its parked slots are handed back at once and none are
// opened to replace them; the requests already running keep their slots until
// they finish, which is the difference between draining and disconnecting.
func (p *Pool) SetDraining(draining bool) {
	p.mu.Lock()
	changed := p.draining != draining
	p.draining = draining
	p.mu.Unlock()
	if changed {
		p.poke()
	}
}

// Draining reports whether refilling is currently suspended.
func (p *Pool) Draining() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.draining
}

// ApplyHint takes the replica's watermark advice. It is advice only: a hint
// may lower this pool's ceiling or raise its floor within the locally
// configured limits, and can never talk the Agent above them.
func (p *Pool) ApplyHint(hint *tunnelv1.SlotHint) {
	p.mu.Lock()
	p.hint = hint
	p.mu.Unlock()
	p.poke()
}

// maintain is the single goroutine that owns slot creation and reaping.
func (p *Pool) maintain(ctx context.Context) {
	// The reap tick is half the idle timeout, so a surplus slot lives at most
	// one and a half timeouts rather than needing a timer of its own.
	tick := newRearmingTimer(p.clock, max(p.cfg.Slots.IdleTimeout/2, time.Second))
	defer tick.stop()

	p.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			p.reconcile(ctx)
		case <-tick.C():
			p.reconcile(ctx)
			tick.arm()
		}
	}
}

// reconcile brings both classes back to their watermarks: reap what has been
// idle too long, then open what the floor and the low watermark call for.
func (p *Pool) reconcile(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	var (
		open []*slot
		reap []reapTarget
	)

	p.mu.Lock()
	now := p.clock.Now()
	for _, class := range []tunnelv1.SlotClass{
		tunnelv1.SlotClass_SLOT_CLASS_INFERENCE,
		tunnelv1.SlotClass_SLOT_CLASS_BULK,
	} {
		floor, low, ceiling := p.limitsLocked(class)
		stats := p.statsLocked(class)
		total, idle := stats.Total(), stats.Idle
		// A slot whose stream is still being opened counts as idle-to-be.
		// Without that, every reconcile between the decision to open a slot
		// and its first Ready would see the watermark unmet and open another.
		opening := stats.Opening

		for _, s := range p.slots {
			if s.class != class || !s.poolIdle || s.poolReaping {
				continue
			}
			switch {
			// A draining replica gives its idle slots back immediately: they
			// exist only to be dispatched into, and it has stopped
			// dispatching.
			case p.draining:
				reap = append(reap, reapTarget{slot: s, reason: "replica draining"})
			// Age-based rotation ignores the floor: the slot is replaced in
			// this same pass, so retiring it costs nothing and a stream never
			// outlives its limit just because the pool happens to sit at its
			// minimum. A busy slot rotates when its request ends instead.
			case p.cfg.Slots.MaxSlotAge > 0 && !now.Before(s.created.Add(p.cfg.Slots.MaxSlotAge)):
				reap = append(reap, reapTarget{slot: s, reason: "age limit reached"})
			case total > floor && p.cfg.Slots.IdleTimeout > 0 &&
				now.Sub(s.poolIdleSince) >= p.cfg.Slots.IdleTimeout:
				reap = append(reap, reapTarget{slot: s, reason: "idle timeout"})
			default:
				continue
			}
			s.poolReaping = true
			total--
			idle--
		}

		// The floor is unconditional; the low watermark only tops up the idle
		// set. Neither may cross the ceiling — that refusal is the whole
		// point of the per-replica share.
		want := floor
		if pending := idle + opening; pending < low {
			want = max(want, total+(low-pending))
		}
		want = min(want, ceiling)

		if !p.openPausedLocked(now) {
			for i := total; i < want; i++ {
				open = append(open, p.newSlotLocked(class))
			}
		}
	}
	p.mu.Unlock()

	for _, target := range reap {
		target.slot.retire(target.reason)
	}
	for _, s := range open {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			s.run(ctx)
		}()
	}

	p.refreshServing()
	p.reportSlots()
}

// reportSlots publishes the occupancy gauges. It runs at the end of every
// reconcile and on every slot state change, which is every moment the numbers
// can have moved: the maintenance loop is woken by exactly those events.
func (p *Pool) reportSlots() {
	p.mu.Lock()
	inference := p.statsLocked(tunnelv1.SlotClass_SLOT_CLASS_INFERENCE)
	bulk := p.statsLocked(tunnelv1.SlotClass_SLOT_CLASS_BULK)
	p.mu.Unlock()

	p.metrics.Slots(tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, inference.Idle, inference.Busy)
	p.metrics.Slots(tunnelv1.SlotClass_SLOT_CLASS_BULK, bulk.Idle, bulk.Busy)
}

// slotOpenBackoff is how long the pool waits before trying to open another
// slot after one failed to reach Ready. Retries then happen at the reap
// tick's cadence, which is the same order of magnitude as the reconnect
// backoff on the Control stream.
const slotOpenBackoff = time.Second

// reapTarget is one slot the reconcile decided to close, and why.
type reapTarget struct {
	slot   *slot
	reason string
}

// newSlotLocked registers a slot before its goroutine starts, so it counts
// against the ceiling from the moment it is decided on rather than from the
// moment its stream is up. Without that, a reconcile that ran while streams
// were still opening would decide to open them all over again.
func (p *Pool) newSlotLocked(class tunnelv1.SlotClass) *slot {
	p.nextID++
	id := p.cfg.NodeID + "-" + slotClassLabel(class) + "-" + strconv.Itoa(p.nextID)
	s := &slot{
		pool:    p,
		id:      id,
		class:   class,
		created: p.clock.Now(),
		logger: p.logger.With(
			slog.String("slot_id", id),
			slog.String("class", slotClassLabel(class))),
	}
	p.slots[id] = s
	return s
}

// limitsLocked returns the floor, the low watermark and the ceiling in force
// for a class, hint included.
func (p *Pool) limitsLocked(class tunnelv1.SlotClass) (floor, low, ceiling int) {
	if p.draining {
		// Nothing parked, nothing opened. Busy slots are not measured against
		// a ceiling of zero — reconcile never closes a busy slot — so the
		// requests already running go to completion.
		return 0, 0, 0
	}
	if class == tunnelv1.SlotClass_SLOT_CLASS_BULK {
		// The bulk quota stays parked in full: isolating artifact transfers
		// from inference only works if a bulk slot is actually standing by
		// when one arrives.
		n := p.cfg.Slots.BulkSlots
		if h := int(p.hint.GetBulkSlots()); h > 0 && h < n {
			n = h
		}
		return n, n, n
	}

	ceiling = PerReplicaMax(p.cfg.Slots.NodeTotalSlots, p.cfg.Slots.MinSlots, p.activeReplicas)
	floor = p.cfg.Slots.MinSlots
	if h := int(p.hint.GetMinSlots()); h > 0 {
		floor = clampInt(h, p.cfg.Slots.MinSlots, ceiling)
	}
	if h := int(p.hint.GetMaxSlots()); h > 0 {
		ceiling = clampInt(h, floor, ceiling)
	}
	low = min(p.cfg.Slots.LowWatermark, floor)
	return floor, low, ceiling
}

// statsLocked counts one class.
func (p *Pool) statsLocked(class tunnelv1.SlotClass) ClassStats {
	_, _, ceiling := p.limitsLocked(class)
	stats := ClassStats{Max: ceiling}
	for _, s := range p.slots {
		// A slot on its way out is counted nowhere: it can take no request,
		// and counting it would let it hold a place the pool may need to
		// refill.
		if s.class != class || s.poolReaping {
			continue
		}
		switch {
		case !s.poolParked:
			stats.Opening++
		case s.poolIdle:
			stats.Idle++
		default:
			stats.Busy++
		}
	}
	return stats
}

// openPausedLocked reports whether opening is still held off after a slot
// failed to come up.
func (p *Pool) openPausedLocked(now time.Time) bool {
	return !p.openFailedAt.IsZero() && now.Sub(p.openFailedAt) < slotOpenBackoff
}

// slotOpenFailed records that a slot could not be brought into service, so
// the next reconcile holds off instead of immediately trying again.
func (p *Pool) slotOpenFailed() {
	p.mu.Lock()
	p.openFailedAt = p.clock.Now()
	p.mu.Unlock()
}

// slotIdle records that a slot has parked itself with Ready.
func (p *Pool) slotIdle(s *slot) {
	p.mu.Lock()
	s.poolParked = true
	s.poolIdle = true
	s.poolIdleSince = p.clock.Now()
	// A slot came up, so whatever stopped the last one is over.
	p.openFailedAt = time.Time{}
	p.mu.Unlock()
	p.refreshServing()
	p.reportSlots()
	p.poke()
}

// slotBusy records that a request was dispatched into a slot.
func (p *Pool) slotBusy(s *slot) {
	p.mu.Lock()
	s.poolIdle = false
	p.mu.Unlock()
	// Taking a slot out of the idle set may drop the pool below its low
	// watermark, which is exactly when a replacement must be opened — before
	// the next request arrives rather than after it has waited.
	p.reportSlots()
	p.poke()
}

// slotFinished removes a slot that has closed for any reason: rotation, idle
// reaping, a protocol fault, or a broken stream. One slot going away is never
// more than one slot going away; the pool simply opens another if the
// watermarks still ask for one.
func (p *Pool) slotFinished(s *slot) {
	p.mu.Lock()
	delete(p.slots, s.id)
	p.mu.Unlock()
	p.refreshServing()
	p.reportSlots()
	p.poke()
}

// poke asks the maintenance loop to reconsider the watermarks.
func (p *Pool) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// refreshServing reports the "has this tunnel any live slot" edge to the
// Client, which is the connected ⇄ serving transition of the state machine.
func (p *Pool) refreshServing() {
	p.mu.Lock()
	serving := false
	for _, s := range p.slots {
		if s.poolParked {
			serving = true
			break
		}
	}
	p.mu.Unlock()
	p.setServing(serving)
}

func (p *Pool) setServing(serving bool) {
	p.mu.Lock()
	changed := p.serving != serving
	p.serving = serving
	p.mu.Unlock()
	if changed && p.cfg.OnServing != nil {
		p.cfg.OnServing(serving)
	}
}

// slotClassLabel renders a slot class for ids, logs and metric labels.
func slotClassLabel(class tunnelv1.SlotClass) string {
	switch class {
	case tunnelv1.SlotClass_SLOT_CLASS_INFERENCE:
		return "inference"
	case tunnelv1.SlotClass_SLOT_CLASS_BULK:
		return "bulk"
	default:
		return "unspecified"
	}
}

func poolConfigError(msg string) error {
	return &runtime.RuntimeError{
		Code:      runtime.ErrorInvalidConfig,
		Operation: poolOperation,
		Message:   "invalid slot pool configuration: " + msg,
		Cause:     ErrFatal,
	}
}

func setDefaultInt(n *int, def int) {
	if *n == 0 {
		*n = def
	}
}

func clampInt(n, lo, hi int) int {
	return min(max(n, lo), hi)
}

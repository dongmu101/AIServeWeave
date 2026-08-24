package scheduler

import (
	"errors"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

// Default thresholds for breakerRegistry, used whenever a Config leaves the
// corresponding field at its zero value. These are a starting point, not a
// value validated against real traffic — like the tunnel README's own
// node_total default, the real number has to wait for production numbers.
const (
	defaultFailureThreshold = 5
	defaultBaseCooldown     = 5 * time.Second
	defaultMaxCooldown      = 2 * time.Minute
)

// breakerFailure reports whether err should count toward a candidate's
// consecutive-failure streak. Only the three codes that plausibly mean "this
// node instance itself is broken" qualify: a connection that cannot be
// established, a request that times out, or the upstream answering with an
// error. ErrorBackpressure and ErrorRateLimited are deliberately excluded —
// they mean "busy right now", not "broken", which is the distinction
// README's 调度流程 section already drew when it said backpressure does not
// count toward circuit-breaking. Every other code is either not retryable to
// begin with (so candidates() never gets a second chance to skip the node
// anyway) or is a client-side problem the node cannot fix by resting.
func breakerFailure(err error) bool {
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		return false
	}
	switch rtErr.Code {
	case runtime.ErrorConnection, runtime.ErrorTimeout, runtime.ErrorUpstream:
		return true
	default:
		return false
	}
}

// breakerState is one candidate's circuit-breaker bookkeeping.
type breakerState struct {
	consecutiveFailures int
	tripCount           int
	open                bool
	openUntil           time.Time
}

// breakerRegistry tracks one breakerState per Candidate. All access goes
// through a single mutex: candidates() reads it once per scheduling decision
// and record is called once per dispatch attempt, which is nowhere near hot
// enough to need finer-grained locking.
type breakerRegistry struct {
	failureThreshold int
	baseCooldown     time.Duration
	maxCooldown      time.Duration

	mu      sync.Mutex
	entries map[Candidate]*breakerState
}

func newBreakerRegistry(failureThreshold int, baseCooldown, maxCooldown time.Duration) *breakerRegistry {
	if failureThreshold <= 0 {
		failureThreshold = defaultFailureThreshold
	}
	if baseCooldown <= 0 {
		baseCooldown = defaultBaseCooldown
	}
	if maxCooldown <= 0 {
		maxCooldown = defaultMaxCooldown
	}
	if maxCooldown < baseCooldown {
		maxCooldown = baseCooldown
	}
	return &breakerRegistry{
		failureThreshold: failureThreshold,
		baseCooldown:     baseCooldown,
		maxCooldown:      maxCooldown,
		entries:          make(map[Candidate]*breakerState),
	}
}

// eligible reports whether c may be tried right now. A candidate with no
// recorded failures is always eligible; an open breaker becomes eligible
// again once its cooldown has elapsed, so the next attempt doubles as a
// probe — see the package-level note on why this skips a dedicated
// half-open state.
func (r *breakerRegistry) eligible(c Candidate, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.entries[c]
	if !ok || !st.open {
		return true
	}
	return !now.Before(st.openUntil)
}

// record applies the outcome of one dispatch attempt against c. Only errors
// breakerFailure recognizes move the state machine; any other outcome
// (success or a non-qualifying error) resets the failure streak, since a
// non-qualifying error already says nothing about whether the node itself is
// healthy.
func (r *breakerRegistry) record(c Candidate, err error, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !breakerFailure(err) {
		if st, ok := r.entries[c]; ok {
			st.consecutiveFailures = 0
			st.tripCount = 0
			st.open = false
		}
		return
	}

	st, ok := r.entries[c]
	if !ok {
		st = &breakerState{}
		r.entries[c] = st
	}
	st.consecutiveFailures++
	if st.consecutiveFailures >= r.failureThreshold {
		st.open = true
		st.openUntil = now.Add(r.cooldown(st.tripCount))
		st.tripCount++
	}
}

// cooldown returns the open-window duration for the tripCount'th trip:
// baseCooldown doubled once per trip, capped at maxCooldown. No jitter is
// applied — this paces one Gateway replica's own probes against one node,
// not many independent clients converging on a shared server, so there is no
// thundering-herd problem to spread out.
func (r *breakerRegistry) cooldown(tripCount int) time.Duration {
	const maxShift = 32 // more than enough to saturate past maxCooldown
	shift := min(tripCount, maxShift)
	d := r.baseCooldown << shift
	if d <= 0 || d > r.maxCooldown {
		return r.maxCooldown
	}
	return d
}

package runtime

import "sync"

// Limiter enforces a single Runtime instance's independent concurrency cap
// on inference and workflow requests. Probe, Health and Discover never
// acquire it, so a busy instance does not lose health visibility.
type Limiter struct {
	mu       sync.Mutex
	max      int // <= 0 means unlimited
	inflight int
	closed   bool
}

// NewLimiter creates a Limiter allowing at most max concurrent acquisitions.
// max <= 0 means unlimited, matching Config's "zero is default, an explicit
// negative value is unlimited" convention.
func NewLimiter(max int) *Limiter {
	return &Limiter{max: max}
}

// Acquire reserves one slot and returns a release function the caller must
// invoke exactly once, typically via defer, regardless of how the request
// ends; extra calls to release are safe no-ops. Acquire never blocks or
// queues: at capacity it immediately returns a *RuntimeError with Code
// ErrorBackpressure wrapping ErrConcurrencyLimit. After Close it returns a
// *RuntimeError with Code ErrorClosed wrapping ErrRuntimeClosed.
func (l *Limiter) Acquire() (release func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil, &RuntimeError{
			Code:      ErrorClosed,
			Operation: "acquire",
			Message:   "runtime is closed",
			Cause:     ErrRuntimeClosed,
		}
	}
	if l.max > 0 && l.inflight >= l.max {
		return nil, &RuntimeError{
			Code:      ErrorBackpressure,
			Operation: "acquire",
			Message:   "concurrency limit reached",
			Cause:     ErrConcurrencyLimit,
		}
	}
	l.inflight++

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.inflight--
			l.mu.Unlock()
		})
	}, nil
}

// Close prevents further Acquire calls from succeeding. Slots already
// granted are unaffected; their release functions keep working so in-flight
// requests can still wind down cleanly.
func (l *Limiter) Close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
}

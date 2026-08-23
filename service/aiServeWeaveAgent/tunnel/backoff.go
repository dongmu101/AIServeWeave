package tunnel

import (
	"math/rand"
	"time"
)

// Backoff produces the reconnect delays for one tunnel: full jitter over an
// exponentially growing window, `rand(0, min(max, initial * 2^attempt))`.
//
// Full jitter rather than a fixed multiplier is what keeps a restarted
// Gateway replica from being hit by every Agent at the same instant, and each
// tunnel owns its own Backoff so one replica's outage never paces another's
// reconnects. A Backoff is not safe for concurrent use; the run loop of a
// single tunnel is its only caller.
type Backoff struct {
	initial time.Duration
	max     time.Duration
	jitter  func() float64
	attempt int
}

// maxBackoffShift caps the exponent so a long outage cannot overflow the
// shift. Any window this size is already clamped to max.
const maxBackoffShift = 62

// NewBackoff returns a Backoff over [0, min(max, initial*2^attempt)).
// A non-positive initial or max falls back to the README defaults of 1s and
// 30s. jitter supplies a fraction in [0, 1) and may be nil, in which case a
// per-Backoff seeded source is used; tests pass a deterministic function.
func NewBackoff(initial, max time.Duration, jitter func() float64) *Backoff {
	if initial <= 0 {
		initial = time.Second
	}
	if max <= 0 {
		max = 30 * time.Second
	}
	if max < initial {
		max = initial
	}
	if jitter == nil {
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		jitter = r.Float64
	}
	return &Backoff{initial: initial, max: max, jitter: jitter}
}

// Next returns the next delay and advances the attempt counter.
func (b *Backoff) Next() time.Duration {
	window := b.window()
	b.attempt++
	return time.Duration(b.jitter() * float64(window))
}

// Window returns the current upper bound, min(max, initial*2^attempt), which
// is the interval Next draws from. It is exported for the log line that
// explains a reconnect delay.
func (b *Backoff) Window() time.Duration { return b.window() }

// Reset returns the sequence to its first attempt. The run loop calls it once
// a connection is established, so a tunnel that has been up for a week does
// not resume at a 30s delay after its first blip.
func (b *Backoff) Reset() { b.attempt = 0 }

// Attempt reports how many delays have been drawn since the last Reset.
func (b *Backoff) Attempt() int { return b.attempt }

func (b *Backoff) window() time.Duration {
	window := b.initial << min(b.attempt, maxBackoffShift)
	if window <= 0 || window > b.max {
		return b.max
	}
	return window
}

// Package tunneltest holds the fakes shared by the tunnel package's tests: a
// manually advanced Clock and an in-memory Gateway that speaks the Control
// stream. It lives under internal/ so it stays out of the public API while
// remaining importable from every test in this tree, mirroring what
// runtime/internal/runtimetest does for the runtime package.
package tunneltest

import (
	"sort"
	"sync"
	"time"
)

// Clock is a manually advanced runtime.Clock. The tunnel's heartbeat,
// status-report and reconnect timers all run on it, so a test drives 60s of
// tunnel behaviour without sleeping for any of it.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	armed  int
}

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
	fired    bool
	stopped  bool
}

// NewClock creates a Clock whose Now() starts at start.
func NewClock(start time.Time) *Clock { return &Clock{now: start} }

// Now reports the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer registers a timer due at Now()+d. A timer whose deadline has
// already passed fires immediately rather than waiting for the next Advance:
// a loop that re-arms its timer after the test moved the clock would
// otherwise hang forever on a deadline in the past.
func (c *Clock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	t := &fakeTimer{deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.armed++
	if !t.deadline.After(c.now) {
		t.fired = true
		t.ch <- c.now
	} else {
		c.timers = append(c.timers, t)
	}

	return t.ch, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if t.fired || t.stopped {
			return false
		}
		t.stopped = true
		return true
	}
}

// Advance moves the clock forward by d and fires every timer that is now due,
// in deadline order.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*fakeTimer
	kept := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.fired || t.stopped:
		case !t.deadline.After(now):
			t.fired = true
			due = append(due, t)
		default:
			kept = append(kept, t)
		}
	}
	c.timers = kept
	c.mu.Unlock()

	sort.Slice(due, func(i, j int) bool { return due[i].deadline.Before(due[j].deadline) })
	for _, t := range due {
		t.ch <- now
	}
}

// Armed reports how many timers have been created since the Clock was made.
// Tests use it as a synchronisation point: waiting for the count to grow
// proves the code under test has reached its select loop, so a subsequent
// Advance cannot race ahead of it.
func (c *Clock) Armed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.armed
}

// WaitArmed blocks until Armed() reaches n, reporting whether it did so
// before timeout. The poll interval is real time, but it is only ever
// traversed when the code under test is still starting up.
func (c *Clock) WaitArmed(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if c.Armed() >= n {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

package gatewaytest

import (
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

// Clock is a runtime.Clock whose time only moves when a test moves it, so
// heartbeat expiry is exercised without waiting for it.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	c       chan time.Time
	stopped bool
	fired   bool
}

// NewClock returns a Clock starting at a fixed, readable instant.
func NewClock() *Clock {
	return &Clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Now implements runtime.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer implements runtime.Clock.
func (c *Clock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), c: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.c, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if t.fired || t.stopped {
			return false
		}
		t.stopped = true
		return true
	}
}

// Advance moves the clock forward and fires every timer that comes due.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*fakeTimer
	kept := c.timers[:0]
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.at.After(now) {
			t.fired = true
			due = append(due, t)
			continue
		}
		if !t.stopped && !t.fired {
			kept = append(kept, t)
		}
	}
	c.timers = kept
	c.mu.Unlock()

	for _, t := range due {
		t.c <- now
	}
}

var _ runtime.Clock = (*Clock)(nil)

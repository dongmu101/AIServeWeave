// Package runtimetest holds fakes shared by tests across the runtime
// package and its subpackages: a manually-advanced Clock, a scriptable
// Runtime, and a scriptable WebSocket dialer/connection pair. It lives
// under internal/ so it stays out of the public API while still being
// importable from every test package in this tree.
package runtimetest

import (
	"sort"
	"sync"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

// Clock is a manually-advanced runtime.Clock, so Manager's periodic
// scheduling and adapter timeouts can be tested without real sleeps.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
	fired    bool
	stopped  bool
}

// NewClock creates a Clock whose Now() starts at start.
func NewClock(start time.Time) *Clock {
	return &Clock{now: start}
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer registers a timer due at Now()+d. The returned stop func mirrors
// time.Timer.Stop: it reports whether the timer was stopped before firing.
func (c *Clock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	t := &fakeTimer{deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)

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

// Advance moves the clock forward by d and fires every timer whose deadline
// has now been reached, in deadline order.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.fired && !t.stopped && !t.deadline.After(now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()

	sort.Slice(due, func(i, j int) bool { return due[i].deadline.Before(due[j].deadline) })
	for _, t := range due {
		t.ch <- now
	}
}

// PendingTimers reports how many timers are registered and still waiting to
// fire. Tests use it to synchronize with a goroutine that is about to wait
// on the clock, so they can Advance at the right moment instead of polling
// with real sleeps.
func (c *Clock) PendingTimers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.fired && !t.stopped {
			n++
		}
	}
	return n
}

var _ runtime.Clock = (*Clock)(nil)

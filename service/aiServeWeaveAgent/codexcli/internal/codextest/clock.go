// Package codextest supplies an injected clock for offline adapter tests.
// Package codextest 为离线适配器测试提供注入时钟。
package codextest

import (
	"sync"
	"time"
)

// Clock exposes timer registration so tests can advance without sleeping.
// Clock 暴露定时器注册信号，使测试无需睡眠即可推进时间。
type Clock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*timer
	Created chan time.Duration
}
type timer struct {
	at      time.Time
	ch      chan time.Time
	stopped bool
}

// NewClock constructs a deterministic clock.
// NewClock 创建确定性的时钟。
func NewClock() *Clock { return &Clock{now: time.Unix(0, 0), Created: make(chan time.Duration, 16)} }

// Now returns the current injected time.
// Now 返回当前注入时间。
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// NewTimer registers a timer and reports its duration to the test.
// NewTimer 注册定时器，并将时长报告给测试。
func (c *Clock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	t := &timer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	c.Created <- d
	return t.ch, func() bool { c.mu.Lock(); defer c.mu.Unlock(); active := !t.stopped; t.stopped = true; return active }
}

// Advance fires all due timers exactly once.
// Advance 将所有到期定时器各触发一次。
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, t := range c.timers {
		if !t.stopped && !t.at.After(c.now) {
			t.stopped = true
			t.ch <- c.now
		}
	}
}

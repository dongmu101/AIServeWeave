package e2e_test

import (
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

// steppableClock is the clock the Gateway's verifier runs on in these tests,
// so the cache window is crossed by advancing time rather than by waiting out
// a real thirty seconds.
//
// steppableClock 是这些测试中 Gateway verifier 所用的时钟，好让缓存窗口靠推进时间来
// 跨越，而不是靠真的等满三十秒。
type steppableClock struct {
	mu  sync.Mutex
	now time.Time
}

func newSteppableClock() *steppableClock {
	return &steppableClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *steppableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppableClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	return make(chan time.Time), func() bool { return true }
}

func (c *steppableClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var _ runtime.Clock = (*steppableClock)(nil)

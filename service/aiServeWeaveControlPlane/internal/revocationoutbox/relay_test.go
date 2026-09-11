package revocationoutbox_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/cache"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/revocationoutbox"
)

func TestInvalidateFlushesPendingGenerationSynchronously(t *testing.T) {
	store := &fakeStore{pending: true}
	publisher := &fakePublisher{}
	relay := revocationoutbox.New(store, publisher, newFakeClock())

	relay.Invalidate(context.Background(), "must-not-be-observed")

	if got := publisher.Calls(); got != 1 {
		t.Errorf("publisher calls = %d, want 1", got)
	}
	if store.Pending() {
		t.Errorf("pending = true, want false")
	}
}

func TestRunRetriesFailedPublicationOnInjectedClock(t *testing.T) {
	clock := newFakeClock()
	store := &fakeStore{pending: true}
	publisher := &fakePublisher{failures: 1}
	relay := revocationoutbox.New(store, publisher, clock)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		relay.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Run did not exit during cleanup")
		}
	})

	clock.WaitForTimers(t, 1)
	if got := publisher.Calls(); got != 1 {
		t.Fatalf("publisher calls before retry = %d, want 1", got)
	}
	if !store.Pending() {
		t.Fatalf("pending after failed publication = false, want true")
	}
	clock.Advance(time.Second)
	store.WaitForFlushes(t, 2)
	if got := publisher.Calls(); got != 2 {
		t.Errorf("publisher calls after retry = %d, want 2", got)
	}
	if store.Pending() {
		t.Errorf("pending after successful retry = true, want false")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after cancellation")
	}
}

func TestEveryFlushAttemptHasDeadline(t *testing.T) {
	store := &fakeStore{pending: true, requireDeadline: true}
	relay := revocationoutbox.New(store, &fakePublisher{}, newFakeClock())

	relay.InvalidateAll(context.Background())

	if err := store.Err(); err != nil {
		t.Fatalf("flush context error = %v, want nil", err)
	}
}

func TestCachePublisherMakesUnavailableErrorVisible(t *testing.T) {
	var publisher *cache.Verifications

	if err := publisher.PublishInvalidation(context.Background()); err == nil {
		t.Fatal("PublishInvalidation error = nil, want unavailable error")
	}
}

type fakeStore struct {
	mu              sync.Mutex
	pending         bool
	flushes         int
	requireDeadline bool
	err             error
	completed       chan struct{}
}

func (s *fakeStore) FlushRevocations(ctx context.Context, publish func(context.Context) error) (bool, error) {
	s.mu.Lock()
	pending := s.pending
	requireDeadline := s.requireDeadline
	s.mu.Unlock()
	defer s.completeFlush()

	if requireDeadline {
		if _, ok := ctx.Deadline(); !ok {
			s.mu.Lock()
			s.err = errors.New("flush context has no deadline")
			s.mu.Unlock()
			return false, s.err
		}
	}
	if !pending {
		return false, nil
	}
	if err := publish(ctx); err != nil {
		return false, err
	}
	s.mu.Lock()
	s.pending = false
	s.mu.Unlock()
	return true, nil
}

func (s *fakeStore) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

func (s *fakeStore) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *fakeStore) WaitForFlushes(t *testing.T, want int) {
	t.Helper()
	for {
		s.mu.Lock()
		got := s.flushes
		if s.completed == nil {
			s.completed = make(chan struct{}, 32)
		}
		completed := s.completed
		s.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Fatalf("flushes = %d, want at least %d", got, want)
		}
	}
}

func (s *fakeStore) completeFlush() {
	s.mu.Lock()
	s.flushes++
	if s.completed == nil {
		s.completed = make(chan struct{}, 32)
	}
	completed := s.completed
	s.mu.Unlock()
	completed <- struct{}{}
}

type fakePublisher struct {
	mu       sync.Mutex
	calls    int
	failures int
}

func (p *fakePublisher) PublishInvalidation(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.failures > 0 {
		p.failures--
		return errors.New("publisher unavailable")
	}
	return nil
}

func (p *fakePublisher) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	added  chan struct{}
}

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
	stopped  bool
	fired    bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0), added: make(chan struct{}, 32)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	timer := &fakeTimer{deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, timer)
	c.mu.Unlock()
	c.added <- struct{}{}
	return timer.ch, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if timer.fired || timer.stopped {
			return false
		}
		timer.stopped = true
		return true
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*fakeTimer
	for _, timer := range c.timers {
		if !timer.fired && !timer.stopped && !timer.deadline.After(now) {
			timer.fired = true
			due = append(due, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range due {
		timer.ch <- now
	}
}

func (c *fakeClock) WaitForTimers(t *testing.T, want int) {
	t.Helper()
	for {
		c.mu.Lock()
		got := 0
		for _, timer := range c.timers {
			if !timer.fired && !timer.stopped {
				got++
			}
		}
		c.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-c.added:
		case <-time.After(time.Second):
			t.Fatalf("pending timers = %d, want at least %d", got, want)
		}
	}
}

var _ runtime.Clock = (*fakeClock)(nil)
var _ revocationoutbox.Publisher = (*cache.Verifications)(nil)

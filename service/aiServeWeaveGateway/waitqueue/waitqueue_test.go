package waitqueue

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// memStore mirrors the Redis scripts' semantics on a shared clock, so the
// wait loop's decisions are testable without Redis. The live suite in
// store_redis_test.go holds the Lua to the same behaviour.
type memStore struct {
	mu       sync.Mutex
	clock    runtime.Clock
	seq      int64
	tickets  map[string]int64     // ticket -> seq
	leases   map[string]time.Time // ticket -> expiry
	progress time.Time
	failing  error
}

func newMemStore(clock runtime.Clock) *memStore {
	return &memStore{clock: clock, tickets: map[string]int64{}, leases: map[string]time.Time{}}
}

func (s *memStore) purge(now time.Time) {
	for ticket, expiry := range s.leases {
		if !expiry.After(now) {
			delete(s.leases, ticket)
			delete(s.tickets, ticket)
		}
	}
}

func (s *memStore) ordered() []string {
	out := make([]string, 0, len(s.tickets))
	for ticket := range s.tickets {
		out = append(out, ticket)
	}
	sort.Slice(out, func(i, j int) bool { return s.tickets[out[i]] < s.tickets[out[j]] })
	return out
}

func (s *memStore) join(_ context.Context, ticket string, maxWaiters int, lease time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing != nil {
		return false, s.failing
	}
	now := s.clock.Now()
	s.purge(now)
	if len(s.tickets) >= maxWaiters {
		return false, nil
	}
	if len(s.tickets) == 0 {
		s.progress = now
	}
	s.seq++
	s.tickets[ticket] = s.seq
	s.leases[ticket] = now.Add(lease)
	return true, nil
}

func (s *memStore) poll(_ context.Context, ticket, prefix string, lease time.Duration, window int, stall time.Duration) (pollResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing != nil {
		return pollWait, s.failing
	}
	now := s.clock.Now()
	s.purge(now)
	if _, ok := s.tickets[ticket]; !ok {
		return pollLost, nil
	}
	s.leases[ticket] = now.Add(lease)
	order := s.ordered()
	rank := 0
	for i, t := range order {
		if t == ticket {
			rank = i
		}
	}
	if rank < window {
		return pollEligible, nil
	}
	if now.Sub(s.progress) <= stall {
		return pollWait, nil
	}
	for _, t := range order[:rank+1] {
		if strings.HasPrefix(t, prefix) {
			if t == ticket {
				return pollEligible, nil
			}
			return pollWait, nil
		}
	}
	return pollWait, nil
}

func (s *memStore) leave(_ context.Context, ticket string, dispatched bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tickets, ticket)
	delete(s.leases, ticket)
	if dispatched {
		s.progress = s.clock.Now()
	}
	return nil
}

func (s *memStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tickets)
}

var errBusy = &runtime.RuntimeError{Code: runtime.ErrorBackpressure, Retryable: true, Message: "busy"}

func newTestQueue(t *testing.T, clock *gatewaytest.Clock, store ticketStore, replica string, mutate func(*Config)) *Queue {
	t.Helper()
	cfg := Config{ReplicaID: replica, MaxWait: 10 * time.Second, RetryInterval: time.Second, MaxWaiters: 4, HeadWindow: 1, Clock: clock}
	if mutate != nil {
		mutate(&cfg)
	}
	q, err := newQueue(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// step advances the clock by d once the expected number of timers is armed.
func step(t *testing.T, clock *gatewaytest.Clock, timers int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for clock.PendingTimers() < timers {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d timers, have %d", timers, clock.PendingTimers())
		}
		time.Sleep(time.Millisecond)
	}
	clock.Advance(d)
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	clock := gatewaytest.NewClock()
	if _, err := newQueue(Config{ReplicaID: "r1", Clock: clock}, newMemStore(clock)); err == nil {
		t.Error("zero MaxWait accepted")
	}
	if _, err := newQueue(Config{ReplicaID: "a|b", MaxWait: time.Second, Clock: clock}, newMemStore(clock)); err == nil {
		t.Error("replica id with '|' accepted")
	}
	if _, err := New(Config{ReplicaID: "r1", MaxWait: time.Second}); err == nil {
		t.Error("missing Redis client accepted")
	}
}

func TestWaitResolvesWhenCapacityFreesUp(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	q := newTestQueue(t, clock, store, "r1", nil)
	var attempts atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- q.Wait(context.Background(), errBusy, func() (error, bool) {
			if attempts.Add(1) < 2 {
				return errBusy, true
			}
			return nil, false
		})
	}()
	step(t, clock, 1, time.Second)
	step(t, clock, 1, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("Wait = %v, want success", err)
	}
	if store.size() != 0 {
		t.Fatalf("ticket left behind: %d", store.size())
	}
}

func TestWaitRejectsWhenTheClusterQueueIsFull(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	q := newTestQueue(t, clock, store, "r1", func(c *Config) { c.MaxWaiters = 1 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Wait(ctx, errBusy, func() (error, bool) { return errBusy, true }) }()
	deadline := time.Now().Add(2 * time.Second)
	for store.size() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("first waiter never joined")
		}
		time.Sleep(time.Millisecond)
	}
	// A different replica sharing the store sees the same cluster-wide cap.
	other := newTestQueue(t, clock, store, "r2", func(c *Config) { c.MaxWaiters = 1 })
	called := false
	err := other.Wait(context.Background(), errBusy, func() (error, bool) { called = true; return nil, false })
	if !errors.Is(err, errBusy) || called {
		t.Fatalf("Wait = %v called=%v, want the first busy error at once", err, called)
	}
}

func TestWaitTimesOutWithTheLatestBusyError(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	q := newTestQueue(t, clock, store, "r1", func(c *Config) { c.MaxWait = 2 * time.Second })
	latest := &runtime.RuntimeError{Code: runtime.ErrorBackpressure, Message: "still busy"}
	done := make(chan error, 1)
	go func() {
		done <- q.Wait(context.Background(), errBusy, func() (error, bool) { return latest, true })
	}()
	step(t, clock, 1, time.Second)
	step(t, clock, 1, time.Second)
	if err := <-done; err != latest {
		t.Fatalf("Wait = %v, want the latest busy error", err)
	}
}

func TestOnlyTheHeadWindowRunsAPass(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	// Stall escape far away, so only FIFO order decides.
	q := newTestQueue(t, clock, store, "r1", func(c *Config) { c.RetryInterval = time.Second; c.MaxWait = time.Minute })
	q.stall = time.Hour

	var headAttempts, tailAttempts atomic.Int32
	release := make(chan struct{})
	headDone := make(chan error, 1)
	go func() {
		headDone <- q.Wait(context.Background(), errBusy, func() (error, bool) {
			headAttempts.Add(1)
			select {
			case <-release:
				return nil, false
			default:
				return errBusy, true
			}
		})
	}()
	waitSize(t, store, 1)
	tailDone := make(chan error, 1)
	go func() {
		tailDone <- q.Wait(context.Background(), errBusy, func() (error, bool) {
			tailAttempts.Add(1)
			return nil, false
		})
	}()
	waitSize(t, store, 2)

	step(t, clock, 2, time.Second)
	waitAttempts(t, &headAttempts, 1)
	if tailAttempts.Load() != 0 {
		t.Fatal("a ticket outside the head window ran a pass")
	}
	close(release)
	step(t, clock, 2, time.Second)
	if err := <-headDone; err != nil {
		t.Fatal(err)
	}
	// The tail is now the head: it runs on this advance or, if it polled
	// before the head left, on the next.
	var tailErr error
	select {
	case tailErr = <-tailDone:
	case <-time.After(50 * time.Millisecond):
		step(t, clock, 1, time.Second)
		tailErr = <-tailDone
	}
	if tailErr != nil || tailAttempts.Load() != 1 {
		t.Fatalf("tail = %v attempts %d", tailErr, tailAttempts.Load())
	}
}

func TestStallEscapeLetsAReplicasOldestTicketTry(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	// The head belongs to replica r1, which never makes progress; r2's
	// waiter is outside the window and must escape once the queue stalls.
	head := newTestQueue(t, clock, store, "r1", func(c *Config) { c.MaxWait = time.Minute })
	tail := newTestQueue(t, clock, store, "r2", func(c *Config) { c.MaxWait = time.Minute })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = head.Wait(ctx, errBusy, func() (error, bool) { return errBusy, true }) }()
	waitSize(t, store, 1)
	var tailAttempts atomic.Int32
	tailDone := make(chan error, 1)
	go func() {
		tailDone <- tail.Wait(context.Background(), errBusy, func() (error, bool) {
			tailAttempts.Add(1)
			return nil, false
		})
	}()
	waitSize(t, store, 2)

	// Within the stall window (4 × 1s) the tail waits.
	for i := 0; i < 4; i++ {
		step(t, clock, 2, time.Second)
	}
	time.Sleep(10 * time.Millisecond)
	if tailAttempts.Load() != 0 {
		t.Fatal("the tail ran before the queue stalled")
	}
	step(t, clock, 2, time.Second)
	if err := <-tailDone; err != nil || tailAttempts.Load() != 1 {
		t.Fatalf("tail = %v attempts %d, want it to escape the stall", err, tailAttempts.Load())
	}
}

func TestStoreFailureAnswersAtOnce(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	store.failing = errors.New("redis down")
	q := newTestQueue(t, clock, store, "r1", nil)
	if err := q.Wait(context.Background(), errBusy, func() (error, bool) { return nil, false }); !errors.Is(err, errBusy) {
		t.Fatalf("Wait = %v, want the busy error without waiting", err)
	}
}

func TestCanceledWaitLeavesNoTicket(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	q := newTestQueue(t, clock, store, "r1", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.Wait(ctx, errBusy, func() (error, bool) { return errBusy, true }) }()
	waitSize(t, store, 1)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v, want context.Canceled", err)
	}
	if store.size() != 0 {
		t.Fatal("a canceled wait left its ticket behind")
	}
}

func TestExpiredLeaseIsLost(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	q := newTestQueue(t, clock, store, "r1", func(c *Config) { c.MaxWait = time.Hour })
	done := make(chan error, 1)
	go func() { done <- q.Wait(context.Background(), errBusy, func() (error, bool) { return errBusy, true }) }()
	waitSize(t, store, 1)
	// A pause longer than the lease (min 5s at a 1s interval → 10s).
	step(t, clock, 1, 11*time.Second)
	if err := <-done; !errors.Is(err, errBusy) {
		t.Fatalf("Wait = %v, want the busy error after losing the ticket", err)
	}
}

func waitSize(t *testing.T, store *memStore, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for store.size() != n {
		if time.Now().After(deadline) {
			t.Fatalf("store size %d, want %d", store.size(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitAttempts(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("attempts %d, want %d", n.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

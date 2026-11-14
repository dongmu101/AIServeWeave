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
	mu           sync.Mutex
	clock        runtime.Clock
	seq          int64
	lanes        map[string]map[string]int64 // lane -> ticket -> seq
	leases       map[string]time.Time        // ticket -> expiry
	laneOf       map[string]string           // ticket -> lane
	lastDispatch map[string]time.Time        // lane -> last queued dispatch
	failing      error
	block        bool // when set, every call waits for its context to end
}

func newMemStore(clock runtime.Clock) *memStore {
	return &memStore{clock: clock, lanes: map[string]map[string]int64{}, leases: map[string]time.Time{},
		laneOf: map[string]string{}, lastDispatch: map[string]time.Time{}}
}

func (s *memStore) gate(ctx context.Context) error {
	s.mu.Lock()
	block, failing := s.block, s.failing
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return failing
}

func (s *memStore) remove(ticket string) {
	if lane, ok := s.laneOf[ticket]; ok {
		delete(s.lanes[lane], ticket)
	}
	delete(s.leases, ticket)
	delete(s.laneOf, ticket)
}

func (s *memStore) purge(now time.Time) {
	for ticket, expiry := range s.leases {
		if !expiry.After(now) {
			s.remove(ticket)
		}
	}
}

func (s *memStore) ordered(lane string) []string {
	out := make([]string, 0, len(s.lanes[lane]))
	for ticket := range s.lanes[lane] {
		out = append(out, ticket)
	}
	sort.Slice(out, func(i, j int) bool { return s.lanes[lane][out[i]] < s.lanes[lane][out[j]] })
	return out
}

func (s *memStore) add(lane, ticket string, seq int64, lease time.Duration) {
	if s.lanes[lane] == nil {
		s.lanes[lane] = map[string]int64{}
	}
	s.lanes[lane][ticket] = seq
	s.laneOf[ticket] = lane
	s.leases[ticket] = s.clock.Now().Add(lease)
}

func (s *memStore) join(ctx context.Context, lane, ticket string, maxWaiters int, lease time.Duration) (bool, error) {
	if err := s.gate(ctx); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.purge(now)
	if len(s.leases) >= maxWaiters {
		return false, nil
	}
	if len(s.lanes[lane]) == 0 {
		s.lastDispatch[lane] = now
	}
	s.seq++
	s.add(lane, ticket, s.seq, lease)
	return true, nil
}

func (s *memStore) poll(ctx context.Context, lane, ticket, prefix string, lease time.Duration, window int, stall time.Duration) (pollResult, int64, error) {
	if err := s.gate(ctx); err != nil {
		return pollWait, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.purge(now)
	seq, ok := s.lanes[lane][ticket]
	if !ok {
		return pollLost, 0, nil
	}
	order := s.ordered(lane)
	rank := 0
	for i, t := range order {
		if t == ticket {
			rank = i
		}
	}
	eligible := rank < window
	if !eligible && now.Sub(s.lastDispatch[lane]) > stall {
		for _, t := range order[:rank+1] {
			if strings.HasPrefix(t, prefix) {
				eligible = t == ticket
				break
			}
		}
	}
	if !eligible {
		s.leases[ticket] = now.Add(lease)
		return pollWait, 0, nil
	}
	s.remove(ticket)
	return pollEligible, seq, nil
}

func (s *memStore) rejoin(ctx context.Context, lane, ticket string, seq int64, lease time.Duration) error {
	if err := s.gate(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.add(lane, ticket, seq, lease)
	return nil
}

func (s *memStore) leave(_ context.Context, _ string, ticket string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remove(ticket)
	return nil
}

func (s *memStore) progress(ctx context.Context, lane string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastDispatch[lane] = s.clock.Now()
	return nil
}

func (s *memStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.leases)
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
		done <- q.Wait(context.Background(), "m", errBusy, func() (error, bool) {
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
	go func() { _ = q.Wait(ctx, "m", errBusy, func() (error, bool) { return errBusy, true }) }()
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
	err := other.Wait(context.Background(), "m", errBusy, func() (error, bool) { called = true; return nil, false })
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
		done <- q.Wait(context.Background(), "m", errBusy, func() (error, bool) { return latest, true })
	}()
	step(t, clock, 1, time.Second)
	step(t, clock, 1, time.Second)
	if err := <-done; err != latest {
		t.Fatalf("Wait = %v, want the latest busy error", err)
	}
}

// stuckHead puts a ticket at the head of lane "m" that never polls: a
// waiter on a replica that cannot reach the free node.
func stuckHead(t *testing.T, store *memStore, replica string) string {
	t.Helper()
	ticket := replica + "|" + laneOf("m") + "|stuck"
	if ok, err := store.join(context.Background(), laneOf("m"), ticket, 100, time.Hour); err != nil || !ok {
		t.Fatalf("join stuck head = %v %v", ok, err)
	}
	return ticket
}

func TestOnlyTheHeadWindowRunsAPass(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	q := newTestQueue(t, clock, store, "r1", func(c *Config) { c.MaxWait = time.Minute })
	q.stall = time.Hour // only FIFO order decides
	head := stuckHead(t, store, "r0")

	var attempts atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- q.Wait(context.Background(), "m", errBusy, func() (error, bool) { attempts.Add(1); return nil, false })
	}()
	waitSize(t, store, 2)
	for i := 0; i < 3; i++ {
		step(t, clock, 1, time.Second)
	}
	time.Sleep(10 * time.Millisecond)
	if attempts.Load() != 0 {
		t.Fatal("a ticket outside the head window ran a pass")
	}
	_ = store.leave(context.Background(), laneOf("m"), head)
	step(t, clock, 1, time.Second)
	if err := <-done; err != nil || attempts.Load() != 1 {
		t.Fatalf("Wait = %v attempts %d, want one pass once it reached the head", err, attempts.Load())
	}
}

func TestLanesDoNotBlockEachOther(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	q := newTestQueue(t, clock, store, "r1", func(c *Config) { c.MaxWait = time.Minute })
	q.stall = time.Hour
	stuckHead(t, store, "r0") // model "m" is saturated

	done := make(chan error, 1)
	go func() {
		done <- q.Wait(context.Background(), "other-model", errBusy, func() (error, bool) { return nil, false })
	}()
	waitSize(t, store, 2)
	step(t, clock, 1, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("a waiter for another model must not queue behind model m: %v", err)
	}
}

func TestStallEscapeLetsAReplicasOldestTicketTry(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	tail := newTestQueue(t, clock, store, "r2", func(c *Config) { c.MaxWait = time.Minute })
	stuckHead(t, store, "r1") // r1 never makes progress

	var tailAttempts atomic.Int32
	tailDone := make(chan error, 1)
	go func() {
		tailDone <- tail.Wait(context.Background(), "m", errBusy, func() (error, bool) {
			tailAttempts.Add(1)
			return nil, false
		})
	}()
	waitSize(t, store, 2)

	// Within the stall window (4 × 1s) the tail waits.
	for i := 0; i < 4; i++ {
		step(t, clock, 1, time.Second)
	}
	time.Sleep(10 * time.Millisecond)
	if tailAttempts.Load() != 0 {
		t.Fatal("the tail ran before the lane stalled")
	}
	step(t, clock, 1, time.Second)
	if err := <-tailDone; err != nil || tailAttempts.Load() != 1 {
		t.Fatalf("tail = %v attempts %d, want it to escape the stall", err, tailAttempts.Load())
	}
}

func TestUnresponsiveStoreAnswersWithinTheStoreTimeout(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	store.block = true
	q := newTestQueue(t, clock, store, "r1", nil)
	start := time.Now()
	err := q.Wait(context.Background(), "m", errBusy, func() (error, bool) { return nil, false })
	if !errors.Is(err, errBusy) {
		t.Fatalf("Wait = %v, want the busy error", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("an unresponsive store held the request %v", elapsed)
	}
}

func TestCanceledJoinIsNotAStoreError(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	store.block = true
	q := newTestQueue(t, clock, store, "r1", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Wait(ctx, "m", errBusy, func() (error, bool) { return nil, false }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v, want context.Canceled", err)
	}
}

func TestNewEnforcesLoadBounds(t *testing.T) {
	clock := gatewaytest.NewClock()
	if _, err := newQueue(Config{ReplicaID: "r1", MaxWait: time.Second, RetryInterval: time.Millisecond, Clock: clock}, newMemStore(clock)); err == nil {
		t.Error("a 1ms retry interval was accepted")
	}
	if _, err := newQueue(Config{ReplicaID: "r1", MaxWait: time.Second, MaxWaiters: MaxMaxWaiters + 1, Clock: clock}, newMemStore(clock)); err == nil {
		t.Error("max waiters above the bound was accepted")
	}
}

func TestStoreFailureAnswersAtOnce(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	store.failing = errors.New("redis down")
	q := newTestQueue(t, clock, store, "r1", nil)
	if err := q.Wait(context.Background(), "m", errBusy, func() (error, bool) { return nil, false }); !errors.Is(err, errBusy) {
		t.Fatalf("Wait = %v, want the busy error without waiting", err)
	}
}

func TestCanceledWaitLeavesNoTicket(t *testing.T) {
	clock := gatewaytest.NewClock()
	store := newMemStore(clock)
	q := newTestQueue(t, clock, store, "r1", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.Wait(ctx, "m", errBusy, func() (error, bool) { return errBusy, true }) }()
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
	go func() {
		done <- q.Wait(context.Background(), "m", errBusy, func() (error, bool) { return errBusy, true })
	}()
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

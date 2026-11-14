// Package jobqueuetest provides in-memory jobqueue.Store and
// jobqueue.Transport implementations, so the queue and the HTTP handlers on
// top of it can be tested without Redis, including several replicas sharing
// one queue.
//
// jobqueuetest 提供内存版的 jobqueue.Store 与 jobqueue.Transport，使队列及其上层的 HTTP
// 处理器无需 Redis 即可测试，包括多个副本共享同一个队列的情形。
package jobqueuetest

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/jobqueue"
)

// Store is an in-memory jobqueue.Store with the Redis scripts' semantics.
type Store struct {
	mu       sync.Mutex
	records  map[string]jobqueue.Record
	intents  map[string]string
	admitted map[string]int64 // job id -> admission order
	seq      int64
	// err, when set via SetErr, is returned by every call.
	err error
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{records: map[string]jobqueue.Record{}, intents: map[string]string{}, admitted: map[string]int64{}}
}

func (s *Store) Admit(_ context.Context, rec jobqueue.Record, maxLength int, _, _ time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	if len(s.admitted) >= maxLength {
		return 0, jobqueue.ErrQueueFull
	}
	if _, ok := s.records[rec.JobID]; ok {
		return 0, jobqueue.ErrIndeterminate
	}
	rec.State = jobqueue.StateQueued
	rec.UpdatedAt = rec.CreatedAt
	s.records[rec.JobID] = rec
	s.seq++
	s.admitted[rec.JobID] = s.seq
	return s.positionLocked(rec.JobID), nil
}

func (s *Store) positionLocked(jobID string) int {
	order, ok := s.admitted[jobID]
	if !ok {
		return 0
	}
	position := 1
	for _, other := range s.admitted {
		if other < order {
			position++
		}
	}
	return position
}

func (s *Store) transition(jobID string, from jobqueue.State, intent string, drop bool, mutate func(*jobqueue.Record)) (bool, bool, error) {
	if s.err != nil {
		return false, false, s.err
	}
	rec, ok := s.records[jobID]
	if !ok || rec.State != from || (intent != "" && s.intents[jobID] != intent) {
		return false, false, nil
	}
	mutate(&rec)
	s.records[jobID] = rec
	if drop {
		delete(s.admitted, jobID)
	}
	return true, rec.CancelRequested, nil
}

func (s *Store) Withdraw(_ context.Context, jobID string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok, _, err := s.transition(jobID, jobqueue.StateQueued, "", true, func(r *jobqueue.Record) {
		r.State, r.ErrorCode, r.UpdatedAt = jobqueue.StateCancelled, jobqueue.CodeCancelled, now
	})
	return ok, err
}

func (s *Store) Get(_ context.Context, jobID string) (jobqueue.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return jobqueue.Record{}, false, s.err
	}
	rec, ok := s.records[jobID]
	return rec, ok, nil
}

func (s *Store) Position(_ context.Context, jobID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	return s.positionLocked(jobID), nil
}

func (s *Store) Depth(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.admitted), s.err
}

func (s *Store) BeginDispatch(_ context.Context, jobID, intent string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok, _, err := s.transition(jobID, jobqueue.StateQueued, "", false, func(r *jobqueue.Record) {
		r.State, r.UpdatedAt = jobqueue.StateDispatching, now
	})
	if ok {
		s.intents[jobID] = intent
	}
	return ok, err
}

func (s *Store) Requeue(_ context.Context, jobID, intent string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, err := s.transition(jobID, jobqueue.StateDispatching, intent, false, func(r *jobqueue.Record) {
		r.State, r.UpdatedAt = jobqueue.StateQueued, now
	})
	return err
}

func (s *Store) Dispatched(_ context.Context, jobID, intent string, b jobqueue.Binding, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, cancel, err := s.transition(jobID, jobqueue.StateDispatching, intent, true, func(r *jobqueue.Record) {
		r.State, r.Binding, r.UpdatedAt = jobqueue.StateDispatched, b, now
	})
	return cancel, err
}

func (s *Store) Fail(_ context.Context, jobID string, from jobqueue.State, code string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok, _, err := s.transition(jobID, from, "", true, func(r *jobqueue.Record) {
		r.State, r.ErrorCode, r.UpdatedAt = jobqueue.StateFailed, code, now
	})
	return ok, err
}

func (s *Store) Cancel(_ context.Context, jobID string, now time.Time) (jobqueue.CancelResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	rec, ok := s.records[jobID]
	if !ok {
		return 0, jobqueue.ErrNotFound
	}
	switch rec.State {
	case jobqueue.StateQueued:
		rec.State, rec.ErrorCode, rec.UpdatedAt = jobqueue.StateCancelled, jobqueue.CodeCancelled, now
		s.records[jobID] = rec
		delete(s.admitted, jobID)
		return jobqueue.CancelDone, nil
	case jobqueue.StateDispatching:
		rec.CancelRequested = true
		s.records[jobID] = rec
		return jobqueue.CancelRequested, nil
	case jobqueue.StateDispatched:
		return jobqueue.CancelDispatched, nil
	default:
		return jobqueue.CancelFinished, nil
	}
}

// SetErr makes every later call fail with err (nil restores service).
func (s *Store) SetErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// SetState forces a record's state, for tests that stage a crash mid-submit.
func (s *Store) SetState(jobID string, state jobqueue.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[jobID]
	rec.State = state
	s.records[jobID] = rec
}

// Transport is an in-memory jobqueue.Transport. Tasks are delivered only when
// a test calls Deliver, so a test decides which replica's handler runs which
// task and when; several replicas can share one Transport.
//
// Transport 是内存版的 jobqueue.Transport。只有测试调用 Deliver 时才投递任务，因此由测试
// 决定哪个副本的处理器在何时运行哪个任务；多个副本可以共享同一个 Transport。
type Transport struct {
	mu         sync.Mutex
	pending    map[string][]byte
	order      map[string]int64
	seq        int64
	handlers   map[*Handle]func(ctx context.Context, payload []byte) error
	enqueueErr error
}

// Handle is one replica's view of a shared Transport.
type Handle struct{ t *Transport }

// NewTransport returns an empty Transport.
func NewTransport() *Transport {
	return &Transport{pending: map[string][]byte{}, order: map[string]int64{}, handlers: map[*Handle]func(context.Context, []byte) error{}}
}

// SetEnqueueErr makes Enqueue fail with err (nil restores service).
func (t *Transport) SetEnqueueErr(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.enqueueErr = err
}

// Replica returns a jobqueue.Transport for one replica sharing t.
func (t *Transport) Replica() *Handle { return &Handle{t: t} }

func (h *Handle) Enqueue(_ context.Context, jobID string, payload []byte, _ time.Duration) error {
	t := h.t
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.enqueueErr != nil {
		return t.enqueueErr
	}
	t.seq++
	t.pending[jobID] = payload
	t.order[jobID] = t.seq
	return nil
}

func (h *Handle) Delete(_ context.Context, jobID string) error {
	t := h.t
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, jobID)
	delete(t.order, jobID)
	return nil
}

func (h *Handle) Start(handle func(ctx context.Context, payload []byte) error) error {
	h.t.mu.Lock()
	defer h.t.mu.Unlock()
	h.t.handlers[h] = handle
	return nil
}

func (h *Handle) Shutdown() {
	h.t.mu.Lock()
	defer h.t.mu.Unlock()
	delete(h.t.handlers, h)
}

// Pending lists undelivered job ids in enqueue order.
func (t *Transport) Pending() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.pending))
	for id := range t.pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return t.order[ids[i]] < t.order[ids[j]] })
	return ids
}

// Deliver runs jobID's task on replica's handler once. A requeue puts it back
// as pending; any other outcome removes it. It returns the handler's error.
func (t *Transport) Deliver(ctx context.Context, replica *Handle, jobID string) error {
	t.mu.Lock()
	payload, ok := t.pending[jobID]
	handle := t.handlers[replica]
	t.mu.Unlock()
	if !ok {
		return errors.New("jobqueuetest: no pending task " + jobID)
	}
	if handle == nil {
		return errors.New("jobqueuetest: replica not started")
	}
	err := handle(ctx, payload)
	if !errors.Is(err, jobqueue.ErrRequeue) {
		t.mu.Lock()
		delete(t.pending, jobID)
		delete(t.order, jobID)
		t.mu.Unlock()
	}
	return err
}

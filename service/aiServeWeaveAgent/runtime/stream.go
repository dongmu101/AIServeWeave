package runtime

import (
	"errors"
	"io"
	"sync"
)

// Stream is a generic, cancelable sequence of events returned by streaming
// operations such as InferenceRuntime.ChatStream and
// WorkflowRuntime.Subscribe.
//
// Recv returns io.EOF when the stream ends normally, or another error for a
// terminal failure. Close must be safe to call more than once and must cause
// any blocked Recv to return promptly.
type Stream[T any] interface {
	Recv() (T, error)
	// Committed reports whether at least one item has already been
	// delivered to the consumer. Once true, the caller must not
	// transparently retry the request that produced this stream — partial
	// output (tokens, workflow events) may already be visible upstream.
	Committed() bool
	Close() error
}

// ErrStreamClosed is returned by Recv once the consumer has called Close,
// distinguishing a caller-initiated close from a normal io.EOF end or a
// producer-reported error.
var ErrStreamClosed = errors.New("runtime: stream closed")

// ChanStream is a Stream[T] backed by a channel. Adapters that decode events
// on a background goroutine use Send and CloseWithError to produce items;
// callers use the Stream[T] methods Recv and Close to consume them.
type ChanStream[T any] struct {
	items     chan T
	done      chan struct{}
	closeOnce sync.Once

	mu        sync.Mutex
	err       error
	committed bool
}

// NewChanStream creates a ChanStream with the given item buffer size. A
// buffer of 0 makes Send synchronize with Recv.
func NewChanStream[T any](buffer int) *ChanStream[T] {
	return &ChanStream[T]{
		items: make(chan T, buffer),
		done:  make(chan struct{}),
	}
}

// Send delivers an item to the consumer, blocking until it is received or
// the consumer closes the stream. It returns false once the consumer has
// closed the stream, at which point the producer must stop and must not
// call Send or CloseWithError again.
func (s *ChanStream[T]) Send(item T) bool {
	select {
	case s.items <- item:
		s.mu.Lock()
		s.committed = true
		s.mu.Unlock()
		return true
	case <-s.done:
		return false
	}
}

// Committed reports whether Send has ever completed, i.e. at least one item
// has been handed to the consumer.
func (s *ChanStream[T]) Committed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.committed
}

// CloseWithError marks the stream finished on the producer side. err == nil
// means a normal end, reported to Recv as io.EOF; a non-nil err is returned
// by Recv instead. The producer must call CloseWithError at most once, and
// must not call it after Send has returned false.
func (s *ChanStream[T]) CloseWithError(err error) {
	s.mu.Lock()
	if err == nil {
		err = io.EOF
	}
	s.err = err
	s.mu.Unlock()
	close(s.items)
}

// Recv returns the next item, or the terminal error once the stream has
// ended. If the consumer has called Close, Recv returns ErrStreamClosed
// without waiting for the producer.
func (s *ChanStream[T]) Recv() (T, error) {
	select {
	case item, ok := <-s.items:
		if ok {
			return item, nil
		}
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		var zero T
		return zero, err
	case <-s.done:
		var zero T
		return zero, ErrStreamClosed
	}
}

// Close signals the producer to stop and causes any blocked Recv to return
// ErrStreamClosed. Close is idempotent.
func (s *ChanStream[T]) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

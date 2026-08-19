package runtime

import (
	"errors"
	"io"
	"testing"
	"time"
)

func TestChanStreamNormalEnd(t *testing.T) {
	s := NewChanStream[int](0)
	go func() {
		for i := 0; i < 3; i++ {
			if !s.Send(i) {
				return
			}
		}
		s.CloseWithError(nil)
	}()

	for i := 0; i < 3; i++ {
		got, err := s.Recv()
		if err != nil {
			t.Fatalf("unexpected error at item %d: %v", i, err)
		}
		if got != i {
			t.Fatalf("got %d, want %d", got, i)
		}
	}
	if _, err := s.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF after normal end, got %v", err)
	}
}

func TestChanStreamTerminalError(t *testing.T) {
	s := NewChanStream[int](1)
	boom := errors.New("boom")
	s.Send(1)
	s.CloseWithError(boom)

	got, err := s.Recv()
	if err != nil || got != 1 {
		t.Fatalf("expected buffered item 1, got %d err %v", got, err)
	}
	if _, err := s.Recv(); !errors.Is(err, boom) {
		t.Fatalf("expected terminal error %v, got %v", boom, err)
	}
}

func TestChanStreamCloseUnblocksRecv(t *testing.T) {
	s := NewChanStream[int](0)
	done := make(chan struct{})
	var recvErr error
	go func() {
		_, recvErr = s.Recv()
		close(done)
	}()

	time.Sleep(20 * time.Millisecond) // let Recv start blocking
	s.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Recv did not unblock after Close")
	}
	if !errors.Is(recvErr, ErrStreamClosed) {
		t.Fatalf("expected ErrStreamClosed, got %v", recvErr)
	}
}

func TestChanStreamSendStopsAfterConsumerClose(t *testing.T) {
	s := NewChanStream[int](0)
	s.Close()
	if s.Send(1) {
		t.Fatal("expected Send to return false after consumer Close")
	}
}

func TestChanStreamCommittedFalseBeforeFirstSend(t *testing.T) {
	s := NewChanStream[int](0)
	if s.Committed() {
		t.Fatal("Committed() = true before any Send")
	}
}

func TestChanStreamCommittedTrueAfterFirstSend(t *testing.T) {
	s := NewChanStream[int](1)
	if !s.Send(1) {
		t.Fatal("Send() = false, want true")
	}
	if !s.Committed() {
		t.Fatal("Committed() = false after a successful Send")
	}
}

func TestChanStreamCloseIsIdempotent(t *testing.T) {
	s := NewChanStream[int](0)
	s.Close()
	s.Close() // must not panic
}

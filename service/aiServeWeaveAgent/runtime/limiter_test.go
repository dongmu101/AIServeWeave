package runtime

import (
	"errors"
	"sync"
	"testing"
)

func TestLimiterAcquireUpToMaxThenRejects(t *testing.T) {
	l := NewLimiter(2)

	release1, err := l.Acquire()
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	release2, err := l.Acquire()
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}

	_, err = l.Acquire()
	if !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("acquire 3 = %v, want a RuntimeError wrapping ErrConcurrencyLimit", err)
	}
	var rtErr *RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != ErrorBackpressure {
		t.Fatalf("acquire 3 Code = %v, want %s", err, ErrorBackpressure)
	}

	release1()
	release2()
}

func TestLimiterReleaseAllowsReacquire(t *testing.T) {
	l := NewLimiter(1)

	release, err := l.Acquire()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := l.Acquire(); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("expected limit reached before release, got %v", err)
	}

	release()

	if _, err := l.Acquire(); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestLimiterReleaseIsIdempotent(t *testing.T) {
	l := NewLimiter(1)
	release, err := l.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	release()
	release() // must not double-free the slot

	release2, err := l.Acquire()
	if err != nil {
		t.Fatalf("acquire after idempotent release: %v", err)
	}
	if _, err := l.Acquire(); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("expected the slot to still be occupied exactly once, got %v", err)
	}
	release2()
}

func TestLimiterCloseStopsIssuing(t *testing.T) {
	l := NewLimiter(4)
	l.Close()

	_, err := l.Acquire()
	if !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf("acquire after Close = %v, want a RuntimeError wrapping ErrRuntimeClosed", err)
	}
	var rtErr *RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != ErrorClosed {
		t.Fatalf("acquire after Close Code = %v, want %s", err, ErrorClosed)
	}
}

func TestLimiterUnlimitedWhenMaxNotPositive(t *testing.T) {
	l := NewLimiter(0)
	var releases []func()
	for i := 0; i < 100; i++ {
		release, err := l.Acquire()
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}
}

func TestLimiterConcurrentAcquireRelease(t *testing.T) {
	const max = 8
	l := NewLimiter(max)

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				release, err := l.Acquire()
				if err == nil {
					release()
					return
				}
			}
		}()
	}
	wg.Wait()
}

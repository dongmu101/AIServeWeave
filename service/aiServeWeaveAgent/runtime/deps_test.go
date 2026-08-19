package runtime

import (
	"testing"
	"time"
)

func TestSystemClockNowIsCloseToRealTime(t *testing.T) {
	c := NewSystemClock()
	before := time.Now()
	got := c.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestSystemClockTimerFires(t *testing.T) {
	c := NewSystemClock()
	ch, stop := c.NewTimer(10 * time.Millisecond)
	defer stop()

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timer did not fire within 1s")
	}
}

func TestSystemClockTimerStopPreventsFire(t *testing.T) {
	c := NewSystemClock()
	ch, stop := c.NewTimer(50 * time.Millisecond)
	if !stop() {
		t.Fatal("stop() = false, want true for a timer stopped before firing")
	}

	select {
	case <-ch:
		t.Fatal("stopped timer fired")
	case <-time.After(100 * time.Millisecond):
	}
}

// Compile-time interface satisfaction checks; failures surface at build
// time rather than through a runtime test.
var (
	_ Clock = (*systemClock)(nil)
)

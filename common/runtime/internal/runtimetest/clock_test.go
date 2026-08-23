package runtimetest_test

import (
	"testing"
	"time"

	"AIServeWeave/common/runtime/internal/runtimetest"
)

func TestClockNowStartsAtGivenTime(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := runtimetest.NewClock(start)
	if !c.Now().Equal(start) {
		t.Fatalf("Now() = %v, want %v", c.Now(), start)
	}
}

func TestClockTimerFiresOnAdvance(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := runtimetest.NewClock(start)
	ch, _ := c.NewTimer(10 * time.Second)

	select {
	case <-ch:
		t.Fatal("timer fired before Advance")
	default:
	}

	c.Advance(10 * time.Second)

	select {
	case got := <-ch:
		if !got.Equal(start.Add(10 * time.Second)) {
			t.Fatalf("fired at %v, want %v", got, start.Add(10*time.Second))
		}
	default:
		t.Fatal("timer did not fire after Advance reached its deadline")
	}
}

func TestClockTimerDoesNotFireEarly(t *testing.T) {
	c := runtimetest.NewClock(time.Now())
	ch, _ := c.NewTimer(10 * time.Second)

	c.Advance(5 * time.Second)

	select {
	case <-ch:
		t.Fatal("timer fired before its deadline")
	default:
	}
}

func TestClockTimerStopPreventsFire(t *testing.T) {
	c := runtimetest.NewClock(time.Now())
	ch, stop := c.NewTimer(10 * time.Second)

	if !stop() {
		t.Fatal("stop() = false, want true for a timer stopped before firing")
	}
	c.Advance(20 * time.Second)

	select {
	case <-ch:
		t.Fatal("stopped timer fired")
	default:
	}
}

func TestClockMultipleTimersFireInDeadlineOrder(t *testing.T) {
	c := runtimetest.NewClock(time.Now())
	chLate, _ := c.NewTimer(20 * time.Second)
	chEarly, _ := c.NewTimer(5 * time.Second)

	c.Advance(20 * time.Second)

	select {
	case <-chEarly:
	default:
		t.Fatal("earlier timer did not fire")
	}
	select {
	case <-chLate:
	default:
		t.Fatal("later timer did not fire")
	}
}

package tunnel_test

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMain asserts no test in this package leaks a goroutine, per the
// README quality gate: this is the single place that check happens, so
// individual tests do not need to hand-write it. A short polling window
// tolerates goroutines (GC workers, finalizers) that wind down slightly
// after the last test returns rather than exactly synchronously.
func TestMain(m *testing.M) {
	before := runtime.NumGoroutine()
	code := m.Run()
	if code == 0 && !goroutineCountSettles(before) {
		os.Stderr.WriteString("leaked goroutines detected after tests completed\n")
		code = 1
	}
	os.Exit(code)
}

func goroutineCountSettles(baseline int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if runtime.NumGoroutine() <= baseline {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

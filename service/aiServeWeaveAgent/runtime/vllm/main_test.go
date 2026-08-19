package vllm_test

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMain enforces the package's goroutine-leak gate in one place: every
// ChatStream spawns a background SSE decoder, so a missed Close shows up
// here rather than in whichever test happened to trigger it.
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

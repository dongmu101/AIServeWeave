package ollama_test

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMain enforces the package's goroutine-leak gate in one place: the
// adapter fans out to /api/show during Discover and spawns an SSE decoder
// per ChatStream, so a missed Close would show up here rather than in
// whichever test happened to trigger it.
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

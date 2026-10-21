package claudecode

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMain checks goroutine cleanup for the entire package.
// TestMain 统一检查整个包的协程回收。
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "auth" || os.Args[1] == "--print") {
		if os.Getenv("AISW_CLAUDE_USAGE_FIXTURE") == "1" {
			usageProcessFixture()
			os.Exit(0)
		}
		modelProcessFixture()
		os.Exit(0)
	}
	baseline := runtime.NumGoroutine()
	code := m.Run()
	deadline := time.Now().Add(2 * time.Second)
	for code == 0 && runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "goroutine cleanup: want baseline, got extra goroutines")
			code = 1
			break
		}
		runtime.Gosched()
	}
	os.Exit(code)
}

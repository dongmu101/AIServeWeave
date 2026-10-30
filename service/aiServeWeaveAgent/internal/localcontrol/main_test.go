package localcontrol_test

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMain checks that private control listeners leave no goroutines behind.
// TestMain 统一检查私有控制监听器未遗留协程。
func TestMain(m *testing.M) {
	before := runtime.NumGoroutine()
	code := m.Run()
	deadline := time.Now().Add(2 * time.Second)
	for code == 0 && runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "local control goroutines remain, want initial count")
			code = 1
			break
		}
		runtime.Gosched()
	}
	os.Exit(code)
}

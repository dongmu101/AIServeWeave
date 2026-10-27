package cliusage

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMain checks quota-query goroutine cleanup for the entire package.
// TestMain 统一检查整个包的额度查询协程回收。
func TestMain(m *testing.M) {
	baseline := runtime.NumGoroutine()
	code := m.Run()
	deadline := time.Now().Add(2 * time.Second)
	for code == 0 && runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "quota query goroutines remain, want initial count")
			code = 1
			break
		}
		runtime.Gosched()
	}
	os.Exit(code)
}

//go:build darwin || linux

package geminicli_test

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/geminicli/internal/geminitest"
)

// TestMain supplies the subprocess fixture and checks package-wide goroutine cleanup.
// TestMain 提供子进程测试桩并统一检查本包协程回收。
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && filepath.Base(os.Args[1]) == "index.js" {
		os.Exit(geminitest.Serve(os.Args[1]))
	}
	before := goruntime.NumGoroutine()
	code := m.Run()
	deadline := time.Now().Add(2 * time.Second)
	for code == 0 && goruntime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "goroutines remain, want initial count")
			code = 1
			break
		}
		goruntime.Gosched()
	}
	os.Exit(code)
}
